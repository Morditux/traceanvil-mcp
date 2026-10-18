package valgrind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// ErrClosed is returned when Start is called after Close.
var ErrClosed = errors.New("valgrind: service is closed")

// ErrBackendUnavailable identifies a missing or unusable Valgrind executable.
var ErrBackendUnavailable = errors.New("valgrind: backend unavailable")

// BackendError reports an unusable backend or suppression file.
type BackendError struct {
	Reason string
	Err    error
}

func (e *BackendError) Error() string {
	if e.Err != nil {
		return "valgrind: " + e.Reason + ": " + e.Err.Error()
	}
	return "valgrind: " + e.Reason
}

// Unwrap exposes the underlying error, if any.
func (e *BackendError) Unwrap() error { return e.Err }

// Is reports the backend-unavailable sentinel.
func (e *BackendError) Is(target error) bool { return target == ErrBackendUnavailable }

// Request describes one Memcheck analysis. Program arguments are still subject
// to the target's configured allowlist.
//
// A nil Options selects DefaultOptions. A non-nil value is used as given, with
// Validate filling in only the documented zero-value defaults, so an explicit
// TrackOrigins=false is honoured.
type Request struct {
	TargetID string
	Args     []string
	Options  *Options
}

// ServiceOptions wires the analysis service to the J2 execution foundation.
type ServiceOptions struct {
	Binary  string
	Version string

	Store    *artifacts.Store
	Manager  *jobs.Manager
	Resolver *policy.Resolver

	// Roots are the resolved workspace roots used for path normalization. When
	// empty, the resolver's current roots are used.
	Roots []string
	// Suppressions maps profile ids to configured suppression files.
	Suppressions map[string]string

	MaxRawXMLBytes int64
	ParseOptions   ParseOptions
	WatchInterval  time.Duration
	MaxRetained    int
}

// DefaultRawXMLBytes bounds the raw Valgrind XML captured per run.
const DefaultRawXMLBytes int64 = DefaultMaxXMLBytes

// DefaultWatchInterval is how often the raw XML size is checked while running.
const DefaultWatchInterval = 100 * time.Millisecond

// analysisReportReserve keeps room for analysis.json after the raw XML is
// adopted, so a large raw document cannot make the normalized report
// unpersistable.
const analysisReportReserve int64 = 128 << 10

// Service owns the Memcheck pipeline. It does not expose MCP tools; J4 wraps
// this contract with analysis_start/status/report.
type Service struct {
	store    *artifacts.Store
	manager  *jobs.Manager
	resolver *policy.Resolver

	binary  string
	version string
	roots   []string

	suppressions   map[string]string
	maxRawXMLBytes int64
	parseOptions   ParseOptions
	watchInterval  time.Duration
	maxRetained    int

	mu       sync.Mutex
	closed   bool
	analyses map[string]*Analysis
	finished []string
	wg       sync.WaitGroup
}

// NewService validates the wiring and returns a Memcheck service.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("valgrind: nil artifact store")
	}
	if opts.Manager == nil {
		return nil, errors.New("valgrind: nil job manager")
	}
	if opts.Resolver == nil {
		return nil, errors.New("valgrind: nil policy resolver")
	}
	if !filepath.IsAbs(opts.Binary) || opts.Binary == "" {
		return nil, &BackendError{Reason: "binary must be an absolute path"}
	}

	roots := append([]string(nil), opts.Roots...)
	if len(roots) == 0 {
		roots = opts.Resolver.ResolvedRoots()
	}
	maxRaw := opts.MaxRawXMLBytes
	if maxRaw <= 0 {
		maxRaw = DefaultRawXMLBytes
	}
	if limit := opts.Store.MaxBytesPerRun(); limit > 0 && maxRaw > limit {
		maxRaw = limit
	}
	parseOptions := opts.ParseOptions
	if parseOptions.MaxBytes <= 0 || parseOptions.MaxBytes > maxRaw {
		parseOptions.MaxBytes = maxRaw
	}
	watchInterval := opts.WatchInterval
	if watchInterval <= 0 {
		watchInterval = DefaultWatchInterval
	}
	maxRetained := opts.MaxRetained
	if maxRetained <= 0 {
		maxRetained = 256
	}

	suppressions := make(map[string]string, len(opts.Suppressions))
	for id, path := range opts.Suppressions {
		suppressions[id] = path
	}
	return &Service{
		store:          opts.Store,
		manager:        opts.Manager,
		resolver:       opts.Resolver,
		binary:         opts.Binary,
		version:        opts.Version,
		roots:          roots,
		suppressions:   suppressions,
		maxRawXMLBytes: maxRaw,
		parseOptions:   parseOptions,
		watchInterval:  watchInterval,
		maxRetained:    maxRetained,
		analyses:       make(map[string]*Analysis),
	}, nil
}

