package debugger

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

type fakeSession struct {
	id  string
	dir string

	mu        sync.Mutex
	status    Status
	closed    chan struct{}
	closeOnce sync.Once

	breakpointsReq BreakpointsRequest
	continueReq    ContinueRequest
	stepReq        StepRequest
	pauseReq       PauseRequest
	threadsReq     ThreadsRequest
	stackReq       StackRequest
	scopesReq      ScopesRequest
	variablesReq   VariablesRequest
	stopReq        StopRequest
}

func newFakeSession(id string) *fakeSession {
	return &fakeSession{
		id:     id,
		dir:    "/tmp",
		status: Status{SchemaVersion: APISchemaVersion, SessionID: id, Backend: BackendGDB, State: StateStopped},
		closed: make(chan struct{}),
	}
}

func (f *fakeSession) ID() string            { return f.id }
func (f *fakeSession) WorkingDir() string    { return f.dir }
func (f *fakeSession) Done() <-chan struct{} { return f.closed }

func (f *fakeSession) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeSession) setState(state State) {
	f.mu.Lock()
	f.status.State = state
	f.mu.Unlock()
}

func (f *fakeSession) SetBreakpoints(_ context.Context, req BreakpointsRequest) (BreakpointsResult, error) {
	f.mu.Lock()
	f.breakpointsReq = req
	f.mu.Unlock()
	return BreakpointsResult{SessionID: f.id, Source: req.SourceDisplay}, nil
}

func (f *fakeSession) Continue(_ context.Context, req ContinueRequest) (ActionResult, error) {
	f.mu.Lock()
	f.continueReq = req
	f.mu.Unlock()
	return ActionResult{SessionID: f.id, Action: "continue", Accepted: true}, nil
}

func (f *fakeSession) Step(_ context.Context, req StepRequest) (ActionResult, error) {
	f.mu.Lock()
	f.stepReq = req
	f.mu.Unlock()
	return ActionResult{SessionID: f.id, Action: "step_" + req.Kind, Accepted: true}, nil
}

func (f *fakeSession) Pause(_ context.Context, req PauseRequest) (ActionResult, error) {
	f.mu.Lock()
	f.pauseReq = req
	f.mu.Unlock()
	return ActionResult{SessionID: f.id, Action: "pause", Accepted: true}, nil
}

func (f *fakeSession) Threads(_ context.Context, req ThreadsRequest) (ThreadsResult, error) {
	f.mu.Lock()
	f.threadsReq = req
	f.mu.Unlock()
	return ThreadsResult{SessionID: f.id}, nil
}

func (f *fakeSession) StackTrace(_ context.Context, req StackRequest) (StackResult, error) {
	f.mu.Lock()
	f.stackReq = req
	f.mu.Unlock()
	return StackResult{SessionID: f.id}, nil
}

func (f *fakeSession) Scopes(_ context.Context, req ScopesRequest) (ScopesResult, error) {
	f.mu.Lock()
	f.scopesReq = req
	f.mu.Unlock()
	return ScopesResult{SessionID: f.id}, nil
}

func (f *fakeSession) Variables(_ context.Context, req VariablesRequest) (VariablesResult, error) {
	f.mu.Lock()
	f.variablesReq = req
	f.mu.Unlock()
	return VariablesResult{SessionID: f.id}, nil
}

func (f *fakeSession) Stop(_ context.Context, req StopRequest) (StopResult, error) {
	f.mu.Lock()
	f.stopReq = req
	f.status.State = StateTerminated
	f.mu.Unlock()
	return StopResult{SessionID: f.id, Terminated: true, Status: f.Status()}, nil
}

func (f *fakeSession) Close() {
	f.closeOnce.Do(func() { close(f.closed) })
}

func waitClosed(t *testing.T, session *fakeSession) {
	t.Helper()
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("session was not closed")
	}
}

func TestManagerSessionIDsAndQuota(t *testing.T) {
	manager := NewManager(ManagerOptions{MaxSessions: 1, ReapInterval: time.Hour})
	defer manager.Close()

	var first *fakeSession
	start := func(_ context.Context, id string) (Session, error) {
		first = newFakeSession(id)
		return first, nil
	}
	session, err := manager.Start(context.Background(), start)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !regexp.MustCompile(SessionIDPattern).MatchString(session.ID()) {
		t.Errorf("session id = %q, want %s", session.ID(), SessionIDPattern)
	}
	if manager.LiveCount() != 1 {
		t.Errorf("LiveCount() = %d, want 1", manager.LiveCount())
	}

	if _, err := manager.Start(context.Background(), start); err == nil {
		t.Fatal("second Start() succeeded at the session limit")
	} else {
		var limit *LimitError
		if !errors.As(err, &limit) || limit.Max != 1 {
			t.Errorf("second Start() error = %v, want LimitError{Max:1}", err)
		}
	}

	first.setState(StateTerminated)
	second, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		return newFakeSession(id), nil
	})
	if err != nil {
		t.Fatalf("Start() after terminal session error = %v", err)
	}
	if manager.LiveCount() != 1 {
		t.Errorf("LiveCount() = %d, want 1", manager.LiveCount())
	}
	if _, err := manager.Get(second.ID()); err != nil {
		t.Errorf("Get(second) error = %v", err)
	}
}

