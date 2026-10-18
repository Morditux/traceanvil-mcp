package debugger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// BackendOptions wires one debugger backend to its executable and capability
// state. BinaryProvider and Available are called at every start, so a
// capability removed after discovery is not silently used.
type BackendOptions struct {
	Enabled        bool
	BinaryProvider func() (path, version string, err error)
	Available      func() (bool, string)
}

// ServiceOptions wires the debugger API to policy, configuration and the
// enabled backends.
type ServiceOptions struct {
	Resolver *policy.Resolver
	Roots    []string
	Backends map[string]BackendOptions
	Logger   *slog.Logger

	MaxSessions      int
	IdleTimeout      time.Duration
	RetainTimeout    time.Duration
	ReapInterval     time.Duration
	MaxResponseItems int
	RequestTimeout   time.Duration
	StartupTimeout   time.Duration
	StopTimeout      time.Duration

	// Starter is injectable for tests. Production uses startSession.
	Starter func(context.Context, sessionOptions) (Session, error)
}

// Service is the debugger business API. MCP handlers only delegate here.
type Service struct {
	resolver       *policy.Resolver
	roots          []string
	backends       map[string]BackendOptions
	logger         *slog.Logger
	manager        *Manager
	maxItems       int
	requestTimeout time.Duration
	startupTimeout time.Duration
	stopTimeout    time.Duration
	starter        func(context.Context, sessionOptions) (Session, error)
}

const (
	defaultMaxResponseItems = 100
	maxBreakpoints          = 100
	maxSourceLine           = 1_000_000
	maxStackLevels          = 100
	maxVariablesCount       = 200
)

// NewService builds the debugger service and its session manager.
func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Resolver == nil {
		return nil, errors.New("debugger: nil policy resolver")
	}
	if len(opts.Backends) == 0 {
		return nil, errors.New("debugger: no backends configured")
	}
	for name, backend := range opts.Backends {
		if _, err := backendSpecFor(name); err != nil {
			return nil, err
		}
		if backend.Enabled && backend.BinaryProvider == nil {
			return nil, fmt.Errorf("debugger: backend %q is enabled without a binary provider", name)
		}
	}
	if opts.MaxResponseItems <= 0 {
		opts.MaxResponseItems = defaultMaxResponseItems
	}
	if opts.MaxResponseItems > 10_000 {
		opts.MaxResponseItems = 10_000
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	manager := NewManager(ManagerOptions{
		MaxSessions:   opts.MaxSessions,
		IdleTimeout:   opts.IdleTimeout,
		RetainTimeout: opts.RetainTimeout,
		ReapInterval:  opts.ReapInterval,
		Logger:        logger,
	})

	starter := opts.Starter
	if starter == nil {
		starter = startSession
	}
	backends := make(map[string]BackendOptions, len(opts.Backends))
	for name, backend := range opts.Backends {
		backends[name] = backend
	}
	return &Service{
		resolver:       opts.Resolver,
		roots:          append([]string(nil), opts.Roots...),
		backends:       backends,
		logger:         logger,
		manager:        manager,
		maxItems:       opts.MaxResponseItems,
		requestTimeout: opts.RequestTimeout,
		startupTimeout: opts.StartupTimeout,
		stopTimeout:    opts.StopTimeout,
		starter:        starter,
	}, nil
}

// Close releases every debugger session and stops the retention reaper.
func (s *Service) Close() {
	if s == nil || s.manager == nil {
		return
	}
	s.manager.Close()
}

// Start validates one declared target, revalidates the selected backend and
// launches an asynchronous debug session.
func (s *Service) Start(ctx context.Context, req StartRequest) (Status, error) {
	if s == nil || s.manager == nil {
		return Status{}, Errorf(CodeClosed, "debugger service is not configured")
	}
	if ctx == nil {
		return Status{}, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Status{}, Wrap(CodeTimeout, err, "debug session start timed out")
		}
		return Status{}, Wrap(CodeCanceled, err, "debug session start was canceled")
	}
	if req.TargetID == "" {
		return Status{}, Errorf(CodeInvalidArgument, "target_id must not be empty")
	}
	backend, backendOptions, err := s.selectBackend(req.Backend)
	if err != nil {
		return Status{}, err
	}

	resolved, err := s.resolver.Resolve(req.TargetID, req.Args)
	if err != nil {
		var denied *policy.DeniedError
		if errors.As(err, &denied) {
			if denied.Reason == policy.ReasonUnknownTarget {
				return Status{}, Wrap(CodeTargetUnknown, err, "target %q is not declared in configuration", denied.TargetID)
			}
			return Status{}, Wrap(CodePolicyDenied, err, "target %q was denied by policy: %s", denied.TargetID, denied.Reason)
		}
		return Status{}, Wrap(CodePolicyDenied, err, "target %q was denied by policy", req.TargetID)
	}

	binary, version, err := backendOptions.BinaryProvider()
	if err != nil {
		return Status{}, Wrap(CodeBackendUnavailable, err, "%s backend is unavailable", backend)
	}
	if err := validateDebuggerBinary(binary); err != nil {
		return Status{}, Wrap(CodeBackendUnavailable, err, "%s backend is not usable", backend)
	}

	stopOnEntry := true
	if req.StopOnEntry != nil {
		stopOnEntry = *req.StopOnEntry
	}

	session, err := s.manager.Start(ctx, func(startCtx context.Context, id string) (Session, error) {
		return s.starter(startCtx, sessionOptions{
			ID:             id,
			TargetID:       resolved.TargetID,
			Backend:        backend,
			Program:        resolved.Program,
			Dir:            resolved.Dir,
			Binary:         binary,
			Version:        version,
			Args:           resolved.Args,
			StopOnEntry:    stopOnEntry,
			Roots:          append([]string(nil), s.roots...),
			Logger:         s.logger,
			RequestTimeout: s.requestTimeout,
			StartupTimeout: s.startupTimeout,
			StopTimeout:    s.stopTimeout,
		})
	})
	if err != nil {
		var named *Error
		if errors.As(err, &named) {
			return Status{}, named
		}
		var limit *LimitError
		switch {
		case errors.As(err, &limit):
			return Status{}, Wrap(CodeQuotaExceeded, err, "session limit reached (%d live sessions)", limit.Max)
		case errors.Is(err, ErrClosed):
			return Status{}, Wrap(CodeClosed, err, "debugger service is closed")
		case errors.Is(err, context.Canceled):
			return Status{}, Wrap(CodeCanceled, err, "debug session start was canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return Status{}, Wrap(CodeTimeout, err, "debug session start timed out")
		default:
			return Status{}, err
		}
	}
	return session.Status(), nil
}

