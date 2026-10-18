package ctest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

const unknownState jobs.State = "unknown"
const interruptedState jobs.State = "interrupted"

const (
	// DefaultTestTimeout is the per-test CTest timeout when configuration omits it.
	DefaultTestTimeout = 30 * time.Second
	// DefaultMaxReportBytes bounds one ctest.json read.
	DefaultMaxReportBytes int64 = 16 << 20
	// DefaultArtifactBytes is the default excerpt size.
	DefaultArtifactBytes = 64 << 10
	// MaxArtifactBytes is the hard excerpt size.
	MaxArtifactBytes = 1 << 20
	// DefaultMaxRetained bounds live CTest handles.
	DefaultMaxRetained = 256
	// maxDiscoveryBytes bounds ctest --show-only output.
	maxDiscoveryBytes int64 = 4 << 20
	// maxRegexBytes bounds the generated exact-name selector well below the
	// CTest/CMake regex implementation limit.
	maxRegexBytes = 16 << 10
)

// RunIDPattern validates opaque run identifiers accepted by the API.
var RunIDPattern = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

// CommandRunner executes one bounded command; *runner.Runner implements it.
type CommandRunner interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}

// BinaryProvider revalidates the CTest binary at invocation time.
type BinaryProvider func() (string, string, error)

// ServiceOptions wires a CTest service to the execution foundation.
type ServiceOptions struct {
	Store    *artifacts.Store
	Manager  *jobs.Manager
	Runner   CommandRunner
	Binary   BinaryProvider
	Roots    []string
	Projects []config.CTestProject

	DefaultTestTimeout time.Duration
	MaxTimeout         time.Duration
	MaxReportBytes     int64
	MaxArtifactBytes   int
	MaxRetained        int
}

// Service owns trusted CTest discovery and asynchronous execution.
type Service struct {
	store    *artifacts.Store
	manager  *jobs.Manager
	runner   CommandRunner
	binary   BinaryProvider
	roots    []string
	projects map[string]config.CTestProject

	defaultTestTimeout time.Duration
	maxTimeout         time.Duration
	maxReportBytes     int64
	maxArtifactBytes   int
	retained           int

	discoverySem chan struct{}

	mu       sync.Mutex
	closed   bool
	runs     map[string]*Run
	finished []string
	wg       sync.WaitGroup
}

// Run is one asynchronous CTest run.
type Run struct {
	id                 string
	project            config.CTestProject
	selected           []string
	job                *jobs.Job
	runDir             *artifacts.RunDir
	testTimeoutSeconds int
	done               chan struct{}
	mu                 sync.Mutex
	phase              Phase
	report             *Report
	err                error
}

// NewService builds a CTest service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("ctest: nil artifact store")
	}
	if opts.Manager == nil {
		return nil, errors.New("ctest: nil job manager")
	}
	if opts.Runner == nil {
		return nil, errors.New("ctest: nil command runner")
	}
	if opts.Binary == nil {
		return nil, errors.New("ctest: nil binary provider")
	}
	if opts.DefaultTestTimeout <= 0 {
		opts.DefaultTestTimeout = DefaultTestTimeout
	}
	if opts.MaxTimeout <= 0 {
		opts.MaxTimeout = 120 * time.Second
	}
	if opts.MaxReportBytes <= 0 || opts.MaxReportBytes > DefaultMaxReportBytes {
		opts.MaxReportBytes = DefaultMaxReportBytes
	}
	if opts.MaxArtifactBytes <= 0 || opts.MaxArtifactBytes > MaxArtifactBytes {
		opts.MaxArtifactBytes = MaxArtifactBytes
	}
	if opts.MaxRetained <= 0 {
		opts.MaxRetained = DefaultMaxRetained
	}
	projects := make(map[string]config.CTestProject, len(opts.Projects))
	for _, project := range opts.Projects {
		project.AllowedTests = append([]string(nil), project.AllowedTests...)
		if project.Profiles != nil {
			copied := make(map[string][]string, len(project.Profiles))
			for id, tests := range project.Profiles {
				copied[id] = append([]string(nil), tests...)
			}
			project.Profiles = copied
		}
		projects[project.ID] = project
	}
	return &Service{
		store: opts.Store, manager: opts.Manager, runner: opts.Runner, binary: opts.Binary,
		roots: append([]string(nil), opts.Roots...), projects: projects,
		defaultTestTimeout: opts.DefaultTestTimeout,
		maxTimeout:         opts.MaxTimeout,
		maxReportBytes:     opts.MaxReportBytes,
		maxArtifactBytes:   opts.MaxArtifactBytes,
		retained:           opts.MaxRetained,
		runs:               make(map[string]*Run),
		discoverySem:       make(chan struct{}, 1),
	}, nil
}

// HasUsableProject reports whether at least one declared project directory
// currently exists. It performs no execution.
func (s *Service) HasUsableProject() bool {
	for _, project := range s.projects {
		if len(project.AllowedTests) == 0 {
			continue
		}
		if usableProjectDir(project, s.roots) {
			return true
		}
	}
	return false
}

