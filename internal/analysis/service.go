// Package analysis exposes the MCP-facing analysis API: start, status, cancel,
// paginated reports and bounded artifact excerpts. It owns request validation,
// named API errors, response bounds and the streaming report reader. It never
// builds a shell command and never reads a path chosen by the client.
package analysis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/sanitizer"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

const (
	// DefaultTimeout matches the J2 execution default.
	DefaultTimeout = 120 * time.Second
	// DefaultMaxResponseItems is the fallback items-per-response bound.
	DefaultMaxResponseItems = 100
	// DefaultPageItems is the page size used when a request omits limit.
	DefaultPageItems = 50
	// MaxPageItems is the hard items-per-page bound, below the configurable
	// max_response_items.
	MaxPageItems = 500
	// DefaultMaxPageBytes bounds the serialized diagnostics of one page.
	DefaultMaxPageBytes int64 = 1 << 20
	// DefaultMaxDiagnosticBytes bounds one diagnostic record before unmarshal.
	DefaultMaxDiagnosticBytes int64 = 4 << 20
	// DefaultMaxArtifactBytes is the default analysis_artifact excerpt size.
	DefaultMaxArtifactBytes = 64 << 10
	// MaxArtifactBytes is the hard analysis_artifact excerpt bound.
	MaxArtifactBytes = 1 << 20
	// DefaultMaxReportBytes bounds one analysis.json read.
	DefaultMaxReportBytes int64 = 64 << 20
	// maxConcurrentReportLoads bounds how many reports are decoded and cached
	// at the same time. Requests beyond the limit use the streaming reader
	// instead of waiting, so memory stays bounded under concurrent pagination.
	maxConcurrentReportLoads = 2
)

var runIDPattern = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

// Run is one live analysis. *valgrind.Analysis and *sanitizer.Analysis
// implement it.
type Run interface {
	ID() string
	Snapshot() valgrind.AnalysisSnapshot
	JobSnapshot() jobs.Snapshot
	Cancel()
}

// Backend starts and tracks analyses. NewValgrindBackend adapts the Valgrind
// service and NewSanitizerBackend adapts sanitizer runs; tests provide fakes.
type Backend interface {
	Start(context.Context, valgrind.Request) (Run, error)
	Get(runID string) (Run, bool)
}

// ValgrindBackend adapts *valgrind.Service to Backend.
type ValgrindBackend struct {
	Service *valgrind.Service
}

// NewValgrindBackend returns an adapter for a Valgrind service.
func NewValgrindBackend(service *valgrind.Service) *ValgrindBackend {
	return &ValgrindBackend{Service: service}
}

// Start implements Backend.
func (b *ValgrindBackend) Start(ctx context.Context, req valgrind.Request) (Run, error) {
	if b == nil || b.Service == nil {
		return nil, &valgrind.BackendError{Reason: "Memcheck service is not configured"}
	}
	run, err := b.Service.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	return run, nil
}

// Get implements Backend.
func (b *ValgrindBackend) Get(runID string) (Run, bool) {
	if b == nil || b.Service == nil {
		return nil, false
	}
	run, ok := b.Service.Get(runID)
	if !ok || run == nil {
		return nil, false
	}
	return run, true
}

// Options wires the analysis API to the execution foundation.
type Options struct {
	Store   *artifacts.Store
	Backend Backend
	// Backends optionally maps tool names to backends. When empty, Backend is
	// used for memcheck, helgrind and drd (legacy wiring).
	Backends map[string]Backend

	// BackendEnabled reflects configuration. When false, analysis starts are
	// rejected with tool_disabled and the MCP server does not publish them.
	BackendEnabled bool
	// BackendAvailable is consulted on every start so a capability refresh is
	// effective without restarting the server. A nil function means "enabled
	// and assumed available".
	BackendAvailable func() (available bool, reason string)
	// BackendAvailableFor optionally applies per-tool capability checks. When
	// nil, BackendAvailable is used for every tool.
	BackendAvailableFor func(tool string) (available bool, reason string)

	// Timeout is the configured default and maximum job timeout.
	Timeout time.Duration

	MaxResponseItems   int
	MaxPageItems       int
	MaxPageBytes       int64
	MaxDiagnosticBytes int64
	// MaxArtifactBytes is the hard per-call excerpt cap. Zero selects
	// MaxArtifactBytes (1 MiB). Requests that omit max_bytes still default to
	// DefaultMaxArtifactBytes (64 KiB).
	MaxArtifactBytes int
	MaxReportBytes   int64

	// MaxCacheBytes bounds decoded reports kept for fast pagination. A negative
	// value disables the cache; zero selects DefaultMaxCacheBytes.
	MaxCacheBytes int64
	// MaxCacheEntryBytes bounds one decoded report. Zero selects
	// DefaultMaxCacheEntryBytes.
	MaxCacheEntryBytes int64
}

// Service implements the analysis API. It is safe for concurrent use.
type Service struct {
	store    *artifacts.Store
	backends map[string]Backend

	backendEnabled      bool
	backendAvailable    func() (bool, string)
	backendAvailableFor func(string) (bool, string)

	timeout              time.Duration
	maxResponseItems     int
	maxPageItems         int
	maxPageBytes         int64
	maxDiagnosticSize    int64
	defaultArtifactBytes int
	maxArtifactBytes     int
	maxReportBytes       int64

	cache     *reportCache
	loadSlots chan struct{}
	diffSlots chan struct{}

	mu     sync.Mutex
	closed bool
}