// Status reports a session snapshot. It never contacts the adapter and remains
// available after a debuggee exits.
func (s *Service) Status(ctx context.Context, req StatusRequest) (Status, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return Status{}, err
	}
	return session.Status(), nil
}

// SetBreakpoints resolves and validates a workspace source file, then applies
// line breakpoints to a stopped session.
func (s *Service) SetBreakpoints(ctx context.Context, req BreakpointsRequest) (BreakpointsResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return BreakpointsResult{}, err
	}
	lines, err := validateLines(req.Lines)
	if err != nil {
		return BreakpointsResult{}, err
	}
	source, err := s.resolver.ResolveSource(req.SourcePath, session.WorkingDir())
	if err != nil {
		var denied *policy.SourceError
		if errors.As(err, &denied) {
			// Keep the detailed reason in local logs, but do not expose an
			// existence/containment oracle to MCP callers.
			s.logger.Debug("debug source denied",
				"session_id", req.SessionID, "reason", denied.Reason)
		}
		return BreakpointsResult{}, Wrap(CodeSourceDenied, err,
			"source path is not available inside workspace roots")
	}
	req.SourcePath = source.Path
	req.SourceDisplay = source.Relative
	req.Lines = lines
	return session.SetBreakpoints(ctx, req)
}

// Continue resumes a stopped session. The request is acknowledged
// asynchronously.
func (s *Service) Continue(ctx context.Context, req ContinueRequest) (ActionResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return ActionResult{}, err
	}
	if req.ThreadID < 0 {
		return ActionResult{}, Errorf(CodeInvalidArgument, "thread_id must not be negative")
	}
	return session.Continue(ctx, req)
}

// Step executes one source-level step and acknowledges it asynchronously.
func (s *Service) Step(ctx context.Context, req StepRequest) (ActionResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return ActionResult{}, err
	}
	if req.ThreadID < 0 {
		return ActionResult{}, Errorf(CodeInvalidArgument, "thread_id must not be negative")
	}
	switch req.Kind {
	case "next", "in", "out":
	default:
		return ActionResult{}, Errorf(CodeInvalidArgument, "kind must be next, in or out")
	}
	return session.Step(ctx, req)
}

// Pause requests a stop of a running session.
func (s *Service) Pause(ctx context.Context, req PauseRequest) (ActionResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return ActionResult{}, err
	}
	if req.ThreadID < 0 {
		return ActionResult{}, Errorf(CodeInvalidArgument, "thread_id must not be negative")
	}
	return session.Pause(ctx, req)
}

// Threads returns one bounded page of adapter-reported threads.
func (s *Service) Threads(ctx context.Context, req ThreadsRequest) (ThreadsResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return ThreadsResult{}, err
	}
	if req.Start < 0 {
		return ThreadsResult{}, Errorf(CodeInvalidArgument, "start must not be negative")
	}
	req.Count = clampCount(req.Count, s.maxItems, 500)
	return session.Threads(ctx, req)
}

// StackTrace returns one bounded page of stack frames.
func (s *Service) StackTrace(ctx context.Context, req StackRequest) (StackResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return StackResult{}, err
	}
	if req.ThreadID < 0 {
		return StackResult{}, Errorf(CodeInvalidArgument, "thread_id must not be negative")
	}
	if req.StartFrame < 0 {
		return StackResult{}, Errorf(CodeInvalidArgument, "start_frame must not be negative")
	}
	req.Levels = clampCount(req.Levels, s.maxItems, maxStackLevels)
	return session.StackTrace(ctx, req)
}

