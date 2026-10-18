package sanitizer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

const (
	// DefaultMaxRetained bounds live/retained sanitizer handles.
	DefaultMaxRetained = 256
	// StderrArtifactName is the target's captured stderr. It is preserved but
	// never treated as trusted sanitizer runtime evidence.
	StderrArtifactName = "stderr.log"
	// SanitizerArtifactName is the bounded, sanitizer-owned runtime log.
	SanitizerArtifactName = "sanitizer.log"
	// AnalysisArtifactName is the normalized report.
	AnalysisArtifactName = "analysis.json"
)

// ErrClosed is returned after Service.Close.
var ErrClosed = errors.New("sanitizer: service is closed")

// BackendError reports an unavailable or unsuitable sanitizer target.
type BackendError struct {
	Reason string
	Err    error
}

func (e *BackendError) Error() string {
	if e.Err != nil {
		return "sanitizer: " + e.Reason + ": " + e.Err.Error()
	}
	return "sanitizer: " + e.Reason
}
func (e *BackendError) Unwrap() error { return e.Err }

// ServiceOptions wires a sanitizer service to the execution foundation.
type ServiceOptions struct {
	Store    *artifacts.Store
	Manager  *jobs.Manager
	Resolver *policy.Resolver
	Roots    []string

	MaxLogBytes int64
	Parser      ParseOptions
	MaxRetained int
}

// Service owns sanitizer runs. It never builds targets.
type Service struct {
	store    *artifacts.Store
	manager  *jobs.Manager
	resolver *policy.Resolver
	roots    []string
	maxLog   int64
	parser   ParseOptions
	retained int

	mu       sync.Mutex
	closed   bool
	analyses map[string]*Analysis
	finished []string
	wg       sync.WaitGroup
}

// NewService builds a sanitizer service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("sanitizer: nil artifact store")
	}
	if opts.Manager == nil {
		return nil, errors.New("sanitizer: nil job manager")
	}
	if opts.Resolver == nil {
		return nil, errors.New("sanitizer: nil policy resolver")
	}
	if opts.MaxLogBytes <= 0 {
		opts.MaxLogBytes = DefaultMaxLogBytes
	}
	if opts.MaxRetained <= 0 {
		opts.MaxRetained = DefaultMaxRetained
	}
	return &Service{
		store: opts.Store, manager: opts.Manager, resolver: opts.Resolver,
		roots: append([]string(nil), opts.Roots...), maxLog: opts.MaxLogBytes,
		parser: opts.Parser, retained: opts.MaxRetained,
		analyses: make(map[string]*Analysis),
	}, nil
}

// Analysis is one sanitizer run handle.
type Analysis struct {
	id          string
	tool        Tool
	programHash string
	programInfo os.FileInfo
	job         *jobs.Job
	runDir      *artifacts.RunDir
	done        chan struct{}

	mu     sync.Mutex
	phase  valgrind.Phase
	report *diagnostic.Report
	err    error
}

// ID returns the opaque run id.
func (a *Analysis) ID() string { return a.id }

// Snapshot returns the current sanitizer state.
func (a *Analysis) Snapshot() valgrind.AnalysisSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return valgrind.AnalysisSnapshot{
		RunID: a.id, Tool: valgrind.Tool(a.tool), Phase: a.phase,
		Report: a.report, Error: errorString(a.err),
	}
}

// JobSnapshot returns the underlying job manifest snapshot.
func (a *Analysis) JobSnapshot() jobs.Snapshot { return a.job.Snapshot() }

// Cancel requests cancellation; poll Snapshot for the final phase.
func (a *Analysis) Cancel() { a.job.Cancel() }

// Wait blocks until post-processing has finished.
func (a *Analysis) Wait(ctx context.Context) (*diagnostic.Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.report, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Start resolves a declared target, validates the sanitizer declaration and
// launches the instrumented binary asynchronously.
func (s *Service) Start(ctx context.Context, req valgrind.Request) (*Analysis, error) {
	if ctx == nil {
		return nil, errors.New("sanitizer: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	tool := Tool(string(req.Tool))
	if !tool.Valid() {
		return nil, &BackendError{Reason: fmt.Sprintf("unsupported sanitizer tool %q", req.Tool)}
	}
	if req.Options != nil {
		return nil, &BackendError{Reason: "sanitizer runs accept no analysis options"}
	}
	resolved, err := s.resolver.Resolve(req.TargetID, req.Args)
	if err != nil {
		return nil, err
	}
	if !contains(resolved.Sanitizers, string(tool)) {
		return nil, &BackendError{Reason: fmt.Sprintf(
			"target %q is not declared as built with sanitizer %s", resolved.TargetID, tool)}
	}
	programHash, programInfo, err := hashProgram(ctx, resolved.Program)
	if err != nil {
		return nil, &BackendError{Reason: "hash sanitizer target", Err: err}
	}
	runDir, err := s.store.Create()
	if err != nil {
		return nil, err
	}
	logPrefix := filepath.Join(runDir.Path(), "sanitizer-log")
	env := append(policy.Environment(runDir.Path()), sanitizerEnvironment(tool, logPrefix)...)
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID: resolved.TargetID, Tool: string(tool), Executable: resolved.Program,
		Args: append([]string(nil), resolved.Args...), Dir: resolved.Dir,
		Env: env, Timeout: req.Timeout,
	})
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return nil, err
	}
	analysis := &Analysis{
		id: runDir.ID(), tool: tool, programHash: programHash, programInfo: programInfo,
		job: job, runDir: runDir, done: make(chan struct{}),
		phase: valgrind.PhaseQueued,
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		job.Cancel()
		_ = s.store.Remove(runDir.ID())
		return nil, ErrClosed
	}
	s.analyses[analysis.id] = analysis
	s.wg.Add(1)
	s.mu.Unlock()
	go s.analyze(analysis, resolved)
	return analysis, nil
}