// New validates the wiring and applies documented defaults.
func New(opts Options) (*Service, error) {
	if opts.Store == nil {
		return nil, errors.New("analysis: nil artifact store")
	}
	if opts.Backend == nil && len(opts.Backends) == 0 {
		return nil, errors.New("analysis: nil backend")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxResponseItems <= 0 {
		opts.MaxResponseItems = DefaultMaxResponseItems
	}
	if opts.MaxPageItems <= 0 || opts.MaxPageItems > MaxPageItems {
		opts.MaxPageItems = MaxPageItems
	}
	if opts.MaxPageItems > opts.MaxResponseItems {
		opts.MaxPageItems = opts.MaxResponseItems
	}
	if opts.MaxPageBytes <= 0 {
		opts.MaxPageBytes = DefaultMaxPageBytes
	}
	if opts.MaxDiagnosticBytes <= 0 || opts.MaxDiagnosticBytes < opts.MaxPageBytes {
		opts.MaxDiagnosticBytes = DefaultMaxDiagnosticBytes
	}
	if opts.MaxDiagnosticBytes < opts.MaxPageBytes {
		opts.MaxDiagnosticBytes = opts.MaxPageBytes
	}
	if opts.MaxArtifactBytes <= 0 || opts.MaxArtifactBytes > MaxArtifactBytes {
		opts.MaxArtifactBytes = MaxArtifactBytes
	}
	defaultArtifactBytes := DefaultMaxArtifactBytes
	if defaultArtifactBytes > opts.MaxArtifactBytes {
		defaultArtifactBytes = opts.MaxArtifactBytes
	}
	if opts.MaxReportBytes <= 0 {
		opts.MaxReportBytes = DefaultMaxReportBytes
	}
	if opts.MaxCacheBytes == 0 {
		opts.MaxCacheBytes = DefaultMaxCacheBytes
	}
	var cache *reportCache
	if opts.MaxCacheBytes > 0 {
		cache = newReportCache(opts.MaxCacheBytes, opts.MaxCacheEntryBytes)
	}

	backends := make(map[string]Backend, len(opts.Backends)+3)
	if len(opts.Backends) > 0 {
		for tool, backend := range opts.Backends {
			if !ValidTool(tool) {
				return nil, fmt.Errorf("analysis: unsupported backend tool %q", tool)
			}
			if backend == nil {
				return nil, fmt.Errorf("analysis: nil backend for tool %q", tool)
			}
			backends[tool] = backend
		}
	} else if opts.Backend != nil {
		for _, tool := range []string{ToolMemcheck, ToolHelgrind, ToolDRD} {
			backends[tool] = opts.Backend
		}
	}
	return &Service{
		store:                opts.Store,
		backends:             backends,
		backendEnabled:       opts.BackendEnabled,
		backendAvailable:     opts.BackendAvailable,
		backendAvailableFor:  opts.BackendAvailableFor,
		timeout:              opts.Timeout,
		maxResponseItems:     opts.MaxResponseItems,
		maxPageItems:         opts.MaxPageItems,
		maxPageBytes:         opts.MaxPageBytes,
		maxDiagnosticSize:    opts.MaxDiagnosticBytes,
		defaultArtifactBytes: defaultArtifactBytes,
		maxArtifactBytes:     opts.MaxArtifactBytes,
		maxReportBytes:       opts.MaxReportBytes,
		cache:                cache,
		loadSlots:            make(chan struct{}, maxConcurrentReportLoads),
		diffSlots:            make(chan struct{}, 1),
	}, nil
}

// Close rejects new analyses and drops cached decoded reports. Live runs are
// owned and closed by the backend.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.cache != nil {
		s.cache.clear()
	}
}

func (s *Service) checkOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Errorf(CodeClosed, "analysis service is closed")
	}
	return nil
}