func TestManagerReapsIdleAndRetainedSessions(t *testing.T) {
	manager := NewManager(ManagerOptions{
		MaxSessions:   2,
		IdleTimeout:   40 * time.Millisecond,
		RetainTimeout: 40 * time.Millisecond,
		ReapInterval:  time.Hour,
	})
	defer manager.Close()

	var idle, terminal *fakeSession
	if _, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		idle = newFakeSession(id)
		return idle, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		terminal = newFakeSession(id)
		terminal.setState(StateTerminated)
		return terminal, nil
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(70 * time.Millisecond)
	manager.Reap()

	waitClosed(t, idle)
	waitClosed(t, terminal)
	if _, err := manager.Get(idle.ID()); err == nil {
		t.Error("idle session is still registered after Reap")
	}
	if _, err := manager.Get(terminal.ID()); err == nil {
		t.Error("terminal session is still registered after retention")
	}
}

func TestManagerCloseAndClosedAdmission(t *testing.T) {
	manager := NewManager(ManagerOptions{MaxSessions: 2, ReapInterval: time.Hour})
	var session *fakeSession
	if _, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		session = newFakeSession(id)
		return session, nil
	}); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	waitClosed(t, session)
	manager.Close() // idempotent

	if _, err := manager.Get(session.ID()); !errors.Is(err, ErrClosed) {
		t.Errorf("Get() after Close error = %v, want ErrClosed", err)
	}
	if _, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		return newFakeSession(id), nil
	}); !errors.Is(err, ErrClosed) {
		t.Errorf("Start() after Close error = %v, want ErrClosed", err)
	}
}

func TestManagerGetUpdatesActivityAndUnknownID(t *testing.T) {
	manager := NewManager(ManagerOptions{MaxSessions: 1, ReapInterval: time.Hour})
	defer manager.Close()
	var session *fakeSession
	if _, err := manager.Start(context.Background(), func(_ context.Context, id string) (Session, error) {
		session = newFakeSession(id)
		return session, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get("dbg_does_not_exist"); CodeOf(err) != CodeSessionNotFound {
		t.Errorf("Get(unknown) error = %v, want session_not_found", err)
	}
	if _, err := manager.Get(""); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Get(empty) error = %v, want invalid_argument", err)
	}
	if got, err := manager.Get(session.ID()); err != nil || got != session {
		t.Errorf("Get() = %v, %v", got, err)
	}
}

func TestManagerStartCanceledContext(t *testing.T) {
	manager := NewManager(ManagerOptions{MaxSessions: 1, ReapInterval: time.Hour})
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Start(ctx, func(_ context.Context, id string) (Session, error) {
		return newFakeSession(id), nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("Start(canceled) error = %v, want context.Canceled", err)
	}
}

func TestManagerStartValidationAndClosedManager(t *testing.T) {
	manager := NewManager(ManagerOptions{})
	defer manager.Close()
	if _, err := manager.Start(nil, func(context.Context, string) (Session, error) {
		return nil, nil
	}); CodeOf(err) != CodeInvalidArgument && err == nil {
		t.Errorf("Start(nil) error = %v", err)
	}
	if _, err := manager.Start(context.Background(), nil); CodeOf(err) != CodeInternal {
		t.Errorf("Start(nil starter) error = %v", err)
	}
	if _, err := manager.Start(context.Background(), func(context.Context, string) (Session, error) {
		return nil, nil
	}); CodeOf(err) != CodeInternal {
		t.Errorf("Start(nil session) error = %v", err)
	}
	if _, err := manager.Start(context.Background(), func(context.Context, string) (Session, error) {
		return newFakeSession("dbg_wrong"), nil
	}); CodeOf(err) != CodeInternal {
		t.Errorf("Start(mismatched id) error = %v", err)
	}
}

func TestManagerStartObservesCancellation(t *testing.T) {
	manager := NewManager(ManagerOptions{MaxSessions: 1, ReapInterval: time.Hour})
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := manager.Start(ctx, func(startCtx context.Context, id string) (Session, error) {
			close(started)
			<-startCtx.Done()
			return nil, startCtx.Err()
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Start() error = %v, want context.Canceled", err)
	}
}

func TestManagerDefaultsAndEmptyGet(t *testing.T) {
	manager := NewManager(ManagerOptions{})
	defer manager.Close()
	if manager.opts.MaxSessions != 1 || manager.opts.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("defaults = %+v", manager.opts)
	}
	if _, err := manager.Get(""); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Get(empty) error = %v", err)
	}
	if manager.HasSessions() {
		t.Error("new manager has sessions")
	}
}