// ProjectIDs returns configured project ids in stable order.
func (s *Service) ProjectIDs() []string {
	ids := make([]string, 0, len(s.projects))
	for id := range s.projects {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids
}

// Discover lists configured and declared tests without executing them.
func (s *Service) Discover(ctx context.Context, req DiscoverRequest) (Discovery, error) {
	if ctx == nil {
		return Discovery{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Discovery{}, mapContextError(err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Discovery{}, Errorf(CodeClosed, "CTest service is closed")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	project, err := s.project(req.ProjectID)
	if err != nil {
		return Discovery{}, err
	}
	if len(project.AllowedTests) == 0 {
		return Discovery{}, Errorf(CodeInvalidArgument,
			"project %q has no allowed_tests", project.ID)
	}
	resolvedDir, err := ensureProjectDir(project, s.roots)
	if err != nil {
		return Discovery{}, err
	}
	select {
	case s.discoverySem <- struct{}{}:
		defer func() { <-s.discoverySem }()
	case <-ctx.Done():
		return Discovery{}, mapContextError(ctx.Err())
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Discovery{}, Errorf(CodeClosed, "CTest service is closed")
	}
	binary, _, err := s.binary()
	if err != nil {
		return Discovery{}, Wrap(CodeBackendUnavailable, err, "CTest backend is unavailable")
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	result, err := s.runner.Run(ctx, runner.Request{
		Executable:     binary,
		Args:           []string{"--show-only=json-v1"},
		Dir:            resolvedDir,
		Env:            policy.Environment(""),
		Timeout:        minDuration(20*time.Second, s.maxTimeout),
		MaxOutputBytes: maxDiscoveryBytes,
		Stdout:         stdout,
		Stderr:         stderr,
	})
	if err != nil {
		return Discovery{}, Wrap(CodeDiscoveryFailed, err, "run ctest discovery")
	}
	if !result.Started {
		return Discovery{}, Errorf(CodeDiscoveryFailed, "ctest discovery did not start")
	}
	if result.ExitCode != 0 {
		return Discovery{}, Errorf(CodeDiscoveryFailed,
			"ctest discovery exited with %d: %s", result.ExitCode, boundedText(stderr.String(), 512))
	}
	if result.StdoutTruncated {
		return Discovery{}, Errorf(CodeDiscoveryFailed, "ctest discovery output was truncated")
	}
	discovery, err := parseDiscovery(stdout.Bytes(), project)
	if err != nil {
		return Discovery{}, Wrap(CodeDiscoveryFailed, err, "parse ctest discovery")
	}
	if result.StderrBytes > 0 {
		discovery.Warnings = append(discovery.Warnings,
			"ctest discovery wrote to stderr: "+boundedText(stderr.String(), 512))
	}
	return discovery, nil
}

// Start validates a trusted selection and launches CTest asynchronously.
func (s *Service) Start(ctx context.Context, req RunRequest) (Status, error) {
	if ctx == nil {
		return Status{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, mapContextError(err)
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Status{}, Errorf(CodeClosed, "CTest service is closed")
	}
	project, err := s.project(req.ProjectID)
	if err != nil {
		return Status{}, err
	}
	resolvedDir, err := ensureProjectDir(project, s.roots)
	if err != nil {
		return Status{}, err
	}
	selected, err := selectTests(project, req)
	if err != nil {
		return Status{}, err
	}
	binary, _, err := s.binary()
	if err != nil {
		return Status{}, Wrap(CodeBackendUnavailable, err, "CTest backend is unavailable")
	}
	overallTimeout, err := s.resolveTimeout(req.TimeoutMS)
	if err != nil {
		return Status{}, err
	}
	testTimeoutSeconds, err := s.resolveTestTimeout(req.TestTimeoutMS)
	if err != nil {
		return Status{}, err
	}
	selector, err := exactTestSelector(selected)
	if err != nil {
		return Status{}, err
	}
	runDir, err := s.store.Create()
	if err != nil {
		return Status{}, mapStartError(err)
	}
	junitPath := filepath.Join(runDir.Path(), JUnitArtifactName)
	args := []string{
		"-FA", ".*",
		"--output-on-failure",
		"--output-junit", junitPath,
		"--timeout", strconv.Itoa(testTimeoutSeconds),
		"--no-tests=error",
		"--tests-regex", selector,
	}
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID:   project.ID,
		Tool:       ToolName,
		Executable: binary,
		Args:       args,
		Dir:        resolvedDir,
		Env:        policy.Environment(runDir.Path()),
		Timeout:    overallTimeout,
	})
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return Status{}, mapStartError(err)
	}
	run := &Run{
		id: runDir.ID(), project: project, selected: selected, job: job,
		runDir: runDir, testTimeoutSeconds: testTimeoutSeconds,
		done: make(chan struct{}), phase: PhaseQueued,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		job.Cancel()
		_ = s.store.Remove(runDir.ID())
		return Status{}, Errorf(CodeClosed, "CTest service is closed")
	}
	s.runs[run.id] = run
	s.wg.Add(1)
	s.mu.Unlock()
	go s.analyze(run)
	return s.statusFromRun(run), nil
}

// Get returns one live CTest run.
func (s *Service) Get(id string) (*Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return run, ok
}

// Wait blocks until post-processing completes.
func (r *Run) Wait(ctx context.Context) (*Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.report, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ID returns the run identifier.
func (r *Run) ID() string { return r.id }

// Cancel requests cancellation.
func (r *Run) Cancel() { r.job.Cancel() }

// JobSnapshot returns the underlying execution manifest.
func (r *Run) JobSnapshot() jobs.Snapshot { return r.job.Snapshot() }

// Snapshot returns the current status.
func (r *Run) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.job.Snapshot()
	status := Status{
		SchemaVersion: SchemaVersion,
		RunID:         r.id,
		ProjectID:     r.project.ID,
		Tool:          ToolName,
		State:         snapshot.State,
		Phase:         r.phase,
		ElapsedMS:     elapsedMS(snapshot),
		Warnings:      []string{},
	}
	if r.report != nil {
		status.Result = r.report.Result
		status.Complete = r.report.Complete
		status.Summary = r.report.Summary
		status.Warnings = append(status.Warnings, r.report.Warnings...)
	}
	if snapshot.Result != nil && snapshot.Result.DurationMS > 0 {
		status.ElapsedMS = snapshot.Result.DurationMS
	}
	status.StartedAt = formatTimePtr(snapshot.StartedAt)
	status.FinishedAt = formatTimePtr(snapshot.FinishedAt)
	status.Error = snapshot.Error
	if r.err != nil {
		status.Error = r.err.Error()
	}
	return status
}

func (s *Service) analyze(run *Run) {
	defer s.wg.Done()
	defer func() {
		close(run.done)
		s.retain(run.id)
	}()
	run.mu.Lock()
	run.phase = PhaseRunning
	run.mu.Unlock()
	snapshot, err := run.job.Wait(context.Background())
	if err != nil && !errors.Is(err, context.Canceled) {
		s.fail(run, err)
		return
	}
	run.mu.Lock()
	run.phase = PhaseAnalyzing
	run.mu.Unlock()

	report := Report{
		SchemaVersion: SchemaVersion,
		RunID:         run.id,
		ProjectID:     run.project.ID,
		Tool:          ToolName,
		Selected:      append([]string(nil), run.selected...),
		Tests:         []TestResult{},
		Artifacts:     []diagnostic.Artifact{},
		Warnings:      []string{},
	}
	issues, parseErr, artifact, warnings, truncated := s.readJUnit(run, &report)
	report.Warnings = append(report.Warnings, warnings...)
	if artifact.Name != "" {
		report.Artifacts = append(report.Artifacts, artifact)
	}
	report.Artifacts = append(report.Artifacts, s.outputArtifacts(run, snapshot)...)
	complete := parseErr == nil && !truncated
	if parseErr != nil {
		report.Warnings = append(report.Warnings, "CTest JUnit evidence is unavailable: "+parseErr.Error())
	}
	if snapshot.Result == nil {
		complete = false
		report.Warnings = append(report.Warnings, "CTest process result is missing")
	} else if !snapshot.Result.Started {
		complete = false
		report.Warnings = append(report.Warnings, "CTest process did not start")
	} else if snapshot.Result.TimedOut {
		complete = false
		report.Warnings = append(report.Warnings, "CTest overall run timed out")
	} else if snapshot.Result.Canceled {
		complete = false
		report.Warnings = append(report.Warnings, "CTest run was canceled")
	}
	if snapshot.State == jobs.StateFailed || snapshot.State == jobs.StateCancelled || snapshot.State == jobs.StateTimedOut {
		complete = false
	}
	if snapshot.Result != nil {
		if snapshot.Result.StdoutTruncated {
			report.Warnings = append(report.Warnings, "CTest stdout was truncated at the output bound")
		}
		if snapshot.Result.StderrTruncated {
			report.Warnings = append(report.Warnings, "CTest stderr was truncated at the output bound")
		}
	}

	issuesByName := make(map[string]junitIssue, len(issues))
	for _, issue := range issues {
		if _, duplicate := issuesByName[issue.Name]; duplicate {
			complete = false
			report.Warnings = append(report.Warnings, "JUnit contains duplicate testcase "+issue.Name)
			continue
		}
		issuesByName[issue.Name] = issue
	}
	selectedSet := make(map[string]struct{}, len(run.selected))
	for _, name := range run.selected {
		selectedSet[name] = struct{}{}
	}
	selectionViolation := false
	for name := range issuesByName {
		if _, ok := selectedSet[name]; !ok {
			selectionViolation = true
			report.Warnings = append(report.Warnings, "JUnit contains an unselected testcase "+name)
		}
	}
	for _, name := range run.selected {
		issue, ok := issuesByName[name]
		if !ok {
			report.Tests = append(report.Tests, TestResult{Name: name, Status: TestNotRun})
			report.Warnings = append(report.Warnings, "selected test has no JUnit result: "+name)
			complete = false
			continue
		}
		status := issue.Status
		message := issue.Message
		if status == TestPassed && run.testTimeoutSeconds > 0 &&
			issue.DurationMS > int64(run.testTimeoutSeconds)*1000 {
			status = TestTimeout
			message = fmt.Sprintf("test exceeded the %d-second per-test limit", run.testTimeoutSeconds)
			report.Warnings = append(report.Warnings, "test "+name+" exceeded the per-test limit")
		}
		report.Tests = append(report.Tests, TestResult{
			Name: name, Status: status, DurationMS: issue.DurationMS, Message: message,
		})
	}
	report.Summary = summarize(report.Tests)
	if report.Summary.Failed > 0 || report.Summary.Timeout > 0 {
		report.Result = ResultFailed
	} else if !complete {
		report.Result = ResultIncomplete
	} else {
		report.Result = ResultPassed
	}
	if report.Summary.Skipped > 0 {
		report.Warnings = append(report.Warnings, "skipped tests were not executed and are not evidence of correctness")
		if report.Result == ResultPassed {
			report.Result = ResultIncomplete
		}
	}
	if selectionViolation {
		complete = false
		report.Result = ResultError
	}
	if snapshot.Result != nil && snapshot.Result.ExitCode != 0 && report.Summary.Failed == 0 && report.Summary.Timeout == 0 {
		complete = false
		if report.Result == ResultPassed {
			report.Result = ResultIncomplete
		}
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("CTest exited with %d without a parsed failing test", snapshot.Result.ExitCode))
	}
	report.Complete = complete
	if err := validateReport(report); err != nil {
		s.fail(run, err)
		return
	}
	data, err := json.Marshal(report)
	if err != nil {
		s.fail(run, err)
		return
	}
	if err := run.runDir.WriteFileAtomic(ReportArtifactName, data); err != nil {
		s.fail(run, err)
		return
	}
	run.mu.Lock()
	run.phase = finalPhase(snapshot)
	run.report = &report
	run.mu.Unlock()
}

func (s *Service) readJUnit(run *Run, report *Report) ([]junitIssue, error, diagnostic.Artifact, []string, bool) {
	warnings := []string{}
	file, err := run.runDir.OpenRead(JUnitArtifactName)
	if err != nil {
		return nil, err, diagnostic.Artifact{}, warnings, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err, diagnostic.Artifact{}, warnings, false
	}
	artifact := diagnostic.Artifact{Name: JUnitArtifactName, Kind: "junit_xml", Bytes: info.Size()}
	limit := s.maxReportBytes
	if info.Size() > limit {
		artifact.Truncated = true
		warnings = append(warnings, "CTest JUnit report exceeded the report read bound")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	if readErr != nil {
		return nil, readErr, artifact, warnings, true
	}
	if int64(len(data)) > limit {
		data = data[:limit]
	}
	issues, parseWarnings, err := parseJUnit(data)
	warnings = append(warnings, parseWarnings...)
	return issues, err, artifact, warnings, artifact.Truncated
}

func (s *Service) outputArtifacts(run *Run, snapshot jobs.Snapshot) []diagnostic.Artifact {
	artifacts := []diagnostic.Artifact{}
	for _, item := range []struct {
		name      string
		kind      string
		truncated bool
	}{
		{StdoutArtifactName, "stdout", snapshot.Result != nil && snapshot.Result.StdoutTruncated},
		{StderrArtifactName, "stderr", snapshot.Result != nil && snapshot.Result.StderrTruncated},
	} {
		file, err := run.runDir.OpenRead(item.name)
		if err != nil {
			continue
		}
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr != nil {
			continue
		}
		artifacts = append(artifacts, diagnostic.Artifact{
			Name: item.name, Kind: item.kind, Bytes: info.Size(), Truncated: item.truncated,
		})
	}
	return artifacts
}

func (s *Service) fail(run *Run, err error) {
	run.mu.Lock()
	run.phase = PhaseFailed
	run.err = err
	run.mu.Unlock()
}

func (s *Service) retain(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, id)
	for len(s.finished) > s.retained {
		oldest := s.finished[0]
		s.finished = s.finished[1:]
		delete(s.runs, oldest)
	}
}

// Close cancels active runs and waits for post-processing.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	active := make([]*Run, 0, len(s.runs))
	for _, run := range s.runs {
		active = append(active, run)
	}
	s.mu.Unlock()
	for _, run := range active {
		run.Cancel()
	}
	s.wg.Wait()
}

func (s *Service) project(id string) (config.CTestProject, error) {
	project, ok := s.projects[id]
	if !ok {
		return config.CTestProject{}, Errorf(CodeProjectUnknown, "CTest project %q is not declared", id)
	}
	return project, nil
}

func ensureProjectDir(project config.CTestProject, roots []string) (string, error) {
	resolved, err := resolvedProjectDir(project, roots)
	if err != nil {
		return "", Errorf(CodeProjectUnavailable,
			"CTest project %q test directory is unavailable or outside workspace roots", project.ID)
	}
	return resolved, nil
}

func usableProjectDir(project config.CTestProject, roots []string) bool {
	_, err := resolvedProjectDir(project, roots)
	return err == nil
}

func resolvedProjectDir(project config.CTestProject, roots []string) (string, error) {
	resolved, err := filepath.EvalSymlinks(project.TestDir)
	if err != nil {
		return "", err
	}
	if len(roots) > 0 && !withinAny(roots, resolved) {
		return "", errors.New("test directory resolves outside workspace roots")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("test directory is unavailable")
	}
	testFile := filepath.Join(resolved, "CTestTestfile.cmake")
	fileInfo, err := os.Lstat(testFile)
	if err != nil || !fileInfo.Mode().IsRegular() {
		return "", errors.New("CTestTestfile.cmake is unavailable or not a regular file")
	}
	return resolved, nil
}

func withinAny(roots []string, path string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func selectTests(project config.CTestProject, req RunRequest) ([]string, error) {
	if len(project.AllowedTests) == 0 {
		return nil, Errorf(CodeInvalidArgument, "project %q has no allowed_tests", project.ID)
	}
	if req.Profile != "" && len(req.Tests) > 0 {
		return nil, Errorf(CodeInvalidArgument, "profile and tests are mutually exclusive")
	}
	allowed := make(map[string]struct{}, len(project.AllowedTests))
	for _, name := range project.AllowedTests {
		allowed[name] = struct{}{}
	}
	var selected []string
	switch {
	case req.Profile != "":
		tests, ok := project.Profiles[req.Profile]
		if !ok {
			return nil, Errorf(CodeInvalidArgument, "profile %q is not declared", req.Profile)
		}
		selected = append([]string(nil), tests...)
	case len(req.Tests) > 0:
		selected = append([]string(nil), req.Tests...)
	default:
		selected = append([]string(nil), project.AllowedTests...)
	}
	if len(selected) == 0 {
		return nil, Errorf(CodeInvalidArgument, "selection contains no tests")
	}
	seen := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		if _, ok := allowed[name]; !ok {
			return nil, Errorf(CodeInvalidArgument, "test %q is not in allowed_tests", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, Errorf(CodeInvalidArgument, "duplicate test %q", name)
		}
		seen[name] = struct{}{}
	}
	return selected, nil
}

func exactTestSelector(tests []string) (string, error) {
	parts := make([]string, len(tests))
	for i, name := range tests {
		parts[i] = regexp.QuoteMeta(name)
	}
	selector := "^(" + strings.Join(parts, "|") + ")$"
	if len(selector) > maxRegexBytes {
		return "", Errorf(CodeInvalidArgument, "test selection is too large for CTest")
	}
	return selector, nil
}

func (s *Service) resolveTimeout(timeoutMS int) (time.Duration, error) {
	timeout := s.maxTimeout
	if timeoutMS != 0 {
		if timeoutMS < 0 {
			return 0, Errorf(CodeInvalidArgument, "timeout_ms must not be negative")
		}
		requested := time.Duration(timeoutMS) * time.Millisecond
		if requested > s.maxTimeout {
			return 0, Errorf(CodeInvalidArgument, "timeout_ms exceeds the configured maximum")
		}
		timeout = requested
	}
	if timeout <= 0 {
		return 0, Errorf(CodeInvalidArgument, "timeout must be positive")
	}
	return timeout, nil
}

func (s *Service) resolveTestTimeout(timeoutMS int) (int, error) {
	maxMS := int(s.defaultTestTimeout / time.Millisecond)
	if timeoutMS == 0 {
		return int(s.defaultTestTimeout / time.Second), nil
	}
	if timeoutMS < 0 || timeoutMS > maxMS {
		return 0, Errorf(CodeInvalidArgument,
			"test_timeout_ms must be between 1 and %d", maxMS)
	}
	seconds := (timeoutMS + 999) / 1000
	if seconds < 1 {
		seconds = 1
	}
	return seconds, nil
}

func summarize(tests []TestResult) Summary {
	summary := Summary{Selected: len(tests)}
	for _, test := range tests {
		switch test.Status {
		case TestPassed:
			summary.Passed++
		case TestFailed:
			summary.Failed++
		case TestTimeout:
			summary.Timeout++
		case TestSkipped:
			summary.Skipped++
		default:
			summary.NotRun++
		}
	}
	return summary
}

func validateReport(report Report) error {
	if report.RunID == "" || report.ProjectID == "" {
		return Errorf(CodeInternal, "CTest report is missing run/project identity")
	}
	if report.Result == "" || report.Summary.Selected != len(report.Tests) {
		return Errorf(CodeInternal, "CTest report has an inconsistent summary")
	}
	return nil
}

func minDuration(left, right time.Duration) time.Duration {
	if left <= 0 || (right > 0 && right < left) {
		return right
	}
	return left
}

func mapStartError(err error) error {
	var limitErr *jobs.LimitError
	if errors.As(err, &limitErr) {
		return Wrap(CodeQuotaExceeded, err, "concurrent CTest limit reached")
	}
	if errors.Is(err, artifacts.ErrQuota) || errors.Is(err, artifacts.ErrFileQuota) {
		return Wrap(CodeQuotaExceeded, err, "artifact quota exceeded")
	}
	if errors.Is(err, jobs.ErrClosed) {
		return Wrap(CodeClosed, err, "CTest job manager is closed")
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "request deadline exceeded")
	}
	return Wrap(CodeInternal, err, "CTest run could not be started")
}

func mapContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "request deadline exceeded")
	}
	return Wrap(CodeInternal, err, "request failed")
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func elapsedMS(snapshot jobs.Snapshot) int64 {
	if snapshot.StartedAt == nil {
		return 0
	}
	end := time.Now()
	if snapshot.FinishedAt != nil {
		end = *snapshot.FinishedAt
	}
	return end.Sub(*snapshot.StartedAt).Milliseconds()
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// trimIncompleteRune is kept local so artifact excerpts never return a partial
// UTF-8 sequence when they stop before EOF.
func trimIncompleteRune(data []byte) []byte {
	if len(data) == 0 || utf8.Valid(data) {
		return data
	}
	start := len(data) - 1
	for start >= 0 && len(data)-start < utf8.UTFMax && !utf8.RuneStart(data[start]) {
		start--
	}
	if start >= 0 && !utf8.FullRune(data[start:]) {
		return data[:start]
	}
	return data
}

// Status returns live or persisted lifecycle information for one run.
func (s *Service) Status(ctx context.Context, req StatusRequest) (Status, error) {
	if ctx == nil {
		return Status{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, mapContextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return Status{}, err
	}
	if run, ok := s.Get(req.RunID); ok {
		return s.statusFromRun(run), nil
	}
	return s.statusFromDisk(req.RunID)
}

func (s *Service) statusFromRun(run *Run) Status {
	return run.Snapshot()
}

func (s *Service) statusFromDisk(runID string) (Status, error) {
	if err := s.ensureCTestRun(runID); err != nil {
		return Status{}, err
	}
	status := Status{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		Tool:          ToolName,
		State:         unknownState,
		Phase:         Phase(""),
		Warnings:      []string{"run is not tracked by this server process; status is read from persisted evidence"},
	}
	manifest, manifestErr := readJobManifest(s.store, runID)
	if manifestErr == nil {
		status.ProjectID = manifest.TargetID
		status.State = manifest.State
		status.StartedAt = formatTimePtr(manifest.StartedAt)
		status.FinishedAt = formatTimePtr(manifest.FinishedAt)
		status.Error = manifest.Error
		if manifest.Result != nil {
			status.ElapsedMS = manifest.Result.DurationMS
		}
	} else if !errors.Is(manifestErr, os.ErrNotExist) {
		status.Warnings = append(status.Warnings, "job manifest is unreadable: "+manifestErr.Error())
	}
	report, reportErr := readPersistedReport(s.store, runID)
	hasReport := reportErr == nil
	if hasReport {
		status.ProjectID = report.ProjectID
		status.Result = report.Result
		status.Complete = report.Complete
		status.Summary = report.Summary
		status.Warnings = append(status.Warnings, report.Warnings...)
		if status.State == unknownState && report.Complete {
			status.State = jobs.StateCompleted
		}
	} else if errors.Is(reportErr, os.ErrNotExist) {
		if status.State == jobs.StateCompleted || status.State == jobs.StateFailed ||
			status.State == jobs.StateCancelled || status.State == jobs.StateTimedOut {
			status.Warnings = append(status.Warnings,
				"persisted CTest run has no normalized report; result and per-test evidence are unavailable")
		}
	} else {
		status.Warnings = append(status.Warnings, "CTest report is unreadable: "+reportErr.Error())
	}
	if status.State == jobs.StateQueued || status.State == jobs.StateRunning {
		status.State = interruptedState
		status.Complete = false
		status.Warnings = append(status.Warnings,
			"persisted CTest run is not active in this server process; its manifest may be stale")
	}
	status.Phase = phaseFromState(status.State)
	if status.Phase == "" && hasReport {
		status.Phase = PhaseCompleted
	}
	if status.ElapsedMS == 0 && status.StartedAt != "" && status.FinishedAt != "" {
		started, startErr := time.Parse(time.RFC3339Nano, status.StartedAt)
		finished, finishErr := time.Parse(time.RFC3339Nano, status.FinishedAt)
		if startErr == nil && finishErr == nil {
			status.ElapsedMS = finished.Sub(started).Milliseconds()
		}
	}
	return status, nil
}

// Report returns one bounded per-test page.
func (s *Service) Report(ctx context.Context, req ReportRequest) (Report, error) {
	if ctx == nil {
		return Report{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Report{}, mapContextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return Report{}, err
	}
	if run, ok := s.Get(req.RunID); ok {
		run.mu.Lock()
		report := run.report
		phase := run.phase
		err := run.err
		run.mu.Unlock()
		if report != nil {
			result := copyReport(*report)
			return limitReport(result, req.Limit), nil
		}
		if err != nil {
			return Report{}, Wrap(CodeReportUnavailable, err, "CTest report is unavailable")
		}
		return Report{}, Errorf(CodeReportUnavailable, "CTest run is %s; report is not available", phase)
	}
	if err := s.ensureCTestRun(req.RunID); err != nil {
		return Report{}, err
	}
	report, err := readPersistedReport(s.store, req.RunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !s.store.RunExists(req.RunID) {
				return Report{}, Errorf(CodeRunNotFound, "CTest run %q does not exist", req.RunID)
			}
			return Report{}, Wrap(CodeReportUnavailable, err, "CTest report is not available")
		}
		return Report{}, Wrap(CodeReportUnavailable, err, "read CTest report")
	}
	return limitReport(report, req.Limit), nil
}

func limitReport(report Report, requested int) Report {
	report = normalizeReport(report)
	limit := requested
	if limit <= 0 {
		limit = 100
	}
	if limit > config.MaxCTestAllowedTests {
		limit = config.MaxCTestAllowedTests
	}
	if len(report.Tests) > limit {
		report.Tests = report.Tests[:limit]
		report.HasMore = true
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("per-test results were limited to %d; request a larger limit if needed", limit))
	}
	return report
}

func copyReport(report Report) Report {
	return normalizeReport(report)
}

func normalizeReport(report Report) Report {
	report.Selected = cloneStrings(report.Selected)
	report.Tests = cloneTests(report.Tests)
	report.Artifacts = cloneArtifacts(report.Artifacts)
	report.Warnings = cloneStrings(report.Warnings)
	return report
}

func cloneStrings(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneTests(values []TestResult) []TestResult {
	result := make([]TestResult, len(values))
	copy(result, values)
	return result
}

func cloneArtifacts(values []diagnostic.Artifact) []diagnostic.Artifact {
	result := make([]diagnostic.Artifact, len(values))
	copy(result, values)
	return result
}

// Cancel requests cancellation for a live run. Persisted runs are not active.
func (s *Service) Cancel(ctx context.Context, req CancelRequest) (CancelResult, error) {
	if ctx == nil {
		return CancelResult{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return CancelResult{}, mapContextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return CancelResult{}, err
	}
	if run, ok := s.Get(req.RunID); ok {
		status := run.Snapshot()
		jobSnapshot := run.JobSnapshot()
		if terminalPhase(status.Phase) || terminalJobState(jobSnapshot.State) {
			return CancelResult{
				SchemaVersion: SchemaVersion, RunID: req.RunID, Canceled: false,
				State: status.State, Phase: status.Phase,
			}, nil
		}
		run.Cancel()
		status = run.Snapshot()
		return CancelResult{
			SchemaVersion: SchemaVersion, RunID: req.RunID, Canceled: true,
			State: status.State, Phase: status.Phase,
		}, nil
	}
	if err := s.ensureCTestRun(req.RunID); err != nil {
		return CancelResult{}, err
	}
	status, statusErr := s.statusFromDisk(req.RunID)
	if statusErr != nil {
		return CancelResult{}, statusErr
	}
	return CancelResult{
		SchemaVersion: SchemaVersion, RunID: req.RunID, Canceled: false,
		State: status.State, Phase: status.Phase,
	}, nil
}

// Artifact returns a bounded UTF-8 excerpt of one allowlisted CTest artifact.
func (s *Service) Artifact(ctx context.Context, req ArtifactRequest) (ArtifactOutput, error) {
	if ctx == nil {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, mapContextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return ArtifactOutput{}, err
	}
	if req.Offset < 0 {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "offset must not be negative")
	}
	if req.MaxBytes < 0 {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument, "max_bytes must not be negative")
	}
	name, err := artifactName(req.ArtifactType)
	if err != nil {
		return ArtifactOutput{}, err
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultArtifactBytes
	}
	if maxBytes > s.maxArtifactBytes {
		maxBytes = s.maxArtifactBytes
	}
	if err := s.ensureCTestRun(req.RunID); err != nil {
		return ArtifactOutput{}, err
	}
	file, err := s.store.OpenArtifact(req.RunID, name)
	if err != nil {
		if !s.store.RunExists(req.RunID) {
			return ArtifactOutput{}, Wrap(CodeRunNotFound, err, "CTest run %q does not exist", req.RunID)
		}
		return ArtifactOutput{}, Wrap(CodeArtifactUnavailable, err, "artifact %q is unavailable", req.ArtifactType)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactUnavailable, err, "inspect artifact %q", req.ArtifactType)
	}
	if req.Offset > info.Size() {
		return ArtifactOutput{}, Errorf(CodeInvalidArgument,
			"offset %d exceeds the %d-byte artifact", req.Offset, info.Size())
	}
	data, err := io.ReadAll(io.NewSectionReader(file, req.Offset, int64(maxBytes)))
	if err != nil {
		return ArtifactOutput{}, Wrap(CodeArtifactUnavailable, err, "read artifact %q", req.ArtifactType)
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, mapContextError(err)
	}
	if req.Offset+int64(len(data)) < info.Size() {
		raw := data
		data = trimIncompleteRune(data)
		if len(data) == 0 && len(raw) > 0 {
			data = raw[:1]
		}
	}
	content := string(data)
	invalid := !utf8.Valid(data)
	if invalid {
		content = strings.ToValidUTF8(content, "\uFFFD")
	}
	return ArtifactOutput{
		SchemaVersion: SchemaVersion, RunID: req.RunID, ArtifactType: req.ArtifactType,
		FileName: name, Bytes: info.Size(), Offset: req.Offset, ReturnedBytes: len(data),
		MaxBytes: maxBytes, HasMore: req.Offset+int64(len(data)) < info.Size(),
		NextOffset: req.Offset + int64(len(data)), Content: content,
		InvalidUTF8Replaced: invalid,
	}, nil
}