// Phase is the analysis-lifecycle phase exposed for status reporting.
type Phase string

const (
	PhaseQueued    Phase = "queued"
	PhaseRunning   Phase = "running"
	PhaseAnalyzing Phase = "analyzing"
	PhaseCompleted Phase = "completed"
	PhaseFailed    Phase = "failed"
	PhaseCancelled Phase = "cancelled"
	PhaseTimedOut  Phase = "timed_out"
)

// AnalysisSnapshot is a consistent view of one analysis.
type AnalysisSnapshot struct {
	RunID  string             `json:"run_id"`
	Phase  Phase              `json:"phase"`
	Report *diagnostic.Report `json:"report,omitempty"`
	Error  string             `json:"error,omitempty"`
}

// Analysis is a handle to one asynchronous Memcheck run.
type Analysis struct {
	id   string
	job  *jobs.Job
	done chan struct{}

	mu     sync.Mutex
	phase  Phase
	report *diagnostic.Report
	err    error
}

// ID returns the opaque run id.
func (a *Analysis) ID() string { return a.id }

// Snapshot returns the current phase and, once available, the report.
func (a *Analysis) Snapshot() AnalysisSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AnalysisSnapshot{RunID: a.id, Phase: a.phase, Report: a.report, Error: errorString(a.err)}
}

// Wait blocks until the analysis and its post-processing finish. A report may
// be returned together with an error when post-processing failed.
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

// Cancel stops the instrumented process group. It is safe after completion.
func (a *Analysis) Cancel() {
	a.job.Cancel()
}

func (a *Analysis) setPhase(phase Phase) {
	a.mu.Lock()
	a.phase = phase
	a.mu.Unlock()
}