// Get returns a live or recently finished analysis.
func (s *Service) Get(id string) (*Analysis, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	analysis, ok := s.analyses[id]
	return analysis, ok
}

// Close cancels active runs and waits for post-processing.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	active := make([]*Analysis, 0, len(s.analyses))
	for _, analysis := range s.analyses {
		active = append(active, analysis)
	}
	s.mu.Unlock()
	for _, analysis := range active {
		analysis.job.Cancel()
	}
	s.wg.Wait()
}

func (s *Service) analyze(analysis *Analysis, target policy.Resolved) {
	defer s.wg.Done()
	defer func() {
		close(analysis.done)
		s.retain(analysis.id)
	}()

	analysis.mu.Lock()
	analysis.phase = valgrind.PhaseRunning
	analysis.mu.Unlock()
	jobSnapshot, err := analysis.job.Wait(context.Background())
	if err != nil && !errors.Is(err, context.Canceled) {
		s.fail(analysis, err)
		return
	}
	analysis.mu.Lock()
	analysis.phase = valgrind.PhaseAnalyzing
	analysis.mu.Unlock()

	result := Result{Issues: []Issue{}, Warnings: []string{}}
	var parseErr error
	var artifact diagnostic.Artifact
	logData, foundLog, truncatedLog, readErr := readSanitizerLogs(analysis.runDir, s.maxLog)
	if readErr != nil {
		parseErr = readErr
	}
	if foundLog {
		if writeErr := analysis.runDir.WriteFileAtomic(SanitizerArtifactName, logData); writeErr != nil {
			s.fail(analysis, fmt.Errorf("persist sanitizer log: %w", writeErr))
			return
		}
	} else if writeErr := analysis.runDir.WriteFileAtomic(SanitizerArtifactName, nil); writeErr != nil {
		s.fail(analysis, fmt.Errorf("persist empty sanitizer log: %w", writeErr))
		return
	}
	artifact = diagnostic.Artifact{
		Name: SanitizerArtifactName, Kind: "raw_log", Bytes: int64(len(logData)),
		Truncated: truncatedLog,
	}
	if foundLog {
		parserOpts := s.parser
		if parserOpts.MaxBytes <= 0 || parserOpts.MaxBytes > s.maxLog {
			parserOpts.MaxBytes = s.maxLog
		}
		result, parseErr = Parse(analysis.tool, bytes.NewReader(logData), parserOpts)
		if truncatedLog {
			result.Truncated = true
			result.Incomplete = true
			result.Warnings = append(result.Warnings,
				"sanitizer log exceeded the configured collection bound; later diagnostics were not examined")
		}
		if len(logData) == 0 && stderrSize(analysis.runDir) > 0 {
			result.Incomplete = true
			result.Warnings = append(result.Warnings,
				"sanitizer log is empty but target stderr is non-empty; runtime evidence is ambiguous")
		}
	} else if stderrBytes := stderrSize(analysis.runDir); stderrBytes > 0 {
		result.Incomplete = true
		result.Warnings = append(result.Warnings,
			"sanitizer runtime produced no protected log; target stderr is not trusted as sanitizer evidence")
	}

	program := diagnostic.Program{
		TargetID: target.TargetID, Path: target.Program, SHA256: analysis.programHash,
		Args: append([]string(nil), target.Args...),
	}
	if jobSnapshot.Result != nil {
		program.ExitCode = jobSnapshot.Result.ExitCode
		program.Signal = jobSnapshot.Result.Signal
		program.TimedOut = jobSnapshot.Result.TimedOut
		program.Canceled = jobSnapshot.Result.Canceled
		program.DurationMS = jobSnapshot.Result.DurationMS
	}
	report := BuildReport(BuildInput{
		Tool: analysis.tool, RunID: analysis.id, Roots: s.roots,
		Issues: result.Issues, ParseResult: result, ParseErr: parseErr,
		Program: program, Artifact: artifact,
	})
	if jobSnapshot.State == jobs.StateFailed || jobSnapshot.State == jobs.StateCancelled || jobSnapshot.State == jobs.StateTimedOut {
		report.AnalysisComplete = false
		if report.Result == diagnostic.ResultNoErrorsObserved {
			report.Result = diagnostic.ResultIncomplete
		}
		if jobSnapshot.Error != "" {
			report.Warnings = append(report.Warnings, "job error: "+jobSnapshot.Error)
		}
	}
	if targetChanged(analysis.programInfo, target.Program) {
		report.AnalysisComplete = false
		if report.Result == diagnostic.ResultNoErrorsObserved {
			report.Result = diagnostic.ResultIncomplete
		}
		report.Warnings = append(report.Warnings,
			"sanitizer target changed during the run; the recorded hash is not the executed image")
	}
	if err := report.Validate(); err != nil {
		s.fail(analysis, fmt.Errorf("validate sanitizer report: %w", err))
		return
	}
	data, err := json.Marshal(report)
	if err != nil {
		s.fail(analysis, fmt.Errorf("encode sanitizer report: %w", err))
		return
	}
	if err := analysis.runDir.WriteFileAtomic(AnalysisArtifactName, data); err != nil {
		s.fail(analysis, fmt.Errorf("persist sanitizer report: %w", err))
		return
	}
	analysis.mu.Lock()
	analysis.phase = valgrind.PhaseCompleted
	analysis.report = &report
	analysis.mu.Unlock()
}

