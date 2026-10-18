package debugger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/dap"
	"github.com/Morditux/traceanvil-mcp/internal/dap/daptest"
)

func minimalSession() *dapSession {
	spec, _ := backendSpecFor(BackendGDB)
	return &dapSession{
		id:       "dbg_00000000000000000000000000000000",
		targetID: "fixture",
		backend:  spec,
		state:    StateRunning,
		changed:  make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func TestGDBStatePreconditions(t *testing.T) {
	session := minimalSession()
	session.defaultThread = 1

	if _, err := session.SetBreakpoints(context.Background(), BreakpointsRequest{}); CodeOf(err) != CodeNotStopped {
		t.Errorf("SetBreakpoints running error = %v, want not_stopped", err)
	}
	if _, err := session.Continue(context.Background(), ContinueRequest{}); CodeOf(err) != CodeNotStopped {
		t.Errorf("Continue running error = %v, want not_stopped", err)
	}
	if _, err := session.StackTrace(context.Background(), StackRequest{ThreadID: 1}); CodeOf(err) != CodeNotStopped {
		t.Errorf("StackTrace running error = %v, want not_stopped", err)
	}
	if _, err := session.Scopes(context.Background(), ScopesRequest{FrameID: 0}); CodeOf(err) != CodeNotStopped {
		t.Errorf("Scopes running error = %v, want not_stopped", err)
	}
	if _, err := session.Variables(context.Background(), VariablesRequest{VariablesReference: 1}); CodeOf(err) != CodeNotStopped {
		t.Errorf("Variables running error = %v, want not_stopped", err)
	}
	if _, err := session.Step(context.Background(), StepRequest{Kind: "sideways"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Step invalid kind error = %v, want invalid_argument", err)
	}
}

func TestGDBPauseWhileStoppedAndTerminalErrors(t *testing.T) {
	session := minimalSession()
	session.state = StateStopped
	if _, err := session.Pause(context.Background(), PauseRequest{ThreadID: 1}); CodeOf(err) != CodeNotRunning {
		t.Errorf("Pause stopped error = %v, want not_running", err)
	}
	session.state = StateTerminated
	if _, err := session.Threads(context.Background(), ThreadsRequest{}); CodeOf(err) != CodeSessionTerminated {
		t.Errorf("Threads terminated error = %v, want session_terminated", err)
	}
}

func TestLateContinuedDoesNotClearNewerStop(t *testing.T) {
	session := minimalSession()
	session.state = StateStopped
	session.stopGen = 2
	session.stop = &Stop{Reason: "breakpoint", ThreadID: 7}
	session.handleEvent(dap.Event{Name: "continued"})
	if session.state != StateStopped || session.stopGen != 2 || session.stop == nil {
		t.Fatalf("late continued cleared the stop: state=%q gen=%d stop=%+v",
			session.state, session.stopGen, session.stop)
	}
}

func TestGDBStatusAndRelease(t *testing.T) {
	session := minimalSession()
	session.state = StateStopped
	session.stopGen = 1
	session.stop = &Stop{Reason: "stopped", ThreadID: 1, HitBreakpoints: []int{1}}
	session.stopOnEntry = true
	session.startedAt = time.Now().UTC()
	session.updatedAt = time.Now().UTC()
	status := session.Status()
	if status.State != StateStopped || status.Stop == nil || status.Stop.ThreadID != 1 {
		t.Errorf("status = %+v", status)
	}
	if len(status.Warnings) != 2 {
		t.Errorf("warnings = %v, want generation and entry-point warnings", status.Warnings)
	}

	home, err := os.MkdirTemp("", "mcp-debug-test-")
	if err != nil {
		t.Fatal(err)
	}
	session.home = home
	session.releaseResources(true)
	select {
	case <-session.done:
	case <-time.After(time.Second):
		t.Fatal("done was not closed")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("session home still exists: %v", err)
	}
	// Idempotent release.
	session.releaseResources(true)
}

func TestGDBThreadDefaultsAndOperationContext(t *testing.T) {
	session := minimalSession()
	session.defaultThread = 7
	if thread, err := session.threadFor(0); err != nil || thread != 7 {
		t.Errorf("threadFor() = %d, %v", thread, err)
	}
	if thread, err := session.threadFor(9); err != nil || thread != 9 {
		t.Errorf("threadFor(9) = %d, %v", thread, err)
	}
	empty := minimalSession()
	if _, err := empty.threadFor(0); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("threadFor(no threads) error = %v", err)
	}

	if _, _, err := session.operationContext(nil); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("operationContext(nil) error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := session.operationContext(canceled); err != context.Canceled {
		t.Errorf("operationContext(canceled) error = %v", err)
	}
}

func TestValidateDebuggerBinaryRejectsNonRegular(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateDebuggerBinary(filepath.Join(root, "dir")); err == nil {
		t.Error("directory accepted")
	}
}

func TestGDBStopAlreadyTerminated(t *testing.T) {
	session := minimalSession()
	session.state = StateTerminated
	result, err := session.Stop(context.Background(), StopRequest{})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !result.AlreadyStopped || !result.Terminated || result.Detached {
		t.Errorf("Stop() = %+v", result)
	}
	select {
	case <-session.done:
	default:
		t.Error("already terminated session did not release resources")
	}

	failed := minimalSession()
	failed.state = StateFailed
	failed.errText = "adapter failed"
	result, err = failed.Stop(context.Background(), StopRequest{})
	if err != nil {
		t.Fatalf("Stop(failed) error = %v", err)
	}
	if !result.AlreadyStopped || result.Terminated || result.Detached || result.Status.State != StateFailed {
		t.Errorf("Stop(failed) = %+v", result)
	}
}

func TestGDBStaleGenerationRejected(t *testing.T) {
	session := minimalSession()
	session.state = StateStopped
	session.stopGen = 2

	if _, err := session.StackTrace(context.Background(), StackRequest{ThreadID: 1, StopGeneration: 1}); CodeOf(err) != CodeStaleReference {
		t.Errorf("StackTrace stale error = %v, want stale_reference", err)
	}
	if _, err := session.Scopes(context.Background(), ScopesRequest{FrameID: 0, StopGeneration: 1}); CodeOf(err) != CodeStaleReference {
		t.Errorf("Scopes stale error = %v, want stale_reference", err)
	}
	if _, err := session.Variables(context.Background(), VariablesRequest{VariablesReference: 1, StopGeneration: 1}); CodeOf(err) != CodeStaleReference {
		t.Errorf("Variables stale error = %v, want stale_reference", err)
	}
}

func mockDAPSession(t *testing.T) (*dapSession, *daptest.Adapter) {
	t.Helper()
	conn, adapter := daptest.NewPair()
	adapter.Handle("initialize", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true, Body: map[string]any{}}
	})
	client, err := dap.New(conn, dap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	initCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.Initialize(initCtx, dap.InitializeRequestArguments{AdapterID: "mock"}); err != nil {
		t.Fatal(err)
	}
	session := minimalSession()
	session.client = client
	session.state = StateStopped
	session.stopGen = 1
	session.stop = &Stop{Reason: "breakpoint", ThreadID: 1}
	session.defaultThread = 1
	session.requestTimeout = time.Second
	t.Cleanup(func() {
		_ = client.Close()
		_ = adapter.Close()
	})
	return session, adapter
}

func TestGDBResumeTimeoutLeavesRunning(t *testing.T) {
	session, adapter := mockDAPSession(t)
	adapter.Handle("continue", func(daptest.Request) *daptest.Payload { return nil })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := session.Continue(ctx, ContinueRequest{}); CodeOf(err) != CodeTimeout {
		t.Fatalf("Continue() error = %v, want timeout", err)
	}
	status := session.Status()
	if status.State != StateRunning {
		t.Errorf("state after timed-out resume = %q, want running (request may have been delivered)", status.State)
	}
	if status.Stop != nil {
		t.Errorf("stop after timed-out resume = %+v, want nil", status.Stop)
	}
}

func TestGDBResumeDAPErrorRestoresStopped(t *testing.T) {
	session, adapter := mockDAPSession(t)
	adapter.Handle("continue", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: false, Message: "cannot continue"}
	})

	if _, err := session.Continue(context.Background(), ContinueRequest{}); CodeOf(err) != CodeProtocol {
		t.Fatalf("Continue() error = %v, want protocol_error", err)
	}
	status := session.Status()
	if status.State != StateStopped || status.Stop == nil || status.Stop.ThreadID != 1 {
		t.Errorf("state after rejected resume = %+v, want stopped with the previous stop", status)
	}
}

func TestGDBVerifyHardeningFailsClosed(t *testing.T) {
	if err := ValidateGDBHardening(""); err != nil {
		t.Fatalf("ValidateGDBHardening(clean) error = %v", err)
	}
	if err := ValidateGDBHardening("Undefined set command: debuginfod enabled\n"); err == nil {
		t.Fatal("ValidateGDBHardening accepted a rejected hardening command")
	}
}