func (a *Analysis) finish(phase Phase, report *diagnostic.Report, err error) {
	a.mu.Lock()
	a.phase = phase
	a.report = report
	a.err = err
	a.mu.Unlock()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Start resolves the target, creates a private run directory and launches the
// Valgrind process asynchronously. It returns as soon as the process has been
// started, with a handle for status, cancellation and the normalized report.
// The context gates admission, resolution and target hashing; once launched the
// job is bound to the service lifetime and stops through Cancel, its timeout or
// Service.Close.
func (s *Service) Start(ctx context.Context, req Request) (*Analysis, error) {
	if ctx == nil {
		return nil, errors.New("valgrind: nil context")
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

	opts := DefaultOptions()
	if req.Options != nil {
		opts = *req.Options
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	binary, err := resolveExecutable(s.binary)
	if err != nil {
		return nil, err
	}
	suppressionPath, err := s.resolveSuppression(opts.SuppressionsProfile)
	if err != nil {
		return nil, err
	}

	resolved, err := s.resolver.Resolve(req.TargetID, req.Args)
	if err != nil {
		return nil, err
	}
	programHash, programInfo, err := hashFileContext(ctx, resolved.Program)
	if err != nil {
		return nil, fmt.Errorf("valgrind: hash target %q: %w", resolved.Program, err)
	}

	runDir, err := s.store.Create()
	if err != nil {
		return nil, err
	}
	xmlPath := filepath.Join(runDir.Path(), RawArtifactName)
	args, err := opts.Arguments(xmlPath, suppressionPath, resolved.Program, resolved.Args)
	if err != nil {
		_ = s.store.Remove(runDir.ID())
		return nil, err
	}

	startedAt := time.Now().UTC()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = s.store.Remove(runDir.ID())
		return nil, ErrClosed
	}
	job, err := s.manager.StartInDir(ctx, runDir, jobs.Spec{
		TargetID:   resolved.TargetID,
		Executable: binary,
		Args:       args,
		Dir:        resolved.Dir,
		Env:        policy.Environment(""),
	})
	if err != nil {
		s.mu.Unlock()
		_ = s.store.Remove(runDir.ID())
		return nil, err
	}

	analysis := &Analysis{
		id:    runDir.ID(),
		job:   job,
		done:  make(chan struct{}),
		phase: PhaseQueued,
	}
	s.analyses[analysis.id] = analysis
	s.wg.Add(1)
	s.mu.Unlock()

	go s.analyze(analysis, runDir, xmlPath, binary, resolved, programHash, programInfo, opts, startedAt)
	return analysis, nil
}

// Analyze is the synchronous convenience form of Start + Wait.
func (s *Service) Analyze(ctx context.Context, req Request) (*diagnostic.Report, error) {
	analysis, err := s.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	return analysis.Wait(ctx)
}

// Get returns an analysis by run id while it is within the retention window.
func (s *Service) Get(runID string) (*Analysis, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	analysis, ok := s.analyses[runID]
	return analysis, ok
}

// Close cancels active analyses and waits for post-processing to finish. The
// job manager and artifact store remain owned by the caller.
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

func (s *Service) analyze(
	analysis *Analysis,
	runDir *artifacts.RunDir,
	xmlPath string,
	binaryPath string,
	target policy.Resolved,
	programHash string,
	programInfo os.FileInfo,
	opts Options,
	startedAt time.Time,
) {
	defer s.wg.Done()
	defer close(analysis.done)

	analysis.setPhase(PhaseRunning)
	var rawExceeded atomic.Bool
	stopWatch := make(chan struct{})
	go s.watchRawXML(xmlPath, &rawExceeded, stopWatch, analysis.job)

	snapshot, _ := analysis.job.Wait(context.Background())
	close(stopWatch)
	analysis.setPhase(PhaseAnalyzing)

	rawLimit := s.maxRawXMLBytes
	if remaining := s.store.MaxBytesPerRun() - runDir.Written() - analysisReportReserve; remaining < rawLimit {
		rawLimit = remaining
	}
	if rawLimit <= 0 {
		// Adopt reads 0 as "no caller limit"; a negative limit keeps nothing.
		rawLimit = -1
	}
	adopted, adoptErr := runDir.Adopt(RawArtifactName, rawLimit)
	var xmlResult *XMLResult
	var parseErr error
	var rawDigest string
	if adoptErr == nil {
		file, err := runDir.OpenRead(RawArtifactName)
		if err != nil {
			parseErr = err
		} else {
			// Hash exactly the bytes the parser consumed, then the remainder.
			hasher := sha256.New()
			xmlResult, parseErr = Parse(io.TeeReader(file, hasher), s.parseOptions)
			if _, err := io.Copy(hasher, file); err == nil {
				rawDigest = "sha256:" + hex.EncodeToString(hasher.Sum(nil))
			}
			_ = file.Close()
		}
	}

	artifact := diagnostic.Artifact{
		Name:      RawArtifactName,
		Kind:      "raw_xml",
		Bytes:     adopted.Bytes,
		Truncated: adopted.Truncated || rawExceeded.Load(),
	}
	artifact.SHA256 = rawDigest

	program := diagnostic.Program{
		TargetID: target.TargetID,
		Path:     target.Program,
		SHA256:   programHash,
		Args:     append([]string{}, target.Args...),
	}
	if snapshot.Result != nil {
		program.ExitCode = snapshot.Result.ExitCode
		program.Signal = snapshot.Result.Signal
		program.TimedOut = snapshot.Result.TimedOut
		program.Canceled = snapshot.Result.Canceled
		program.DurationMS = snapshot.Result.DurationMS
	}

	buildInput := BuildInput{
		RunID:       analysis.id,
		ToolPath:    binaryPath,
		ToolVersion: s.version,
		Roots:       s.roots,
		Options:     opts,
		XML:         xmlResult,
		ParseErr:    parseErr,
		Program:     program,
	}
	if adoptErr == nil {
		buildInput.Artifacts = []diagnostic.Artifact{artifact}
	}
	report := BuildReport(buildInput)
	report.Program = program
	report.Metadata.StartTime = formatTime(snapshot.StartedAt)
	if report.Metadata.StartTime == "" && !startedAt.IsZero() {
		report.Metadata.StartTime = startedAt.UTC().Format(time.RFC3339Nano)
	}
	report.Metadata.EndTime = formatTime(snapshot.FinishedAt)
	report.Metadata.DurationMS = program.DurationMS

	result, complete, warnings := classifyAnalysis(snapshot, xmlResult, parseErr, adoptErr, artifact.Truncated, report.AnalysisComplete)
	report.Result = result
	report.AnalysisComplete = report.AnalysisComplete && complete
	report.Warnings = append(report.Warnings, warnings...)
	if targetChanged(programInfo, target.Program) {
		report.AnalysisComplete = false
		report.Warnings = append(report.Warnings,
			"target binary changed after it was hashed; program.sha256 may not match the executed code")
	}
	if adoptErr != nil {
		report.Warnings = append(report.Warnings, "raw XML unavailable: "+adoptErr.Error())
	}
	if snapshot.Result != nil && snapshot.Result.Signal != 0 {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("instrumented process terminated by signal %d; XML remains the analysis evidence",
				snapshot.Result.Signal))
	}
	if snapshot.Error != "" {
		report.Warnings = append(report.Warnings, "job error: "+snapshot.Error)
	}

	if err := report.Validate(); err != nil {
		analysis.finish(PhaseFailed, &report, err)
		s.publish(analysis)
		return
	}
	if err := writeReport(runDir, &report); err != nil {
		analysis.finish(PhaseFailed, &report, err)
		s.publish(analysis)
		return
	}

	phase := phaseFromSnapshot(snapshot)
	if result == diagnostic.ResultToolError || result == diagnostic.ResultPolicyDenied {
		phase = PhaseFailed
	}
	analysis.finish(phase, &report, nil)
	s.publish(analysis)
}

func (s *Service) watchRawXML(path string, exceeded *atomic.Bool, stop <-chan struct{}, job *jobs.Job) {
	ticker := time.NewTicker(s.watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > s.maxRawXMLBytes {
				exceeded.Store(true)
				job.Cancel()
				return
			}
		}
	}
}