func artifactName(artifactType ArtifactType) (string, error) {
	switch artifactType {
	case ArtifactJUnit:
		return JUnitArtifactName, nil
	case ArtifactStdout:
		return StdoutArtifactName, nil
	case ArtifactStderr:
		return StderrArtifactName, nil
	case ArtifactReport:
		return ReportArtifactName, nil
	default:
		return "", Errorf(CodeInvalidArgument,
			"artifact_type must be one of junit, stdout, stderr, report; got %q", artifactType)
	}
}

func finalPhase(snapshot jobs.Snapshot) Phase {
	if snapshot.State == jobs.StateCancelled || (snapshot.Result != nil && snapshot.Result.Canceled) {
		return PhaseCanceled
	}
	if snapshot.State == jobs.StateTimedOut || (snapshot.Result != nil && snapshot.Result.TimedOut) {
		return PhaseTimedOut
	}
	if snapshot.State == jobs.StateFailed {
		return PhaseFailed
	}
	return PhaseCompleted
}

func terminalJobState(state jobs.State) bool {
	switch state {
	case jobs.StateCompleted, jobs.StateFailed, jobs.StateCancelled, jobs.StateTimedOut:
		return true
	default:
		return false
	}
}

func terminalPhase(phase Phase) bool {
	switch phase {
	case PhaseCompleted, PhaseFailed, PhaseCanceled, PhaseTimedOut:
		return true
	default:
		return false
	}
}