// Start validates a request, checks backend availability at invocation time and
// launches one analysis asynchronously.
func (s *Service) Start(ctx context.Context, req StartRequest) (Status, error) {
	if ctx == nil {
		return Status{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, contextError(err)
	}
	if err := s.checkOpen(); err != nil {
		return Status{}, err
	}

	tool := req.Tool
	if tool == "" {
		tool = ToolMemcheck
	}
	if !ValidTool(tool) {
		return Status{}, Errorf(CodeUnknownTool,
			"tool %q is not supported; supported tools: memcheck, helgrind, drd, asan, ubsan, lsan, tsan", tool)
	}
	if strings.TrimSpace(req.TargetID) == "" {
		return Status{}, Errorf(CodeInvalidArgument, "target_id must not be empty")
	}
	if len(req.TargetID) > 64 || strings.ContainsRune(req.TargetID, 0) {
		return Status{}, Errorf(CodeInvalidArgument, "target_id is invalid")
	}
	timeout, err := s.resolveTimeout(req.TimeoutMS)
	if err != nil {
		return Status{}, err
	}
	var opts *valgrind.Options
	if tool == ToolMemcheck {
		opts, err = req.Options.valgrindOptions()
		if err != nil {
			return Status{}, err
		}
	} else if req.Options != nil {
		return Status{}, Errorf(CodeInvalidArgument, "options are only supported for memcheck")
	}
	if err := s.checkBackend(tool); err != nil {
		return Status{}, err
	}

	backend, ok := s.backends[tool]
	if !ok {
		return Status{}, Errorf(CodeBackendUnavailable, "no backend is configured for tool %q", tool)
	}
	run, err := backend.Start(ctx, valgrind.Request{
		TargetID: req.TargetID,
		Tool:     valgrind.Tool(tool),
		Args:     append([]string{}, req.Args...),
		Options:  opts,
		Timeout:  timeout,
	})
	if err != nil {
		return Status{}, mapStartError(err)
	}
	return s.statusFromLive(run), nil
}

func (s *Service) resolveTimeout(timeoutMS int) (time.Duration, error) {
	if timeoutMS < 0 {
		return 0, Errorf(CodeInvalidArgument, "timeout_ms must not be negative")
	}
	if timeoutMS == 0 {
		return 0, nil
	}
	maxMS := s.timeout.Milliseconds()
	if int64(timeoutMS) > maxMS {
		return 0, Errorf(CodeInvalidArgument,
			"timeout_ms %d exceeds the configured maximum of %d ms", timeoutMS, maxMS)
	}
	return time.Duration(timeoutMS) * time.Millisecond, nil
}

func (s *Service) checkBackend(tool string) error {
	if !s.backendEnabled {
		return Errorf(CodeToolDisabled, "the analysis backend is disabled by configuration")
	}
	var available bool
	var reason string
	if s.backendAvailableFor != nil {
		available, reason = s.backendAvailableFor(tool)
	} else if s.backendAvailable != nil {
		available, reason = s.backendAvailable()
	} else {
		return nil
	}
	if available {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "the " + tool + " backend is not available"
	}
	return Errorf(CodeBackendUnavailable, "%s", reason)
}

func mapStartError(err error) error {
	if err == nil {
		return nil
	}
	// BackendError carries the human-readable reason (missing binary, unsafe
	// suppression file, …); keep it while mapping to the stable code.
	var backendErr *valgrind.BackendError
	if errors.As(err, &backendErr) {
		return Wrap(CodeBackendUnavailable, err, "%s", backendErr.Reason)
	}
	var sanitizerErr *sanitizer.BackendError
	if errors.As(err, &sanitizerErr) {
		return Wrap(CodeBackendUnavailable, err, "%s", sanitizerErr.Reason)
	}
	switch {
	case errors.Is(err, sanitizer.ErrClosed):
		return Wrap(CodeClosed, err, "analysis service is closed")
	case errors.Is(err, valgrind.ErrClosed):
		return Wrap(CodeClosed, err, "analysis service is closed")
	case errors.Is(err, valgrind.ErrBackendUnavailable):
		return Wrap(CodeBackendUnavailable, err, "valgrind backend is unavailable")
	case errors.Is(err, artifacts.ErrQuota), errors.Is(err, artifacts.ErrFileQuota):
		return Wrap(CodeQuotaExceeded, err, "artifact quota exceeded")
	case errors.Is(err, context.Canceled):
		return Wrap(CodeCanceled, err, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(CodeTimeout, err, "request deadline exceeded")
	}
	var limitErr *jobs.LimitError
	if errors.As(err, &limitErr) {
		return Wrap(CodeQuotaExceeded, err, "concurrent analysis limit reached")
	}
	var denied *policy.DeniedError
	if errors.As(err, &denied) {
		if denied.Reason == policy.ReasonUnknownTarget {
			return Wrap(CodeTargetUnknown, err, "target %q is not declared in configuration", denied.TargetID)
		}
		return Wrap(CodePolicyDenied, err, "target %q was denied by policy: %s", denied.TargetID, denied.Reason)
	}
	var optionErr *valgrind.OptionError
	if errors.As(err, &optionErr) {
		return Wrap(CodeInvalidArgument, err, "options.%s: %s", optionErr.Field, optionErr.Reason)
	}
	return Wrap(CodeInternal, err, "analysis could not be started")
}

// Status returns the current state of one run. Runs that are no longer in the
// live backend are read from their persisted manifest and report.
func (s *Service) Status(ctx context.Context, req StatusRequest) (Status, error) {
	if ctx == nil {
		return Status{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, contextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return Status{}, err
	}
	if run, ok := s.getRun(req.RunID); ok {
		return s.statusFromLive(run), nil
	}
	if err := s.ensureAnalysisRun(req.RunID); err != nil {
		return Status{}, err
	}
	return s.statusFromDisk(ctx, req.RunID)
}

// Cancel requests cancellation. It returns an acknowledgement, not a final
// state: callers poll Status.
func (s *Service) Cancel(ctx context.Context, req CancelRequest) (CancelResult, error) {
	if ctx == nil {
		return CancelResult{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return CancelResult{}, contextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return CancelResult{}, err
	}
	if run, ok := s.getRun(req.RunID); ok {
		snap := run.Snapshot()
		job := run.JobSnapshot()
		phase := phaseFromValgrind(snap.Phase)
		if phase == PhaseUnknown {
			phase = phaseFromState(stateFromJobs(job.State))
		}
		if terminalState(job.State) {
			return CancelResult{
				SchemaVersion:   APISchemaVersion,
				RunID:           req.RunID,
				CancelRequested: false,
				State:           stateFromJobs(job.State),
				Phase:           phase,
				Message:         "run already finished; nothing was canceled",
			}, nil
		}
		run.Cancel()
		return CancelResult{
			SchemaVersion:   APISchemaVersion,
			RunID:           req.RunID,
			CancelRequested: true,
			State:           stateFromJobs(job.State),
			Phase:           phase,
			Message:         "cancellation requested; the process group receives SIGTERM then SIGKILL",
		}, nil
	}
	if err := s.ensureAnalysisRun(req.RunID); err != nil {
		return CancelResult{}, err
	}
	status, err := s.statusFromDisk(ctx, req.RunID)
	if err != nil {
		return CancelResult{}, err
	}
	return CancelResult{
		SchemaVersion:   APISchemaVersion,
		RunID:           req.RunID,
		CancelRequested: false,
		State:           status.State,
		Phase:           status.Phase,
		Message:         "run is not active in this server process; nothing was canceled",
	}, nil
}

// Report returns one bounded page of a stored analysis report.
func (s *Service) Report(ctx context.Context, req ReportRequest) (ReportPage, error) {
	if ctx == nil {
		return ReportPage{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return ReportPage{}, contextError(err)
	}
	if err := validateRunID(req.RunID); err != nil {
		return ReportPage{}, err
	}
	parsed, err := parseFilter(req)
	if err != nil {
		return ReportPage{}, err
	}
	offset, err := decodeCursor(req.Cursor, req.RunID, parsed.hash())
	if err != nil {
		return ReportPage{}, err
	}
	limit, err := s.pageLimit(req.Limit)
	if err != nil {
		return ReportPage{}, err
	}
	if err := s.ensureAnalysisRun(req.RunID); err != nil {
		return ReportPage{}, err
	}

	file, err := s.store.OpenArtifact(req.RunID, valgrind.AnalysisArtifactName)
	if err != nil {
		return ReportPage{}, s.artifactError(req.RunID, valgrind.AnalysisArtifactName, err, CodeReportUnavailable,
			"analysis report is unavailable for run %q", req.RunID)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ReportPage{}, Wrap(CodeReportUnavailable, err, "inspect analysis report")
	}
	loadForCache := false
	if s.cache != nil && info.Size() <= s.cache.maxEntry {
		if cached := s.cache.get(req.RunID, info.Size(), info.ModTime()); cached != nil {
			return s.reportPageFromCache(ctx, req, cached, parsed, offset, limit)
		}
		select {
		case s.loadSlots <- struct{}{}:
			loadForCache = true
		default:
			// Too many concurrent full-report loads: use the streaming reader
			// rather than allocating another decoded report.
		}
	}
	if loadForCache {
		defer func() { <-s.loadSlots }()
		read, err := readReport(ctx, file, info.Size(), reportReadOptions{
			allDiagnostics:     true,
			maxPageBytes:       s.maxPageBytes,
			maxDiagnosticBytes: s.maxDiagnosticSize,
			maxReportBytes:     s.maxReportBytes,
		})
		if err != nil {
			return ReportPage{}, err
		}
		if read.Header.RunID != req.RunID {
			return ReportPage{}, Errorf(CodeReportInvalid,
				"analysis report run_id %q does not match the requested run", read.Header.RunID)
		}
		cached := &cachedReport{
			header:      read.Header,
			diagnostics: read.Diagnostics,
			unreadable:  read.Stats.Unreadable,
			size:        info.Size(),
			// Report JSON bytes plus a conservative per-diagnostic allocation
			// estimate; the byte budget is about memory, not just file size.
			cost:    info.Size() + int64(len(read.Diagnostics))*384,
			modTime: info.ModTime(),
		}
		s.cache.put(req.RunID, cached)
		return s.reportPageFromCache(ctx, req, cached, parsed, offset, limit)
	}

	read, err := readReport(ctx, file, info.Size(), reportReadOptions{
		filter:             parsed,
		offset:             offset,
		limit:              limit,
		maxPageBytes:       s.maxPageBytes,
		maxDiagnosticBytes: s.maxDiagnosticSize,
		maxReportBytes:     s.maxReportBytes,
	})
	if err != nil {
		return ReportPage{}, err
	}
	if read.Header.RunID != req.RunID {
		return ReportPage{}, Errorf(CodeReportInvalid,
			"analysis report run_id %q does not match the requested run", read.Header.RunID)
	}
	return s.buildReportPage(req, read.Header, read.Diagnostics, read.Stats, offset, limit, parsed.hash())
}

// reportPageFromCache applies filters and the response budget to a decoded
// report. It performs no filesystem access.
func (s *Service) reportPageFromCache(ctx context.Context, req ReportRequest, cached *cachedReport, parsed filter, offset, limit int) (ReportPage, error) {
	accumulator := newPageAccumulator(parsed, offset, limit, s.maxPageBytes)
	for i, item := range cached.diagnostics {
		if i%diagnosticCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return ReportPage{}, contextError(err)
			}
		}
		if err := accumulator.offer(item); err != nil {
			return ReportPage{}, err
		}
	}
	base := reportStats{Unreadable: cached.unreadable}
	if cached.unreadable > 0 {
		base.ScanIncomplete = true
	}
	items, stats := accumulator.result(base)
	return s.buildReportPage(req, cached.header, items, stats, offset, limit, parsed.hash())
}

func (s *Service) buildReportPage(req ReportRequest, header reportHeader, diagnostics []diagnostic.Diagnostic, stats reportStats, offset, limit int, filterHash string) (ReportPage, error) {
	page := ReportPage{
		SchemaVersion:    APISchemaVersion,
		RunID:            req.RunID,
		AnalysisTool:     toolFromHeader(header),
		Tool:             header.Tool,
		Result:           header.Result,
		AnalysisComplete: header.AnalysisComplete,
		Program:          header.Program,
		Summary:          header.Summary,
		Artifacts:        header.Artifacts,
		Diagnostics:      diagnostics,
		Warnings:         append([]string{}, header.Warnings...),
		Page: PageInfo{
			Offset:                offset,
			Limit:                 limit,
			Returned:              len(diagnostics),
			Matched:               stats.Matched,
			Consumed:              stats.Consumed,
			ByteBudgetReached:     stats.ByteBudgetReached,
			OmittedOversized:      stats.OmittedOversized,
			UnreadableDiagnostics: stats.Unreadable,
			ScanIncomplete:        stats.ScanIncomplete,
		},
	}
	if page.Diagnostics == nil {
		page.Diagnostics = []diagnostic.Diagnostic{}
	}
	if page.Artifacts == nil {
		page.Artifacts = []diagnostic.Artifact{}
	}
	if page.Warnings == nil {
		page.Warnings = []string{}
	}

	consumed := offset + stats.Consumed
	if consumed < offset {
		// Defensive overflow guard; offsets are bounded by the report size.
		consumed = stats.Matched
	}
	page.Page.HasMore = stats.Matched > consumed
	if page.Page.HasMore {
		cursor, err := encodeCursor(req.RunID, consumed, filterHash)
		if err != nil {
			return ReportPage{}, err
		}
		page.Page.NextCursor = cursor
	}

	if header.WarningsTruncated {
		page.Warnings = append(page.Warnings, "report warnings were truncated to fit the response bound")
	}
	if header.ArtifactsTruncated {
		page.Warnings = append(page.Warnings, "report artifacts list was truncated to fit the response bound")
	}
	if stats.Unreadable > 0 {
		page.Warnings = append(page.Warnings, fmt.Sprintf(
			"%d diagnostic records exceed the %d-byte per-record read bound and were not examined; matching counts are incomplete",
			stats.Unreadable, s.maxDiagnosticSize))
	}
	if stats.OmittedOversized > 0 {
		page.Warnings = append(page.Warnings, fmt.Sprintf(
			"%d matching diagnostics exceed the %d-byte page budget and were omitted; use analysis_artifact to read the raw evidence",
			stats.OmittedOversized, s.maxPageBytes))
	}
	if stats.ScanIncomplete {
		page.Warnings = append(page.Warnings, "the report scan is incomplete; do not interpret this page as exhaustive")
	}
	return page, nil
}

// Artifact returns a bounded UTF-8 excerpt of one allowlisted artifact. It never
// exposes a filesystem path. When the excerpt stops before EOF, a trailing
// partial UTF-8 rune is moved back to the next rune boundary unless that would
// return zero bytes; the continuation cursor then starts on that boundary.
func (s *Service) Artifact(ctx context.Context, req ArtifactRequest) (ArtifactOutput, error) {
	if ctx == nil {
		return ArtifactOutput{}, Errorf(CodeInternal, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return ArtifactOutput{}, contextError(err)
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
	if _, err := artifactFileName(ToolMemcheck, req.ArtifactType); err != nil {
		return ArtifactOutput{}, err
	}
	if err := s.ensureAnalysisRun(req.RunID); err != nil {
		return ArtifactOutput{}, err
	}
	name, err := s.artifactName(ctx, req.RunID, req.ArtifactType)
	if err != nil {
		return ArtifactOutput{}, err
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = s.defaultArtifactBytes
	}
	if maxBytes > s.maxArtifactBytes {
		maxBytes = s.maxArtifactBytes
	}

	file, err := s.store.OpenArtifact(req.RunID, name)
	if err != nil {
		return ArtifactOutput{}, s.artifactError(req.RunID, name, err, CodeArtifactUnavailable,
			"artifact %q is unavailable for run %q", req.ArtifactType, req.RunID)
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
		return ArtifactOutput{}, contextError(err)
	}

	if req.Offset+int64(len(data)) < info.Size() {
		data = trimIncompleteRune(data)
	}
	content := string(data)
	invalid := !utf8.Valid(data)
	if invalid {
		content = strings.ToValidUTF8(content, "\uFFFD")
	}
	returned := len(data)
	next := req.Offset + int64(returned)
	return ArtifactOutput{
		SchemaVersion:       APISchemaVersion,
		RunID:               req.RunID,
		ArtifactType:        req.ArtifactType,
		FileName:            name,
		Bytes:               info.Size(),
		Offset:              req.Offset,
		ReturnedBytes:       returned,
		MaxBytes:            maxBytes,
		HasMore:             next < info.Size(),
		NextOffset:          next,
		Content:             content,
		InvalidUTF8Replaced: invalid,
	}, nil
}

func (s *Service) pageLimit(requested int) (int, error) {
	max := s.maxPageItems
	if s.maxResponseItems < max {
		max = s.maxResponseItems
	}
	if max < 1 {
		max = 1
	}
	switch {
	case requested < 0:
		return 0, Errorf(CodeInvalidArgument, "limit must not be negative")
	case requested == 0:
		if max < DefaultPageItems {
			return max, nil
		}
		return DefaultPageItems, nil
	case requested > max:
		return max, nil
	default:
		return requested, nil
	}
}

func (s *Service) artifactName(ctx context.Context, runID string, artifactType ArtifactType) (string, error) {
	if artifactType == ArtifactRawLog {
		if header, err := s.readHeader(ctx, runID); err == nil {
			for _, artifact := range header.Artifacts {
				if artifact.Kind == "raw_log" && artifact.Name != "" {
					return artifact.Name, nil
				}
			}
		}
		if manifest, err := readJobManifest(s.store, runID); err == nil && isSanitizerTool(manifest.Tool) {
			return "sanitizer.log", nil
		}
		return "stderr.log", nil
	}
	if artifactType != ArtifactRawXML {
		return artifactFileName(ToolMemcheck, artifactType)
	}
	tool := ToolMemcheck
	if header, err := s.readHeader(ctx, runID); err == nil {
		tool = toolFromHeader(header)
	} else if manifest, manifestErr := readJobManifest(s.store, runID); manifestErr == nil && manifest.Tool != "" {
		tool = manifest.Tool
	}
	return artifactFileName(tool, artifactType)
}

func (s *Service) ensureAnalysisRun(runID string) error {
	if !s.store.RunExists(runID) {
		return Errorf(CodeRunNotFound, "run %q does not exist", runID)
	}
	if manifest, err := readJobManifest(s.store, runID); err == nil &&
		manifest.Tool != "" && !ValidTool(manifest.Tool) {
		return Errorf(CodeRunNotFound, "run %q is not an analysis run", runID)
	}
	return nil
}

func isSanitizerTool(tool string) bool {
	switch tool {
	case ToolASan, ToolUBSan, ToolLSan, ToolTSan:
		return true
	default:
		return false
	}
}

func (s *Service) artifactError(runID, name string, err error, code ErrorCode, format string, args ...any) error {
	if errors.Is(err, os.ErrNotExist) {
		if !s.store.RunExists(runID) {
			return Wrap(CodeRunNotFound, err, "run %q does not exist", runID)
		}
		return Wrap(code, err, format, args...)
	}
	if !s.store.RunExists(runID) {
		return Wrap(CodeRunNotFound, err, "run %q does not exist", runID)
	}
	return Wrap(code, err, format, args...)
}

func validateRunID(runID string) error {
	if !runIDPattern.MatchString(runID) {
		return Errorf(CodeInvalidArgument, "run_id must have the form run_<32 lowercase hex characters>")
	}
	return nil
}

func (s *Service) getRun(runID string) (Run, bool) {
	for _, backend := range s.backends {
		if run, ok := backend.Get(runID); ok {
			return run, true
		}
	}
	return nil, false
}

func toolFromSnapshot(snapshot valgrind.AnalysisSnapshot) string {
	if snapshot.Tool != "" {
		return string(snapshot.Tool)
	}
	if snapshot.Report != nil {
		return toolFromReport(*snapshot.Report)
	}
	return ToolMemcheck
}

func toolFromReport(report diagnostic.Report) string {
	if value, ok := report.Options["tool"].(string); ok && ValidTool(value) {
		return value
	}
	switch report.Tool.Name {
	case ToolHelgrind, ToolDRD:
		return report.Tool.Name
	}
	return ToolMemcheck
}

func toolFromHeader(header reportHeader) string {
	if value, ok := header.Options["tool"].(string); ok && ValidTool(value) {
		return value
	}
	return ToolMemcheck
}

func (s *Service) statusFromLive(run Run) Status {
	snapshot := run.Snapshot()
	job := run.JobSnapshot()
	status := Status{
		SchemaVersion: APISchemaVersion,
		RunID:         run.ID(),
		Tool:          toolFromSnapshot(snapshot),
		TargetID:      job.TargetID,
		State:         stateFromJobs(job.State),
		Phase:         phaseFromValgrind(snapshot.Phase),
		Artifacts:     []diagnostic.Artifact{},
		Warnings:      []string{},
	}
	if status.Phase == PhaseUnknown {
		status.Phase = phaseFromState(status.State)
	}
	if snapshot.Report != nil {
		report := snapshot.Report
		status.Result = report.Result
		status.AnalysisComplete = report.AnalysisComplete
		program := report.Program
		status.Program = &program
		status.Artifacts = append(status.Artifacts, report.Artifacts...)
		status.Metrics = metricsFromReport(report)
		warnings, _ := normalizeWarnings(report.Warnings)
		status.Warnings = append(status.Warnings, warnings...)
	}
	if job.Result != nil {
		if status.Program == nil {
			program := programFromJob(job)
			status.Program = &program
		}
		mergeJobMetrics(&status.Metrics, job.Result)
	}
	status.StartedAt = formatTimePtr(job.StartedAt)
	status.FinishedAt = formatTimePtr(job.FinishedAt)
	status.Progress.ElapsedMS = elapsedMilliseconds(job.StartedAt, job.FinishedAt, time.Now())
	if size, ok := s.artifactSize(status.RunID, valgrind.AnalysisArtifactName); ok {
		status.ReportAvailable = true
		status.Metrics.ReportBytes = size
	}
	if !status.ReportAvailable && status.Phase == PhaseCompleted && snapshot.Error == "" {
		status.Warnings = append(status.Warnings,
			"normalized report is not available; use analysis_artifact to read the remaining evidence")
	}
	if snapshot.Phase == valgrind.PhaseFailed && snapshot.Error != "" {
		// Post-processing failed (validation or persistence). The in-memory
		// report is not published evidence: never let it read as a complete,
		// clean analysis.
		status.AnalysisComplete = false
		if status.Result == diagnostic.ResultNoErrorsObserved {
			status.Result = diagnostic.ResultIncomplete
		}
		status.Warnings = append(status.Warnings,
			"analysis post-processing failed; no complete normalized report is available")
	}
	if !terminalPhase(status.Phase) {
		status.Warnings = append(status.Warnings, "analysis is not complete; an empty result does not mean no errors")
	}
	status.Error = combineErrors(snapshot.Error, job.Error)
	return status
}

func (s *Service) statusFromDisk(ctx context.Context, runID string) (Status, error) {
	status := Status{
		SchemaVersion: APISchemaVersion,
		RunID:         runID,
		Tool:          ToolMemcheck,
		State:         StateUnknown,
		Phase:         PhaseUnknown,
		Artifacts:     []diagnostic.Artifact{},
		Warnings:      []string{},
	}

	manifest, manifestErr := readJobManifest(s.store, runID)
	if manifestErr == nil {
		status.TargetID = manifest.TargetID
		status.State = stateFromJobs(manifest.State)
		if manifest.Tool != "" {
			status.Tool = manifest.Tool
		}
		status.StartedAt = formatTimePtr(manifest.StartedAt)
		status.FinishedAt = formatTimePtr(manifest.FinishedAt)
		status.Progress.ElapsedMS = elapsedMilliseconds(manifest.StartedAt, manifest.FinishedAt, time.Now())
		status.Error = manifest.Error
		if manifest.Result != nil {
			program := programFromJob(manifest)
			status.Program = &program
			mergeJobMetrics(&status.Metrics, manifest.Result)
			if manifest.Result.DurationMS > 0 {
				status.Progress.ElapsedMS = manifest.Result.DurationMS
			}
		}
	} else if !errors.Is(manifestErr, os.ErrNotExist) {
		status.Warnings = append(status.Warnings, "job manifest is unreadable: "+manifestErr.Error())
		status.State = StateUnknown
	}

	header, headerErr := s.readHeader(ctx, runID)
	if headerErr == nil {
		status.Tool = toolFromHeader(header)
		status.Result = header.Result
		status.AnalysisComplete = header.AnalysisComplete
		program := header.Program
		status.Program = &program
		status.Metrics = metricsFromReportHeader(header)
		if manifest.Result != nil {
			mergeJobMetrics(&status.Metrics, manifest.Result)
		}
		status.Artifacts = append(status.Artifacts, header.Artifacts...)
		warnings, _ := normalizeWarnings(header.Warnings)
		status.Warnings = append(status.Warnings, warnings...)
		if header.WarningsTruncated {
			status.Warnings = append(status.Warnings, "report warnings were truncated to fit the response bound")
		}
		if header.ArtifactsTruncated {
			status.Warnings = append(status.Warnings, "report artifacts list was truncated to fit the response bound")
		}
		if size, ok := s.artifactSize(runID, valgrind.AnalysisArtifactName); ok {
			status.ReportAvailable = true
			status.Metrics.ReportBytes = size
		}
	} else if !errors.Is(headerErr, os.ErrNotExist) {
		status.Warnings = append(status.Warnings, "analysis report is unreadable: "+headerErr.Error())
	} else {
		status.Warnings = append(status.Warnings, "analysis report is not available")
	}
	if manifestErr != nil && headerErr == nil {
		status.Warnings = append(status.Warnings,
			"job manifest is unavailable; execution counters are unknown")
	}
	if status.Progress.ElapsedMS == 0 && status.Program != nil {
		status.Progress.ElapsedMS = status.Program.DurationMS
	}

	status.Phase = phaseForPersisted(manifest, manifestErr, header, headerErr)
	if status.State == StateQueued || status.State == StateRunning {
		status.Warnings = append(status.Warnings,
			"run is not tracked by this server process; the state comes from the persisted manifest and may be stale")
	}
	if status.Phase == PhaseUnknown && status.State == StateUnknown && manifestErr == nil {
		status.Phase = phaseFromState(status.State)
	}
	if !terminalPhase(status.Phase) && status.Phase != PhaseUnknown {
		status.Warnings = append(status.Warnings, "analysis is not complete; an empty result does not mean no errors")
	}
	if manifestErr != nil && headerErr != nil {
		status.Error = combineErrors(status.Error, "run has neither a readable manifest nor a readable report")
	}
	return status, nil
}

func (s *Service) readHeader(ctx context.Context, runID string) (reportHeader, error) {
	file, err := s.store.OpenArtifact(runID, valgrind.AnalysisArtifactName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return reportHeader{}, err
		}
		return reportHeader{}, Wrap(CodeReportUnavailable, err, "analysis report is unavailable")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return reportHeader{}, Wrap(CodeReportUnavailable, err, "analysis report is unreadable")
	}
	read, err := readReport(ctx, file, info.Size(), reportReadOptions{
		skipDiagnostics:    true,
		maxPageBytes:       s.maxPageBytes,
		maxDiagnosticBytes: s.maxDiagnosticSize,
		maxReportBytes:     s.maxReportBytes,
	})
	if err != nil {
		return reportHeader{}, err
	}
	if read.Header.RunID != runID {
		return reportHeader{}, Errorf(CodeReportInvalid,
			"analysis report run_id %q does not match the requested run", read.Header.RunID)
	}
	return read.Header, nil
}

func (s *Service) artifactSize(runID, name string) (int64, bool) {
	file, err := s.store.OpenArtifact(runID, name)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

func stateFromJobs(state jobs.State) State {
	switch state {
	case jobs.StateQueued:
		return StateQueued
	case jobs.StateRunning:
		return StateRunning
	case jobs.StateCompleted:
		return StateCompleted
	case jobs.StateCancelled:
		return StateCancelled
	case jobs.StateFailed:
		return StateFailed
	case jobs.StateTimedOut:
		return StateTimedOut
	default:
		return StateUnknown
	}
}

func phaseFromValgrind(phase valgrind.Phase) Phase {
	switch phase {
	case valgrind.PhaseQueued:
		return PhaseQueued
	case valgrind.PhaseRunning:
		return PhaseRunning
	case valgrind.PhaseAnalyzing:
		return PhaseAnalyzing
	case valgrind.PhaseCompleted:
		return PhaseCompleted
	case valgrind.PhaseFailed:
		return PhaseFailed
	case valgrind.PhaseCancelled:
		return PhaseCancelled
	case valgrind.PhaseTimedOut:
		return PhaseTimedOut
	default:
		return PhaseUnknown
	}
}

func phaseFromState(state State) Phase {
	switch state {
	case StateQueued:
		return PhaseQueued
	case StateRunning:
		return PhaseRunning
	case StateCompleted:
		return PhaseCompleted
	case StateCancelled:
		return PhaseCancelled
	case StateFailed:
		return PhaseFailed
	case StateTimedOut:
		return PhaseTimedOut
	default:
		return PhaseUnknown
	}
}

func phaseForPersisted(manifest jobs.Snapshot, manifestErr error, header reportHeader, headerErr error) Phase {
	if manifestErr == nil {
		phase := phaseFromState(stateFromJobs(manifest.State))
		if phase == PhaseCompleted && headerErr == nil &&
			(header.Result == diagnostic.ResultToolError || header.Result == diagnostic.ResultPolicyDenied) {
			phase = PhaseFailed
		}
		return phase
	}
	if headerErr == nil {
		if header.Result == diagnostic.ResultToolError || header.Result == diagnostic.ResultPolicyDenied {
			return PhaseFailed
		}
		return PhaseCompleted
	}
	return PhaseUnknown
}

func terminalPhase(phase Phase) bool {
	switch phase {
	case PhaseCompleted, PhaseFailed, PhaseCancelled, PhaseTimedOut:
		return true
	default:
		return false
	}
}

func terminalState(state jobs.State) bool {
	switch state {
	case jobs.StateCompleted, jobs.StateCancelled, jobs.StateFailed, jobs.StateTimedOut:
		return true
	default:
		return false
	}
}

func metricsFromReport(report *diagnostic.Report) Metrics {
	metrics := Metrics{
		Diagnostics: report.Summary.DiagnosticRecords,
		Occurrences: report.Summary.Total,
		Suppressed:  report.Summary.Suppressed,
	}
	metrics.RawXMLBytes = artifactBytesByKind(report.Artifacts, "raw_xml")
	metrics.RawLogBytes = artifactBytesByKind(report.Artifacts, "raw_log")
	return metrics
}

func metricsFromReportHeader(header reportHeader) Metrics {
	metrics := Metrics{
		Diagnostics: header.Summary.DiagnosticRecords,
		Occurrences: header.Summary.Total,
		Suppressed:  header.Summary.Suppressed,
	}
	metrics.RawXMLBytes = artifactBytesByKind(header.Artifacts, "raw_xml")
	metrics.RawLogBytes = artifactBytesByKind(header.Artifacts, "raw_log")
	return metrics
}

func artifactBytesByKind(artifacts []diagnostic.Artifact, kind string) int64 {
	var total int64
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			total += artifact.Bytes
		}
	}
	return total
}

func mergeJobMetrics(metrics *Metrics, result *jobs.Result) {
	if result == nil {
		return
	}
	metrics.StdoutBytes = result.StdoutBytes
	metrics.StderrBytes = result.StderrBytes
	metrics.StdoutTruncated = result.StdoutTruncated
	metrics.StderrTruncated = result.StderrTruncated
	metrics.OutputIncomplete = result.OutputIncomplete
}

func programFromJob(job jobs.Snapshot) diagnostic.Program {
	program := diagnostic.Program{
		TargetID: job.TargetID,
		Args:     []string{},
	}
	if job.Result != nil {
		program.ExitCode = job.Result.ExitCode
		program.Signal = job.Result.Signal
		program.TimedOut = job.Result.TimedOut
		program.Canceled = job.Result.Canceled
		program.DurationMS = job.Result.DurationMS
	}
	return program
}

func formatTimePtr(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func elapsedMilliseconds(startedAt, finishedAt *time.Time, now time.Time) int64 {
	if startedAt == nil || startedAt.IsZero() {
		return 0
	}
	end := now
	if finishedAt != nil && !finishedAt.IsZero() {
		end = *finishedAt
	}
	elapsed := end.Sub(*startedAt)
	if elapsed < 0 {
		return 0
	}
	return elapsed.Milliseconds()
}

func combineErrors(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parts = append(parts, limitUTF8(value, maxWarningBytes))
	}
	return strings.Join(parts, "; ")
}

// trimIncompleteRune drops a trailing partial UTF-8 sequence produced by a
// byte-bounded excerpt, so the next excerpt starts on a rune boundary instead
// of both pages containing replacement characters. Data without a complete
// rune is returned unchanged to guarantee forward progress.
func trimIncompleteRune(data []byte) []byte {
	for i := 1; i <= utf8.UTFMax-1 && i <= len(data); i++ {
		if !utf8.RuneStart(data[len(data)-i]) {
			continue
		}
		if !utf8.FullRune(data[len(data)-i:]) && len(data)-i > 0 {
			return data[:len(data)-i]
		}
		break
	}
	return data
}