func (s *Service) resolveSuppression(profile string) (string, error) {
	if profile == "" {
		return "", nil
	}
	configured, ok := s.suppressions[profile]
	if !ok {
		return "", &OptionError{Field: "suppressions_profile", Reason: fmt.Sprintf("unknown profile %q", profile)}
	}
	resolved, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q is not accessible", profile), Err: err}
	}
	if !withinAnyRoot(s.roots, resolved) {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q resolves outside workspace roots", profile)}
	}
	file, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q cannot be opened safely", profile), Err: err}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q cannot be inspected", profile), Err: err}
	}
	if !info.Mode().IsRegular() {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q is not a regular file", profile)}
	}
	if info.Size() > config.MaxSuppressionFileBytes {
		return "", &BackendError{Reason: fmt.Sprintf("suppression profile %q exceeds %d bytes", profile, config.MaxSuppressionFileBytes)}
	}
	return resolved, nil
}

func (s *Service) publish(analysis *Analysis) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, analysis.id)
	for len(s.finished) > s.maxRetained {
		delete(s.analyses, s.finished[0])
		s.finished = s.finished[1:]
	}
}

func phaseFromSnapshot(snapshot jobs.Snapshot) Phase {
	switch snapshot.State {
	case jobs.StateCancelled:
		return PhaseCancelled
	case jobs.StateTimedOut:
		return PhaseTimedOut
	case jobs.StateFailed:
		return PhaseFailed
	default:
		return PhaseCompleted
	}
}

