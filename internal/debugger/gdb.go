package debugger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/dap"
)

const (
	defaultRequestTimeout = 10 * time.Second
	defaultStartupTimeout = 15 * time.Second
	defaultStopTimeout    = 5 * time.Second
	stopEntryTimeout      = 5 * time.Second
	resumeStopWait        = 5 * time.Second
	maxHitBreakpoints     = 64
	maxEventName          = 64
)

type gdbSessionOptions struct {
	ID          string
	TargetID    string
	Program     string
	Dir         string
	Binary      string
	Version     string
	Args        []string
	StopOnEntry bool
	Roots       []string
	Logger      *slog.Logger

	RequestTimeout time.Duration
	StartupTimeout time.Duration
	StopTimeout    time.Duration
}

// gdbSession is one GDB DAP session. The event loop is the only consumer of the
// DAP event channel; status reads are independent and always available.
type gdbSession struct {
	id             string
	targetID       string
	backendVersion string
	dir            string
	stopOnEntry    bool
	roots          []string
	logger         *slog.Logger

	client *dap.Client
	proc   *gdbProcess
	home   string

	requestTimeout time.Duration
	stopTimeout    time.Duration

	opMu   sync.Mutex
	stopMu sync.Mutex

	mu             sync.Mutex
	state          State
	stopGen        int
	stop           *Stop
	exit           *Exit
	lastEvent      string
	lastEventAt    time.Time
	startedAt      time.Time
	updatedAt      time.Time
	errText        string
	caps           Capabilities
	defaultThread  int
	hitBPTruncated bool
	initialized    bool
	closing        bool
	changed        chan struct{}

	done          chan struct{}
	resourcesOnce sync.Once
	closeOnce     sync.Once
}

func startGDBSession(ctx context.Context, opts gdbSessionOptions) (Session, error) {
	if ctx == nil {
		return nil, Errorf(CodeInvalidArgument, "nil context")
	}
	if opts.ID == "" || opts.TargetID == "" {
		return nil, Errorf(CodeInternal, "session id and target id are required")
	}
	if !filepath.IsAbs(opts.Program) || !filepath.IsAbs(opts.Dir) || !filepath.IsAbs(opts.Binary) {
		return nil, Errorf(CodeInternal, "program, working directory and debugger must be absolute")
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = defaultStartupTimeout
	}
	if opts.StopTimeout <= 0 {
		opts.StopTimeout = defaultStopTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	home, err := os.MkdirTemp("", "mcp-gdb-session-")
	if err != nil {
		return nil, Wrap(CodeStartFailed, err, "create debugger session directory")
	}
	if err := os.Chmod(home, 0o700); err != nil {
		removeProcessTemp(home)
		return nil, Wrap(CodeStartFailed, err, "protect debugger session directory")
	}

	proc, err := startGDBProcess(opts.Binary, opts.Dir, home)
	if err != nil {
		removeProcessTemp(home)
		return nil, Wrap(CodeStartFailed, err, "start gdb")
	}

	client, err := dap.New(proc, dap.Options{
		ClientID:           "mcp-native-diagnostics",
		ClientName:         "MCP Native Diagnostics",
		WriteTimeout:       2 * time.Second,
		EventBuffer:        128,
		MaxPendingRequests: 16,
		ReverseRequestHandler: func(context.Context, dap.ReverseRequest) (any, error) {
			// A headless MCP server has no terminal to hand to the adapter.
			// Rejecting explicitly is safer than pretending to support
			// runInTerminal.
			return nil, errors.New("headless server does not support reverse terminal requests")
		},
	})
	if err != nil {
		_ = proc.Close()
		proc.Terminate()
		removeProcessTemp(home)
		return nil, Wrap(CodeStartFailed, err, "create DAP client")
	}

	session := &gdbSession{
		id:             opts.ID,
		targetID:       opts.TargetID,
		backendVersion: opts.Version,
		dir:            opts.Dir,
		stopOnEntry:    opts.StopOnEntry,
		roots:          append([]string(nil), opts.Roots...),
		logger:         opts.Logger,
		client:         client,
		proc:           proc,
		home:           home,
		requestTimeout: opts.RequestTimeout,
		stopTimeout:    opts.StopTimeout,
		state:          StateStarting,
		startedAt:      time.Now().UTC(),
		updatedAt:      time.Now().UTC(),
		changed:        make(chan struct{}),
		done:           make(chan struct{}),
	}
	go session.eventLoop()

	startupCtx, cancel := context.WithTimeout(ctx, opts.StartupTimeout)
	defer cancel()

	caps, err := client.Initialize(startupCtx, dap.InitializeRequestArguments{
		AdapterID:              "gdb",
		SupportsVariableType:   boolPtr(true),
		SupportsVariablePaging: boolPtr(false),
	})
	if err != nil {
		return nil, session.bootFailure("initialize GDB DAP", err)
	}
	if err := session.verifyHardening(); err != nil {
		return nil, session.bootFailure("verify GDB hardening", err)
	}
	session.mu.Lock()
	session.caps = Capabilities{
		ConfigurationDone: caps.SupportsConfigurationDoneRequest,
		TerminateRequest:  caps.SupportsTerminateRequest,
		TerminateDebuggee: caps.SupportsTerminateDebuggee,
		DelayedStackTrace: caps.SupportsDelayedStackTraceLoading,
	}
	session.mu.Unlock()

	if err := session.waitFor(startupCtx, func() bool {
		return session.initialized || session.state.Terminal()
	}); err != nil {
		return nil, session.bootFailure("wait for the DAP initialized event", err)
	}
	session.mu.Lock()
	initialized := session.initialized
	state := session.state
	session.mu.Unlock()
	if !initialized {
		return nil, session.bootFailure("wait for the DAP initialized event",
			fmt.Errorf("adapter entered state %q without an initialized event", state))
	}

	if err := client.BeginLaunch(); err != nil {
		return nil, session.bootFailure("begin launch", err)
	}

	launch := map[string]any{
		"program":     opts.Program,
		"args":        append([]string{}, opts.Args...),
		"cwd":         opts.Dir,
		"stopOnEntry": opts.StopOnEntry,
	}
	// GDB does not answer launch until configurationDone has arrived. Write the
	// launch frame first, then send configurationDone, then await the launch
	// response. RequestAsync returns once the launch frame is written.
	launchFuture, err := client.RequestAsync(startupCtx, "launch", launch)
	if err != nil {
		return nil, session.bootFailure("send launch", err)
	}
	// Observe a launch failure while configurationDone is being retried: a
	// rejected launch leaves GDB permanently in the "launch not specified"
	// state, and retrying would only hide the real adapter error.
	launchErrors := make(chan error, 1)
	go func() {
		if _, err := launchFuture.Wait(startupCtx); err != nil {
			launchErrors <- err
		}
	}()
	if caps.SupportsConfigurationDoneRequest {
		if err := session.sendConfigurationDone(startupCtx, launchErrors); err != nil {
			return nil, session.bootFailure("send configurationDone", err)
		}
	}
	if _, err := launchFuture.Wait(startupCtx); err != nil {
		return nil, session.bootFailure("launch debuggee", err)
	}

	if opts.StopOnEntry {
		stopCtx, stopCancel := context.WithTimeout(startupCtx, stopEntryTimeout)
		err := session.waitFor(stopCtx, func() bool {
			return session.state == StateStopped || session.state.Terminal()
		})
		stopCancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && startupCtx.Err() == nil {
				return nil, session.bootFailure("wait for stop-on-entry",
					fmt.Errorf("GDB did not report a stopped event within %s; the target may not be executable: %v", stopEntryTimeout, err))
			}
			return nil, session.bootFailure("wait for stop-on-entry", err)
		}
		session.mu.Lock()
		state := session.state
		session.mu.Unlock()
		if state != StateStopped {
			return nil, session.bootFailure("wait for stop-on-entry",
				fmt.Errorf("debuggee reached state %q instead of stopping", state))
		}
	} else {
		// Wait briefly for the first thread event so debug_pause can select a
		// thread without asking the client to guess one. A target that exits or
		// stops immediately also settles this wait; a missing thread event is
		// not fatal.
		settleCtx, settleCancel := context.WithTimeout(startupCtx, time.Second)
		_ = session.waitFor(settleCtx, func() bool {
			return session.defaultThread > 0 || session.state.Terminal() || session.state == StateStopped
		})
		settleCancel()
		if client.State() == dap.StateLaunching {
			_ = client.ConfirmRunning()
		}
		session.mu.Lock()
		if !session.state.Terminal() && session.state != StateStopped {
			session.state = StateRunning
			session.updatedAt = time.Now().UTC()
			session.notifyLocked()
		}
		session.mu.Unlock()
	}

	session.logger.Info("debug session started",
		"session_id", session.id, "target_id", session.targetID,
		"backend", BackendGDB, "stop_on_entry", opts.StopOnEntry)
	return session, nil
}