func (s *Service) ensureCTestRun(runID string) error {
	manifest, manifestErr := readJobManifest(s.store, runID)
	if manifestErr == nil {
		if manifest.Tool == ToolName {
			return nil
		}
		if manifest.Tool != "" {
			return Errorf(CodeRunNotFound, "CTest run %q does not exist", runID)
		}
	}
	if _, reportErr := readPersistedReport(s.store, runID); reportErr == nil {
		return nil
	}
	if !s.store.RunExists(runID) {
		return Errorf(CodeRunNotFound, "CTest run %q does not exist", runID)
	}
	return Errorf(CodeRunNotFound, "CTest run %q does not exist", runID)
}

func validateRunID(runID string) error {
	if !RunIDPattern.MatchString(runID) {
		return Errorf(CodeInvalidArgument, "run_id must match %s", RunIDPattern)
	}
	return nil
}

func readJobManifest(store *artifacts.Store, runID string) (jobs.Snapshot, error) {
	file, err := store.OpenArtifact(runID, jobs.ManifestName)
	if err != nil {
		return jobs.Snapshot{}, err
	}
	defer file.Close()
	var snapshot jobs.Snapshot
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&snapshot); err != nil {
		return jobs.Snapshot{}, err
	}
	return snapshot, nil
}

func readPersistedReport(store *artifacts.Store, runID string) (Report, error) {
	file, err := store.OpenArtifact(runID, ReportArtifactName)
	if err != nil {
		return Report{}, err
	}
	defer file.Close()
	var report Report
	decoder := json.NewDecoder(io.LimitReader(file, DefaultMaxReportBytes))
	if err := decoder.Decode(&report); err != nil {
		return Report{}, err
	}
	if report.RunID != runID || report.Tool != ToolName {
		return Report{}, Errorf(CodeReportInvalid, "CTest report identity does not match the requested run")
	}
	if err := validateReport(report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func phaseFromState(state jobs.State) Phase {
	switch state {
	case jobs.StateQueued:
		return PhaseQueued
	case jobs.StateRunning:
		return PhaseRunning
	case jobs.StateCompleted:
		return PhaseCompleted
	case jobs.StateCancelled:
		return PhaseCanceled
	case jobs.StateFailed:
		return PhaseFailed
	case jobs.StateTimedOut:
		return PhaseTimedOut
	default:
		return Phase("")
	}
}