func classifyAnalysis(snapshot jobs.Snapshot, xml *XMLResult, parseErr, adoptErr error, rawTruncated, buildComplete bool) (diagnostic.Result, bool, []string) {
	var warnings []string
	if snapshot.Result == nil {
		return diagnostic.ResultToolError, false, []string{"program result is missing"}
	}
	if !snapshot.Result.Started {
		return diagnostic.ResultToolError, false, []string{"Valgrind process did not start"}
	}
	switch snapshot.State {
	case jobs.StateCancelled:
		warnings = []string{"analysis was cancelled"}
		if rawTruncated {
			warnings = append(warnings, "raw XML exceeded the collection limit")
		}
		return diagnostic.ResultIncomplete, false, warnings
	case jobs.StateTimedOut:
		return diagnostic.ResultIncomplete, false, []string{"analysis timed out"}
	case jobs.StateFailed:
		return diagnostic.ResultToolError, false, []string{"job failed: " + snapshot.Error}
	}

	if xml == nil {
		if adoptErr != nil {
			warnings = append(warnings, "no usable XML evidence: "+adoptErr.Error())
		}
		return diagnostic.ResultToolError, false, warnings
	}
	if xml.ProtocolTool != "" && xml.ProtocolTool != ProtocolTool {
		return diagnostic.ResultToolError, false, []string{
			"Valgrind XML belongs to tool " + xml.ProtocolTool + ", not memcheck",
		}
	}
	if xml.Tool != "" && xml.Tool != ProtocolTool {
		return diagnostic.ResultToolError, false, []string{
			"Valgrind XML tool element is " + xml.Tool + ", not memcheck",
		}
	}
	if parseErr != nil {
		return diagnostic.ResultIncomplete, false, []string{"XML parsing incomplete: " + parseErr.Error()}
	}
	if rawTruncated {
		return diagnostic.ResultIncomplete, false, []string{"raw XML was truncated by the collection quota"}
	}
	if snapshot.Result.StdoutTruncated || snapshot.Result.StderrTruncated || snapshot.Result.OutputIncomplete {
		return diagnostic.ResultIncomplete, false, []string{"captured output was truncated"}
	}
	if xml.ErrorSummary == nil {
		return diagnostic.ResultIncomplete, false, []string{"Valgrind error_summary is missing"}
	}
	if !buildComplete {
		return diagnostic.ResultIncomplete, false, warnings
	}
	if xml.ErrorSummary.Errors > 0 {
		return diagnostic.ResultErrorsFound, true, warnings
	}
	// A complete XML with zero errors is a valid observation, including a target
	// that happens to exit with the --error-exitcode marker value.
	return diagnostic.ResultNoErrorsObserved, true, warnings
}

func writeReport(runDir *artifacts.RunDir, report *diagnostic.Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("valgrind: marshal analysis report: %w", err)
	}
	if err := runDir.WriteFileAtomic(AnalysisArtifactName, data); err != nil {
		if !errors.Is(err, artifacts.ErrQuota) && !errors.Is(err, artifacts.ErrFileQuota) {
			return fmt.Errorf("valgrind: persist analysis report: %w", err)
		}
		compact := *report
		compact.Diagnostics = []diagnostic.Diagnostic{}
		compact.Result = diagnostic.ResultIncomplete
		compact.AnalysisComplete = false
		compact.Summary.Truncated = true
		compact.Warnings = append(append([]string(nil), compact.Warnings...),
			"analysis details omitted from analysis.json because the artifact quota was reached")
		data, marshalErr := json.MarshalIndent(compact, "", "  ")
		if marshalErr != nil {
			return fmt.Errorf("valgrind: marshal compact analysis report: %w", marshalErr)
		}
		if writeErr := runDir.WriteFileAtomic(AnalysisArtifactName, data); writeErr != nil {
			return fmt.Errorf("valgrind: persist compact analysis report: %w", writeErr)
		}
		*report = compact
	}
	return nil
}

func formatTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func hashFile(path string) (string, error) {
	digest, _, err := hashFileContext(context.Background(), path)
	return digest, err
}

func hashFileContext(ctx context.Context, path string) (string, os.FileInfo, error) {
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

// targetChanged reports whether the file at path no longer matches the
// identity recorded when it was hashed. The check narrows, but cannot close,
// the window between hashing and exec, so a mismatch downgrades the report.
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

func hashReader(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func resolveExecutable(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", &BackendError{Reason: "Valgrind binary must be an absolute path"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", &BackendError{Reason: "Valgrind binary is not accessible", Err: err}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", &BackendError{Reason: "Valgrind binary is not accessible", Err: err}
	}
	if !info.Mode().IsRegular() {
		return "", &BackendError{Reason: "Valgrind binary is not a regular file"}
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", &BackendError{Reason: "Valgrind binary is not executable"}
	}
	return resolved, nil
}

func withinAnyRoot(roots []string, path string) bool {
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