// sendConfigurationDone sends configurationDone after the launch frame. GDB
// dispatches DAP requests concurrently and may see configurationDone before it
// has installed the launch state; it then answers "launch or attach not
// specified". A successful response is the only signal that the launch state is
// installed, so retry that specific transient rejection with a short bounded
// backoff. The launch future is awaited by the caller afterwards.
func (s *gdbSession) sendConfigurationDone(ctx context.Context, launchErrors <-chan error) error {
	delay := 20 * time.Millisecond
	for {
		select {
		case err := <-launchErrors:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		_, err := s.client.Request(ctx, "configurationDone", struct{}{})
		if err == nil {
			return nil
		}
		s.logger.Debug("configurationDone retry", "error", err)
		var responseErr *dap.ResponseError
		if !errors.As(err, &responseErr) ||
			!strings.Contains(strings.ToLower(responseErr.Message), "launch or attach not specified") {
			return err
		}
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}

// verifyHardening fails closed when GDB rejected one of the security-critical
// -ex commands. Otherwise an older adapter could silently run without
// auto-load, shell or debuginfod hardening. The check runs before launch and
// before any target code is loaded.
func (s *gdbSession) verifyHardening() error {
	if s.proc == nil {
		return nil
	}
	return ValidateGDBHardening(s.proc.Stderr())
}

func (s *gdbSession) bootFailure(action string, cause error) error {
	if cause == nil {
		cause = errors.New("unknown startup failure")
	}
	s.mu.Lock()
	if !s.state.Terminal() {
		s.state = StateFailed
		s.errText = truncateText(action+": "+cause.Error(), 2048)
	}
	s.updatedAt = time.Now().UTC()
	s.closing = true
	s.notifyLocked()
	s.mu.Unlock()
	s.releaseResources(true)

	code := CodeStartFailed
	if errors.Is(cause, context.DeadlineExceeded) {
		code = CodeTimeout
	} else if errors.Is(cause, context.Canceled) {
		code = CodeCanceled
	}
	detail := cause.Error()
	if stderr := s.proc.Stderr(); stderr != "" {
		detail += "; gdb stderr: " + truncateText(strings.TrimSpace(stderr), 500)
	}
	return Wrap(code, cause, "%s: %s", action, detail)
}

// ID returns the opaque session id.
func (s *gdbSession) ID() string { return s.id }

// WorkingDir returns the validated target working directory.
func (s *gdbSession) WorkingDir() string { return s.dir }

// Done is closed when the session resources are released.
func (s *gdbSession) Done() <-chan struct{} { return s.done }

// Status returns a consistent snapshot. It never contacts the adapter, so it
// remains available after the debuggee exits or the transport fails.
func (s *gdbSession) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := Status{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		Backend:        BackendGDB,
		BackendVersion: s.backendVersion,
		TargetID:       s.targetID,
		State:          s.state,
		StopGeneration: s.stopGen,
		LastEvent:      s.lastEvent,
		StartedAt:      formatTime(s.startedAt),
		UpdatedAt:      formatTime(s.updatedAt),
		Capabilities:   s.caps,
		Error:          s.errText,
	}
	if s.stop != nil {
		stop := *s.stop
		stop.HitBreakpoints = append([]int(nil), s.stop.HitBreakpoints...)
		status.Stop = &stop
	}
	if s.exit != nil {
		exit := *s.exit
		status.Exit = &exit
	}
	if !s.lastEventAt.IsZero() {
		status.LastEventAt = formatTime(s.lastEventAt)
	}
	if s.state == StateStopped {
		status.Warnings = append(status.Warnings,
			fmt.Sprintf("frame, scope and variable references are valid only for stop_generation %d", s.stopGen))
	}
	if s.hitBPTruncated {
		status.Warnings = append(status.Warnings,
			fmt.Sprintf("hit breakpoint ids were truncated to %d entries", maxHitBreakpoints))
	}
	if s.stopOnEntry && s.stopGen == 1 && s.stop != nil &&
		(s.stop.Reason == "stopped" || s.stop.Reason == "entry") {
		status.Warnings = append(status.Warnings,
			"GDB stop_on_entry stops at the process entry point, which may be in the dynamic loader; set a breakpoint to reach source")
	}
	if s.state == StateFailed && status.Error == "" && s.client != nil {
		if err := s.client.Err(); err != nil {
			status.Error = truncateText(err.Error(), 2048)
		}
	}
	return status
}

func (s *gdbSession) eventLoop() {
	for event := range s.client.Events() {
		s.handleEvent(event)
	}
	s.onClientClosed()
}

func (s *gdbSession) handleEvent(event dap.Event) {
	s.mu.Lock()
	s.lastEvent = truncateText(event.Name, maxEventName)
	s.lastEventAt = event.ReceivedAt
	s.updatedAt = time.Now().UTC()
	terminal := false

	switch event.Name {
	case "initialized":
		s.initialized = true
	case "stopped":
		var body struct {
			Reason            string `json:"reason"`
			Description       string `json:"description"`
			ThreadID          int    `json:"threadId"`
			AllThreadsStopped bool   `json:"allThreadsStopped"`
			HitBreakpoints    []int  `json:"hitBreakpointIds"`
		}
		_ = event.DecodeBody(&body)
		if !s.state.Terminal() {
			hitBreakpoints := body.HitBreakpoints
			s.hitBPTruncated = len(hitBreakpoints) > maxHitBreakpoints
			if s.hitBPTruncated {
				hitBreakpoints = hitBreakpoints[:maxHitBreakpoints]
			}
			s.state = StateStopped
			s.stopGen++
			s.stop = &Stop{
				Reason:            truncateText(body.Reason, 64),
				ThreadID:          body.ThreadID,
				AllThreadsStopped: body.AllThreadsStopped,
				Description:       truncateText(body.Description, 512),
				HitBreakpoints:    append([]int(nil), hitBreakpoints...),
			}
		}
	case "continued":
		if !s.state.Terminal() {
			s.state = StateRunning
			s.stop = nil
			s.hitBPTruncated = false
		}
	case "exited":
		var body struct {
			ExitCode *int `json:"exitCode"`
		}
		_ = event.DecodeBody(&body)
		if body.ExitCode != nil {
			s.exit = &Exit{ExitCode: *body.ExitCode}
		}
		if !s.state.Terminal() {
			s.state = StateTerminated
		}
		terminal = true
	case "terminated":
		if !s.state.Terminal() {
			s.state = StateTerminated
		}
		terminal = true
	case "thread":
		var body struct {
			Reason   string `json:"reason"`
			ThreadID int    `json:"threadId"`
		}
		_ = event.DecodeBody(&body)
		if body.Reason == "started" && body.ThreadID > 0 && s.defaultThread == 0 {
			s.defaultThread = body.ThreadID
		}
	}
	s.notifyLocked()
	s.mu.Unlock()

	if terminal {
		s.scheduleRelease()
	}
}

func (s *gdbSession) onClientClosed() {
	s.mu.Lock()
	closing := s.closing
	terminal := s.state.Terminal()
	if !closing && !terminal {
		s.state = StateFailed
		s.errText = "debugger transport closed"
		if err := s.client.Err(); err != nil {
			s.errText = truncateText("debugger transport closed: "+err.Error(), 2048)
		}
		s.updatedAt = time.Now().UTC()
		s.notifyLocked()
	}
	s.mu.Unlock()
	s.releaseResources(true)
}

func (s *gdbSession) scheduleRelease() {
	go func() {
		select {
		case <-time.After(500 * time.Millisecond):
			s.releaseResources(true)
		case <-s.done:
		}
	}()
}

// releaseResources closes the DAP client and the GDB process exactly once. It
// never changes the reported session state; callers set terminated, detached or
// failed first.
func (s *gdbSession) releaseResources(terminate bool) {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()

	s.resourcesOnce.Do(func() {
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.proc != nil {
			if terminate {
				s.proc.Terminate()
			} else {
				s.proc.Detach()
			}
		}
		removeProcessTemp(s.home)

		s.mu.Lock()
		close(s.done)
		s.notifyLocked()
		s.mu.Unlock()
	})
}

// Close terminates the debuggee and releases the session. It is idempotent and
// used by manager shutdown and retention.
func (s *gdbSession) Close() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if !s.state.Terminal() {
			s.state = StateFailed
			s.errText = "debug session closed by the server"
		}
		s.updatedAt = time.Now().UTC()
		s.closing = true
		s.notifyLocked()
		s.mu.Unlock()
		s.releaseResources(true)
	})
}