// Scopes returns the scopes of a frame at the current stop generation.
func (s *Service) Scopes(ctx context.Context, req ScopesRequest) (ScopesResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return ScopesResult{}, err
	}
	if req.FrameID < 0 {
		return ScopesResult{}, Errorf(CodeInvalidArgument, "frame_id must not be negative")
	}
	return session.Scopes(ctx, req)
}

// Variables returns one bounded page of variable children.
func (s *Service) Variables(ctx context.Context, req VariablesRequest) (VariablesResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return VariablesResult{}, err
	}
	if req.VariablesReference <= 0 {
		return VariablesResult{}, Errorf(CodeInvalidArgument, "variables_reference must be positive")
	}
	if req.Start < 0 {
		return VariablesResult{}, Errorf(CodeInvalidArgument, "start must not be negative")
	}
	req.Count = clampCount(req.Count, s.maxItems, maxVariablesCount)
	return session.Variables(ctx, req)
}

// Stop terminates or detaches a session. It is available even if the backend
// capability disappeared, so an existing session can always be cleaned up.
func (s *Service) Stop(ctx context.Context, req StopRequest) (StopResult, error) {
	session, err := s.session(req.SessionID)
	if err != nil {
		return StopResult{}, err
	}
	return session.Stop(ctx, req)
}

func (s *Service) session(sessionID string) (Session, error) {
	if s == nil || s.manager == nil {
		return nil, Errorf(CodeClosed, "debugger service is not configured")
	}
	session, err := s.manager.Get(sessionID)
	if err != nil {
		if errors.Is(err, ErrClosed) {
			return nil, Wrap(CodeClosed, err, "debugger service is closed")
		}
		var named *Error
		if errors.As(err, &named) {
			return nil, named
		}
		return nil, Wrap(CodeSessionNotFound, err, "session %q does not exist", sessionID)
	}
	return session, nil
}

// selectBackend applies the gdb default when the request omits a backend. If
// gdb is configured but unusable, it falls back to lldb so a published
// debug_start remains usable in an LLDB-only configuration. An explicit
// backend is never silently replaced.
func (s *Service) selectBackend(requested string) (string, BackendOptions, error) {
	if requested != "" {
		options, err := s.checkBackend(requested)
		return requested, options, err
	}
	options, err := s.checkBackend(BackendGDB)
	if err == nil {
		return BackendGDB, options, nil
	}
	if lldbOptions, lldbErr := s.checkBackend(BackendLLDB); lldbErr == nil {
		return BackendLLDB, lldbOptions, nil
	}
	return BackendGDB, BackendOptions{}, err
}

func (s *Service) checkBackend(backend string) (BackendOptions, error) {
	options, ok := s.backends[backend]
	if !ok {
		return BackendOptions{}, Errorf(CodeUnsupported,
			"backend %q is not configured", backend)
	}
	if !options.Enabled {
		return BackendOptions{}, Errorf(CodeBackendDisabled,
			"the %s backend is disabled by configuration", backend)
	}
	if options.Available != nil {
		ok, reason := options.Available()
		if !ok {
			if reason == "" {
				reason = backend + " is unavailable"
			}
			return BackendOptions{}, Errorf(CodeBackendUnavailable, "%s", reason)
		}
	}
	if options.BinaryProvider == nil {
		return BackendOptions{}, Errorf(CodeBackendUnavailable,
			"the %s backend has no executable provider", backend)
	}
	return options, nil
}

func validateDebuggerBinary(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("debugger path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("debugger is not accessible: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("debugger is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("debugger is not executable")
	}
	return nil
}

func validateLines(lines []int) ([]int, error) {
	if len(lines) == 0 {
		return nil, Errorf(CodeInvalidArgument, "lines must not be empty")
	}
	if len(lines) > maxBreakpoints {
		return nil, Errorf(CodeInvalidArgument, "at most %d breakpoint lines are allowed", maxBreakpoints)
	}
	seen := make(map[int]struct{}, len(lines))
	result := make([]int, 0, len(lines))
	for _, line := range lines {
		if line < 1 || line > maxSourceLine {
			return nil, Errorf(CodeInvalidArgument, "line %d is outside 1..%d", line, maxSourceLine)
		}
		if _, duplicate := seen[line]; duplicate {
			continue
		}
		seen[line] = struct{}{}
		result = append(result, line)
	}
	return result, nil
}

func clampCount(value, maxItems, hardMax int) int {
	if maxItems <= 0 || maxItems > hardMax {
		maxItems = hardMax
	}
	if value <= 0 {
		return min(maxItems, hardMax)
	}
	if value > maxItems {
		return maxItems
	}
	return value
}

// HasSessions reports whether a retained session exists. It is used to keep
// status and stop published after a backend capability disappears.
func (s *Service) HasSessions() bool {
	if s == nil || s.manager == nil {
		return false
	}
	return s.manager.HasSessions()
}

// HasTargets reports whether at least one target currently resolves under
// policy. It performs filesystem checks only and never executes a target.
// Start still revalidates the target and arguments at invocation.
func (s *Service) HasTargets() bool {
	if s == nil || s.resolver == nil {
		return false
	}
	return s.resolver.HasUsableTarget()
}