func (s *Service) fail(analysis *Analysis, err error) {
	analysis.mu.Lock()
	analysis.phase = valgrind.PhaseFailed
	analysis.err = err
	analysis.mu.Unlock()
}

func (s *Service) retain(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, id)
	for len(s.finished) > s.retained {
		oldest := s.finished[0]
		s.finished = s.finished[1:]
		delete(s.analyses, oldest)
	}
}

func sanitizerEnvironment(tool Tool, logPrefix string) []string {
	base := ""
	switch tool {
	case ToolASan:
		base = "detect_leaks=0:abort_on_error=0"
	case ToolUBSan:
		base = "print_stacktrace=1:halt_on_error=0"
	case ToolLSan:
		base = "report_objects=0"
	case ToolTSan:
		base = "halt_on_error=0"
	default:
		return nil
	}
	if logPrefix == "" {
		return []string{strings.ToUpper(string(tool)) + "_OPTIONS=" + base}
	}
	if tool == ToolLSan {
		return []string{"LSAN_OPTIONS=" + base + ":log_path=" + logPrefix}
	}
	if tool == ToolUBSan {
		return []string{"UBSAN_OPTIONS=" + base + ":log_path=" + logPrefix}
	}
	if tool == ToolTSan {
		return []string{"TSAN_OPTIONS=" + base + ":log_path=" + logPrefix}
	}
	return []string{"ASAN_OPTIONS=" + base + ":log_path=" + logPrefix}
}

func readSanitizerLogs(runDir *artifacts.RunDir, maxBytes int64) ([]byte, bool, bool, error) {
	entries, err := os.ReadDir(runDir.Path())
	if err != nil {
		return nil, false, false, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "sanitizer-log.") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, false, false, nil
	}
	sort.Slice(names, func(i, j int) bool {
		left, leftOK := sanitizerLogPID(names[i])
		right, rightOK := sanitizerLogPID(names[j])
		if leftOK && rightOK && left != right {
			return left < right
		}
		return names[i] < names[j]
	})
	var data []byte
	truncated := false
	for _, name := range names {
		file, err := runDir.OpenRead(name)
		if err != nil {
			return nil, true, true, err
		}
		remaining := maxBytes - int64(len(data))
		if remaining < 0 {
			remaining = 0
		}
		chunk, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		_ = file.Close()
		if readErr != nil {
			return nil, true, true, readErr
		}
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
			truncated = true
		}
		if len(data) > 0 && len(chunk) > 0 && data[len(data)-1] != '\n' {
			if int64(len(data)) >= maxBytes {
				truncated = true
				break
			}
			data = append(data, '\n')
		}
		data = append(data, chunk...)
		if truncated {
			break
		}
	}
	return data, true, truncated, nil
}

func sanitizerLogPID(name string) (uint64, bool) {
	index := strings.LastIndexByte(name, '.')
	if index < 0 || index == len(name)-1 {
		return 0, false
	}
	value, err := strconv.ParseUint(name[index+1:], 10, 64)
	return value, err == nil
}

func stderrSize(runDir *artifacts.RunDir) int64 {
	file, err := runDir.OpenRead(StderrArtifactName)
	if err != nil {
		return 0
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func hashProgram(ctx context.Context, path string) (string, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", nil, readErr
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), info, nil
}

func targetChanged(before os.FileInfo, path string) bool {
	if before == nil {
		return false
	}
	after, err := os.Stat(path)
	if err != nil {
		return true
	}
	return !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