func (s *gdbSession) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *gdbSession) waitFor(ctx context.Context, predicate func() bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		ok := predicate()
		changed := s.changed
		done := s.done
		s.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return ErrClosed
		}
	}
}

func (s *gdbSession) operationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	return opCtx, cancel, nil
}

func (s *gdbSession) SetBreakpoints(ctx context.Context, req BreakpointsRequest) (BreakpointsResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	state := s.state
	generation := s.stopGen
	s.mu.Unlock()
	if err := s.requireStopped(state, "set breakpoints"); err != nil {
		return BreakpointsResult{}, err
	}
	if !filepath.IsAbs(req.SourcePath) {
		return BreakpointsResult{}, Errorf(CodeInvalidArgument, "resolved source path must be absolute")
	}
	if len(req.Lines) == 0 {
		return BreakpointsResult{}, Errorf(CodeInvalidArgument, "lines must not be empty")
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return BreakpointsResult{}, mapDAPError(err, "set breakpoints")
	}
	defer cancel()

	breakpoints := make([]map[string]any, 0, len(req.Lines))
	for _, line := range req.Lines {
		breakpoints = append(breakpoints, map[string]any{"line": line})
	}
	response, err := s.client.Request(opCtx, "setBreakpoints", map[string]any{
		"source":      map[string]any{"path": req.SourcePath, "name": filepath.Base(req.SourcePath)},
		"breakpoints": breakpoints,
		"lines":       req.Lines,
	})
	if err != nil {
		return BreakpointsResult{}, mapDAPError(err, "set breakpoints")
	}

	var body struct {
		Breakpoints []struct {
			ID       int    `json:"id"`
			Verified bool   `json:"verified"`
			Line     int    `json:"line"`
			Message  string `json:"message"`
			Source   *struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"source"`
		} `json:"breakpoints"`
	}
	if err := response.DecodeBody(&body); err != nil {
		return BreakpointsResult{}, Wrap(CodeProtocol, err, "decode setBreakpoints response")
	}

	result := BreakpointsResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		Source:         req.SourceDisplay,
		StopGeneration: generation,
		Breakpoints:    make([]Breakpoint, 0, len(body.Breakpoints)),
	}
	reported := body.Breakpoints
	if len(reported) > maxBreakpoints {
		reported = reported[:maxBreakpoints]
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("adapter returned more than %d breakpoints; the list was truncated", maxBreakpoints))
	}
	unverified := 0
	for _, item := range reported {
		bp := Breakpoint{
			ID:       item.ID,
			Verified: item.Verified,
			Line:     item.Line,
			Message:  truncateText(item.Message, 512),
			Source:   req.SourceDisplay,
		}
		if item.Source != nil {
			if source := s.normalizeSource(item.Source.Name, item.Source.Path); source != nil {
				bp.Source = source.Path
				if bp.Source == "" {
					bp.Source = source.Name
				}
			}
		}
		if !bp.Verified {
			unverified++
		}
		result.Breakpoints = append(result.Breakpoints, bp)
	}
	if unverified > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d breakpoint(s) are not verified; the source or line may be unavailable", unverified))
	}
	if len(result.Breakpoints) < len(req.Lines) {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("adapter reported %d breakpoint(s) for %d requested line(s)", len(result.Breakpoints), len(req.Lines)))
	}
	return result, nil
}

func (s *gdbSession) Continue(ctx context.Context, req ContinueRequest) (ActionResult, error) {
	thread, err := s.threadFor(req.ThreadID)
	if err != nil {
		return ActionResult{}, err
	}
	return s.resume(ctx, "continue", "continue", map[string]any{"threadId": thread}, thread, false)
}

func (s *gdbSession) Step(ctx context.Context, req StepRequest) (ActionResult, error) {
	var command, action string
	switch req.Kind {
	case "next":
		command, action = "next", "step_next"
	case "in":
		command, action = "stepIn", "step_in"
	case "out":
		command, action = "stepOut", "step_out"
	default:
		return ActionResult{}, Errorf(CodeInvalidArgument, "kind must be next, in or out")
	}
	thread, err := s.threadFor(req.ThreadID)
	if err != nil {
		return ActionResult{}, err
	}
	return s.resume(ctx, action, command, map[string]any{"threadId": thread}, thread, true)
}

func (s *gdbSession) resume(ctx context.Context, action, command string, args map[string]any, thread int, waitForStop bool) (ActionResult, error) {
	s.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.opMu.Unlock()
		}
	}()

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return ActionResult{}, mapDAPError(err, action)
	}
	defer cancel()

	s.mu.Lock()
	state := s.state
	previousStop := s.stop
	generation := s.stopGen
	if err := s.requireStopped(state, action); err != nil {
		s.mu.Unlock()
		return ActionResult{}, err
	}
	s.state = StateRunning
	s.stop = nil
	s.updatedAt = time.Now().UTC()
	s.notifyLocked()
	s.mu.Unlock()

	if _, err := s.client.Request(opCtx, command, args); err != nil {
		// A DAP error response proves the resume did not happen. A timeout or
		// cancellation does not: the frame may already have reached GDB. Keep
		// the conservative running state in that case instead of restoring a
		// false stopped state and stale stop event.
		if definitiveResumeFailure(err) {
			s.mu.Lock()
			if s.state == StateRunning && s.stopGen == generation {
				s.state = StateStopped
				s.stop = previousStop
				s.updatedAt = time.Now().UTC()
				s.notifyLocked()
			}
			s.mu.Unlock()
		}
		return ActionResult{}, mapDAPError(err, action)
	}

	if waitForStop {
		// Release opMu before waiting: a pause or status inspection must not be
		// blocked behind a long-running step. The DAP request itself was already
		// serialized.
		s.opMu.Unlock()
		locked = false
		// A step usually reaches the next stop quickly. Wait briefly so common
		// callers can inspect the new frame immediately; a longer-running step
		// remains asynchronous and is reported honestly as running.
		waitCtx, waitCancel := context.WithTimeout(ctx, resumeStopWait)
		_ = s.waitFor(waitCtx, func() bool {
			return s.stopGen > generation || s.state.Terminal()
		})
		waitCancel()
	}

	s.mu.Lock()
	current := s.state
	nextGeneration := s.stopGen
	s.mu.Unlock()
	result := ActionResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		Action:         action,
		Accepted:       true,
		State:          current,
		StopGeneration: nextGeneration,
		ThreadID:       thread,
	}
	if current == StateRunning {
		result.Warnings = append(result.Warnings,
			"the request was accepted but the debuggee is still running; poll debug_status until stop_generation changes")
	}
	return result, nil
}

func (s *gdbSession) Pause(ctx context.Context, req PauseRequest) (ActionResult, error) {
	s.opMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.opMu.Unlock()
		}
	}()

	thread := req.ThreadID
	s.mu.Lock()
	state := s.state
	if thread <= 0 {
		thread = s.defaultThread
	}
	generation := s.stopGen
	s.mu.Unlock()
	if thread <= 0 {
		return ActionResult{}, Errorf(CodeInvalidArgument, "thread_id is required before the adapter has reported a thread")
	}
	if state != StateRunning {
		if state.Terminal() {
			return ActionResult{}, s.terminalError()
		}
		return ActionResult{}, Errorf(CodeNotRunning, "session is %s; pause requires a running session", state)
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return ActionResult{}, mapDAPError(err, "pause")
	}
	defer cancel()
	if _, err := s.client.Request(opCtx, "pause", map[string]any{"threadId": thread}); err != nil {
		return ActionResult{}, mapDAPError(err, "pause")
	}

	s.opMu.Unlock()
	locked = false
	waitCtx, waitCancel := context.WithTimeout(ctx, resumeStopWait)
	_ = s.waitFor(waitCtx, func() bool {
		return s.stopGen > generation || s.state.Terminal()
	})
	waitCancel()

	s.mu.Lock()
	current := s.state
	nextGeneration := s.stopGen
	s.mu.Unlock()
	result := ActionResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		Action:         "pause",
		Accepted:       true,
		State:          current,
		StopGeneration: nextGeneration,
		ThreadID:       thread,
	}
	if current == StateRunning {
		result.Warnings = append(result.Warnings,
			"the pause request was accepted but no stopped event arrived within the wait; poll debug_status")
	}
	return result, nil
}

func (s *gdbSession) Threads(ctx context.Context, req ThreadsRequest) (ThreadsResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	if req.Start < 0 {
		return ThreadsResult{}, Errorf(CodeInvalidArgument, "start must not be negative")
	}
	count := req.Count
	if count <= 0 {
		count = 100
	}
	if count > 500 {
		count = 500
	}

	s.mu.Lock()
	state := s.state
	generation := s.stopGen
	s.mu.Unlock()
	if state.Terminal() {
		return ThreadsResult{}, s.terminalError()
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return ThreadsResult{}, mapDAPError(err, "threads")
	}
	defer cancel()
	response, err := s.client.Request(opCtx, "threads", nil)
	if err != nil {
		return ThreadsResult{}, mapDAPError(err, "threads")
	}
	var body struct {
		Threads []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"threads"`
	}
	if err := response.DecodeBody(&body); err != nil {
		return ThreadsResult{}, Wrap(CodeProtocol, err, "decode threads response")
	}

	result := ThreadsResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		StopGeneration: generation,
		Start:          req.Start,
		Total:          len(body.Threads),
		Threads:        []Thread{},
	}
	if req.Start < len(body.Threads) {
		end := req.Start + count
		if end > len(body.Threads) {
			end = len(body.Threads)
		}
		for _, item := range body.Threads[req.Start:end] {
			result.Threads = append(result.Threads, Thread{ID: item.ID, Name: truncateText(item.Name, 256)})
		}
	}
	result.Returned = len(result.Threads)
	result.HasMore = req.Start+result.Returned < result.Total
	if result.HasMore {
		result.NextStart = req.Start + result.Returned
	}
	return result, nil
}

func (s *gdbSession) StackTrace(ctx context.Context, req StackRequest) (StackResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	state := s.state
	generation := s.stopGen
	s.mu.Unlock()
	if err := s.requireStopped(state, "stack trace"); err != nil {
		return StackResult{}, err
	}
	if err := staleGeneration(req.StopGeneration, generation); err != nil {
		return StackResult{}, err
	}
	if req.StartFrame < 0 {
		return StackResult{}, Errorf(CodeInvalidArgument, "start_frame must not be negative")
	}
	thread := req.ThreadID
	if thread <= 0 {
		thread, _ = s.threadForLocked(0)
	}
	if thread <= 0 {
		return StackResult{}, Errorf(CodeInvalidArgument, "thread_id is required")
	}
	levels := req.Levels
	if levels <= 0 {
		levels = 20
	}
	if levels > 100 {
		levels = 100
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return StackResult{}, mapDAPError(err, "stack trace")
	}
	defer cancel()
	response, err := s.client.Request(opCtx, "stackTrace", map[string]any{
		"threadId":   thread,
		"startFrame": req.StartFrame,
		"levels":     levels + 1,
	})
	if err != nil {
		return StackResult{}, mapDAPError(err, "stack trace")
	}
	var body struct {
		StackFrames []struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Line   int    `json:"line"`
			Column int    `json:"column"`
			Source *struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"source"`
		} `json:"stackFrames"`
		TotalFrames int `json:"totalFrames"`
	}
	if err := response.DecodeBody(&body); err != nil {
		return StackResult{}, Wrap(CodeProtocol, err, "decode stackTrace response")
	}
	if err := s.ensureGeneration(generation); err != nil {
		return StackResult{}, err
	}

	hasMore := len(body.StackFrames) > levels || body.TotalFrames > req.StartFrame+levels
	if len(body.StackFrames) > levels {
		body.StackFrames = body.StackFrames[:levels]
	}
	result := StackResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		StopGeneration: generation,
		StartFrame:     req.StartFrame,
		Returned:       len(body.StackFrames),
		HasMore:        hasMore,
		Frames:         make([]Frame, 0, len(body.StackFrames)),
	}
	if hasMore {
		result.NextFrame = req.StartFrame + len(body.StackFrames)
	}
	sourceCount := 0
	for _, item := range body.StackFrames {
		frame := Frame{
			ID:     item.ID,
			Name:   truncateText(item.Name, 512),
			Line:   item.Line,
			Column: item.Column,
		}
		if item.Source != nil {
			frame.Source = s.normalizeSource(item.Source.Name, item.Source.Path)
			if frame.Source != nil && !frame.Source.External {
				sourceCount++
			}
		}
		result.Frames = append(result.Frames, frame)
	}
	if result.Returned == 0 {
		result.Warnings = append(result.Warnings, "the adapter returned no stack frames")
	} else if sourceCount == 0 {
		result.Warnings = append(result.Warnings,
			"no workspace source is available for these frames; symbols may be stripped or the stop may be in a system library")
	}
	return result, nil
}

func (s *gdbSession) Scopes(ctx context.Context, req ScopesRequest) (ScopesResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	state := s.state
	generation := s.stopGen
	s.mu.Unlock()
	if err := s.requireStopped(state, "scopes"); err != nil {
		return ScopesResult{}, err
	}
	if err := staleGeneration(req.StopGeneration, generation); err != nil {
		return ScopesResult{}, err
	}
	if req.FrameID < 0 {
		return ScopesResult{}, Errorf(CodeInvalidArgument, "frame_id must not be negative")
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return ScopesResult{}, mapDAPError(err, "scopes")
	}
	defer cancel()
	response, err := s.client.Request(opCtx, "scopes", map[string]any{"frameId": req.FrameID})
	if err != nil {
		return ScopesResult{}, mapDAPError(err, "scopes")
	}
	var body struct {
		Scopes []struct {
			Name               string `json:"name"`
			VariablesReference int    `json:"variablesReference"`
			Expensive          bool   `json:"expensive"`
			NamedVariables     int    `json:"namedVariables"`
			IndexedVariables   int    `json:"indexedVariables"`
			Line               int    `json:"line"`
			Source             *struct {
				Name string `json:"name"`
				Path string `json:"path"`
			} `json:"source"`
		} `json:"scopes"`
	}
	if err := response.DecodeBody(&body); err != nil {
		return ScopesResult{}, Wrap(CodeProtocol, err, "decode scopes response")
	}
	if err := s.ensureGeneration(generation); err != nil {
		return ScopesResult{}, err
	}

	const maxScopes = 64
	scopes := body.Scopes
	if len(scopes) > maxScopes {
		scopes = scopes[:maxScopes]
	}
	result := ScopesResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		StopGeneration: generation,
		Scopes:         make([]Scope, 0, len(scopes)),
	}
	if len(body.Scopes) > maxScopes {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("scope list truncated to %d entries", maxScopes))
	}
	for _, item := range scopes {
		scope := Scope{
			Name:               truncateText(item.Name, 256),
			VariablesReference: item.VariablesReference,
			Expensive:          item.Expensive,
			NamedVariables:     item.NamedVariables,
			IndexedVariables:   item.IndexedVariables,
			Line:               item.Line,
		}
		if item.Source != nil {
			scope.Source = s.normalizeSource(item.Source.Name, item.Source.Path)
		}
		result.Scopes = append(result.Scopes, scope)
	}
	return result, nil
}

func (s *gdbSession) Variables(ctx context.Context, req VariablesRequest) (VariablesResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	state := s.state
	generation := s.stopGen
	s.mu.Unlock()
	if err := s.requireStopped(state, "variables"); err != nil {
		return VariablesResult{}, err
	}
	if err := staleGeneration(req.StopGeneration, generation); err != nil {
		return VariablesResult{}, err
	}
	if req.VariablesReference <= 0 {
		return VariablesResult{}, Errorf(CodeInvalidArgument, "variables_reference must be positive")
	}
	if req.Start < 0 {
		return VariablesResult{}, Errorf(CodeInvalidArgument, "start must not be negative")
	}
	count := req.Count
	if count <= 0 {
		count = 100
	}
	if count > 200 {
		count = 200
	}

	opCtx, cancel, err := s.operationContext(ctx)
	if err != nil {
		return VariablesResult{}, mapDAPError(err, "variables")
	}
	defer cancel()
	// GDB rejects start/count values above a variable's child count instead of
	// clamping them. Request the full child list and paginate locally; output
	// is still bounded by count and by the DAP frame limit.
	response, err := s.client.Request(opCtx, "variables", map[string]any{
		"variablesReference": req.VariablesReference,
	})
	if err != nil {
		return VariablesResult{}, mapDAPError(err, "variables")
	}
	var body struct {
		Variables []struct {
			Name               string `json:"name"`
			Value              string `json:"value"`
			Type               string `json:"type"`
			VariablesReference int    `json:"variablesReference"`
			NamedVariables     int    `json:"namedVariables"`
			IndexedVariables   int    `json:"indexedVariables"`
		} `json:"variables"`
	}
	if err := response.DecodeBody(&body); err != nil {
		return VariablesResult{}, Wrap(CodeProtocol, err, "decode variables response")
	}
	if err := s.ensureGeneration(generation); err != nil {
		return VariablesResult{}, err
	}

	result := VariablesResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		StopGeneration: generation,
		Start:          req.Start,
		Total:          len(body.Variables),
		Variables:      []Variable{},
	}
	if req.Start < len(body.Variables) {
		end := req.Start + count
		if end > len(body.Variables) {
			end = len(body.Variables)
		}
		truncatedValues := false
		for _, item := range body.Variables[req.Start:end] {
			value, truncated := truncateUTF8(item.Value, 2048)
			if truncated {
				truncatedValues = true
			}
			result.Variables = append(result.Variables, Variable{
				Name:               truncateText(item.Name, 256),
				Value:              value,
				Type:               truncateText(item.Type, 256),
				VariablesReference: item.VariablesReference,
				NamedVariables:     item.NamedVariables,
				IndexedVariables:   item.IndexedVariables,
				ValueTruncated:     truncated,
			})
		}
		if truncatedValues {
			result.Warnings = append(result.Warnings, "one or more values were truncated at 2048 bytes")
		}
	}
	result.Returned = len(result.Variables)
	result.HasMore = req.Start+result.Returned < result.Total
	if result.HasMore {
		result.NextStart = req.Start + result.Returned
	}
	return result, nil
}

func (s *gdbSession) Stop(ctx context.Context, req StopRequest) (StopResult, error) {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()

	terminate := true
	if req.Terminate != nil {
		terminate = *req.Terminate
	}

	s.mu.Lock()
	state := s.state
	already := state.Terminal()
	s.mu.Unlock()

	if already {
		s.releaseResources(true)
		return StopResult{
			SchemaVersion:  APISchemaVersion,
			SessionID:      s.id,
			Terminated:     state == StateTerminated,
			Detached:       state == StateDetached,
			AlreadyStopped: true,
			Status:         s.Status(),
		}, nil
	}

	if !terminate {
		// GDB 17.1 advertises supportTerminateDebuggee, but real detach tests
		// show that the inferior may still be killed when GDB is released.
		// Refusing is safer than reporting a detach that did not preserve the
		// debuggee. Callers can always terminate the session.
		return StopResult{}, Errorf(CodeUnsupported,
			"detach is not implemented at J6; call debug_stop with terminate=true")
	}

	stopCtx := context.Background()
	if ctx != nil && ctx.Err() == nil {
		stopCtx = ctx
	}
	disconnectCtx, cancel := context.WithTimeout(stopCtx, s.stopTimeout)
	defer cancel()
	_, disconnectErr := s.client.Request(disconnectCtx, "disconnect", map[string]any{
		"terminateDebuggee": true,
	})

	s.mu.Lock()
	s.state = StateTerminated
	s.stop = nil
	s.updatedAt = time.Now().UTC()
	// A clean stop must not retain an older transport error. If the process
	// survives below, the state and error are set explicitly.
	s.errText = ""
	s.notifyLocked()
	s.mu.Unlock()

	s.releaseResources(true)

	result := StopResult{
		SchemaVersion:  APISchemaVersion,
		SessionID:      s.id,
		Terminated:     true,
		Detached:       false,
		AlreadyStopped: false,
	}
	if s.proc != nil && !s.proc.Exited() {
		s.mu.Lock()
		s.state = StateFailed
		s.errText = "debuggee process did not terminate"
		s.updatedAt = time.Now().UTC()
		s.mu.Unlock()
		result.Terminated = false
		result.Warnings = append(result.Warnings, "the debuggee process did not terminate after SIGKILL")
	}
	if disconnectErr != nil {
		result.Warnings = append(result.Warnings,
			"the adapter disconnect request failed ("+truncateText(disconnectErr.Error(), 256)+")")
	}
	result.Status = s.Status()
	return result, nil
}

func (s *gdbSession) threadFor(requested int) (int, error) {
	if requested > 0 {
		return requested, nil
	}
	return s.threadForLocked(requested)
}

func (s *gdbSession) threadForLocked(requested int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if requested > 0 {
		return requested, nil
	}
	if s.stop != nil && s.stop.ThreadID > 0 {
		return s.stop.ThreadID, nil
	}
	if s.defaultThread > 0 {
		return s.defaultThread, nil
	}
	return 0, Errorf(CodeInvalidArgument, "thread_id is required before the adapter has reported a thread")
}

// definitiveResumeFailure reports whether the adapter proved that the resume
// request did not execute. Context errors and transport failures are not
// definitive: the request may already have been delivered.
func definitiveResumeFailure(err error) bool {
	var responseErr *dap.ResponseError
	if errors.As(err, &responseErr) {
		return true
	}
	return errors.Is(err, dap.ErrNotInitialized) ||
		errors.Is(err, dap.ErrInvalidState) ||
		errors.Is(err, dap.ErrInvalidArgument) ||
		errors.Is(err, dap.ErrPendingLimit) ||
		errors.Is(err, dap.ErrNilContext)
}

func (s *gdbSession) ensureGeneration(generation int) error {
	s.mu.Lock()
	current := s.stopGen
	s.mu.Unlock()
	return staleGeneration(generation, current)
}

func staleGeneration(requested, current int) error {
	if requested != 0 && requested != current {
		return Errorf(CodeStaleReference,
			"stop_generation %d is stale; the current generation is %d", requested, current)
	}
	return nil
}

func (s *gdbSession) requireStopped(state State, action string) error {
	if state.Terminal() {
		return s.terminalError()
	}
	if state != StateStopped {
		return Errorf(CodeNotStopped, "cannot %s while the session is %s; call debug_pause and wait for a stopped event", action, state)
	}
	return nil
}

func (s *gdbSession) terminalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	message := fmt.Sprintf("session is %s", s.state)
	if s.errText != "" {
		message += ": " + s.errText
	}
	return Errorf(CodeSessionTerminated, "%s", message)
}

func (s *gdbSession) normalizeSource(name, path string) *Source {
	if name == "" && path == "" {
		return nil
	}
	if strings.ContainsAny(name, `/\\`) {
		name = filepath.Base(name)
	}
	source := &Source{Name: truncateText(name, 256)}
	if source.Name == "" {
		source.Name = truncateText(filepath.Base(path), 256)
	}
	if path != "" && filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		// Resolve an existing source path before containment. DWARF may record
		// a build path through a symlinked root, which would otherwise be
		// misclassified as external. If the file no longer exists, keep the
		// lexical path and let containment fail closed.
		if resolved, err := filepath.EvalSymlinks(clean); err == nil {
			clean = resolved
		}
		if rel, ok := relativePathAny(s.roots, clean); ok {
			source.Path = filepath.ToSlash(rel)
			source.External = false
			return source
		}
	}
	source.External = true
	source.Path = ""
	return source
}

func relativePathAny(roots []string, path string) (string, bool) {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." {
			return rel, true
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return rel, true
		}
	}
	return "", false
}

func truncateText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	trimmed, _ := truncateUTF8(value, limit)
	return trimmed
}

// truncateUTF8 cuts at a UTF-8 rune boundary and reports whether it truncated.
func truncateUTF8(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func mapDAPError(err error, action string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "%s timed out", action)
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "%s was canceled", action)
	}
	if errors.Is(err, dap.ErrClosed) || errors.Is(err, dap.ErrEOF) {
		return Wrap(CodeSessionTerminated, err, "%s failed: the debugger transport closed", action)
	}
	var responseErr *dap.ResponseError
	if errors.As(err, &responseErr) {
		message := responseErr.Error()
		if strings.Contains(strings.ToLower(message), "not supported") ||
			strings.Contains(strings.ToLower(message), "unsupported") {
			return Wrap(CodeUnsupported, err, "%s is not supported by this GDB adapter: %s", action, truncateText(responseErr.Message, 512))
		}
		return Wrap(CodeProtocol, err, "%s failed: %s", action, truncateText(responseErr.Message, 512))
	}
	return Wrap(CodeProtocol, err, "%s failed", action)
}

func boolPtr(value bool) *bool { return &value }
