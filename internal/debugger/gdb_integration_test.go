package debugger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

type gdbHarness struct {
	service *Service
	root    string
	source  string
	program string
	gdbPath string
}

func requireGDB(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	if !filepath.IsAbs(path) {
		t.Skipf("gdb resolved through a relative PATH entry: %q", path)
	}
	if out, err := exec.Command(path, "--version").Output(); err != nil || !strings.Contains(strings.ToLower(string(out)), "gdb") {
		t.Skipf("gdb is not usable: %v %s", err, out)
	}
	return path
}

func buildDebugFixture(t *testing.T, root, name, source string, strip bool) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	src := filepath.Join(root, name+".c")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, name)
	args := []string{"-g", "-O0", "-fno-omit-frame-pointer", "-o", out, src}
	if strip {
		args = append([]string{"-s"}, args...)
	}
	command := exec.Command(cc, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, output)
	}
	return out
}

func newGDBHarness(t *testing.T, source string, strip bool) *gdbHarness {
	t.Helper()
	gdbPath := requireGDB(t)
	root := t.TempDir()
	program := buildDebugFixture(t, root, "fixture", source, strip)
	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root,
		}},
	}
	resolver := policy.New(cfg)
	versionBytes, err := exec.Command(gdbPath, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	version := strings.SplitN(string(versionBytes), "\n", 2)[0]
	service, err := NewService(ServiceOptions{
		Resolver:       resolver,
		Roots:          resolver.ResolvedRoots(),
		BackendEnabled: true,
		BackendAvailable: func() (bool, string) {
			return true, ""
		},
		BinaryProvider: func() (string, string, error) { return gdbPath, version, nil },
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxSessions:    2,
		RequestTimeout: 10 * time.Second,
		StartupTimeout: 15 * time.Second,
		StopTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return &gdbHarness{service: service, root: root, source: filepath.Join(root, "fixture.c"), program: program, gdbPath: gdbPath}
}

const breakpointSource = `#include <stdio.h>

static int add(int a, int b) {
    int sum = a + b;
    return sum;
}

int main(void) {
    int x = 1;
    int y = 2;
    printf("start\n");
    int z = add(x, y);
    printf("z=%d\n", z);
    return 0;
}
`

func (h *gdbHarness) start(t *testing.T, stopOnEntry bool) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := h.service.Start(ctx, StartRequest{TargetID: "fixture", StopOnEntry: &stopOnEntry})
	if err != nil {
		t.Fatalf("Start(stopOnEntry=%t) error = %v", stopOnEntry, err)
	}
	if !strings.HasPrefix(status.SessionID, "dbg_") {
		t.Errorf("session id = %q", status.SessionID)
	}
	return status
}

func (h *gdbHarness) waitFor(t *testing.T, sessionID string, predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := h.service.Status(context.Background(), StatusRequest{SessionID: sessionID})
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if predicate(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("status did not reach the expected state: %+v", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestGDBBreakpointStackScopesVariablesStep(t *testing.T) {
	h := newGDBHarness(t, breakpointSource, false)
	started := h.start(t, true)
	if started.State != StateStopped {
		t.Fatalf("started state = %q, want stopped", started.State)
	}
	if started.StopGeneration != 1 {
		t.Errorf("stop generation = %d, want 1", started.StopGeneration)
	}

	bp, err := h.service.SetBreakpoints(context.Background(), BreakpointsRequest{
		SessionID:  started.SessionID,
		SourcePath: "fixture.c",
		Lines:      []int{11},
	})
	if err != nil {
		t.Fatalf("SetBreakpoints() error = %v", err)
	}
	if len(bp.Breakpoints) != 1 || !bp.Breakpoints[0].Verified {
		t.Fatalf("breakpoints = %+v, want one verified breakpoint", bp.Breakpoints)
	}
	if bp.Source != "fixture.c" {
		t.Errorf("breakpoint source = %q, want fixture.c", bp.Source)
	}

	if _, err := h.service.Continue(context.Background(), ContinueRequest{SessionID: started.SessionID}); err != nil {
		t.Fatalf("Continue() error = %v", err)
	}
	stopped := h.waitFor(t, started.SessionID, func(status Status) bool {
		return status.State == StateStopped && status.StopGeneration > started.StopGeneration
	})
	if stopped.Stop == nil || stopped.Stop.ThreadID == 0 {
		t.Fatalf("stopped event = %+v", stopped.Stop)
	}

	threads, err := h.service.Threads(context.Background(), ThreadsRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Threads() error = %v", err)
	}
	if threads.Total == 0 || threads.Threads[0].ID == 0 {
		t.Fatalf("threads = %+v", threads)
	}

	stack, err := h.service.StackTrace(context.Background(), StackRequest{
		SessionID: started.SessionID,
		ThreadID:  stopped.Stop.ThreadID,
		Levels:    10,
	})
	if err != nil {
		t.Fatalf("StackTrace() error = %v", err)
	}
	var mainFrame *Frame
	for i := range stack.Frames {
		if stack.Frames[i].Name == "main" {
			mainFrame = &stack.Frames[i]
		}
		if stack.Frames[i].Source != nil && filepath.IsAbs(stack.Frames[i].Source.Path) {
			t.Errorf("absolute source path exposed: %+v", stack.Frames[i].Source)
		}
	}
	if mainFrame == nil {
		t.Fatalf("no main frame in %+v", stack.Frames)
	}
	if mainFrame.Source == nil || mainFrame.Source.Path != "fixture.c" || mainFrame.Source.External {
		t.Errorf("main source = %+v, want workspace fixture.c", mainFrame.Source)
	}
	if mainFrame.Line != 11 {
		t.Errorf("main line = %d, want 11", mainFrame.Line)
	}

	scopes, err := h.service.Scopes(context.Background(), ScopesRequest{
		SessionID: started.SessionID, FrameID: mainFrame.ID,
	})
	if err != nil {
		t.Fatalf("Scopes() error = %v", err)
	}
	var locals *Scope
	for i := range scopes.Scopes {
		if scopes.Scopes[i].Name == "Locals" {
			locals = &scopes.Scopes[i]
		}
	}
	if locals == nil {
		t.Fatalf("no Locals scope in %+v", scopes.Scopes)
	}
	variables, err := h.service.Variables(context.Background(), VariablesRequest{
		SessionID: started.SessionID, VariablesReference: locals.VariablesReference, Count: 20,
	})
	if err != nil {
		t.Fatalf("Variables() error = %v", err)
	}
	values := make(map[string]string)
	for _, variable := range variables.Variables {
		values[variable.Name] = variable.Value
	}
	if values["x"] != "1" || values["y"] != "2" {
		t.Errorf("locals = %+v, want x=1 y=2", variables.Variables)
	}

	stepResult, err := h.service.Step(context.Background(), StepRequest{
		SessionID: started.SessionID, Kind: "next", ThreadID: stopped.Stop.ThreadID,
	})
	if err != nil {
		t.Fatalf("Step() error = %v", err)
	}
	if !stepResult.Accepted || stepResult.State != StateStopped {
		t.Errorf("Step() = %+v, want an accepted stop", stepResult)
	}
	stepped := h.waitFor(t, started.SessionID, func(status Status) bool {
		return status.State == StateStopped && status.StopGeneration > stopped.StopGeneration
	})
	if stepped.Stop == nil || stepped.Stop.Reason != "step" {
		t.Errorf("step stop = %+v, want reason step", stepped.Stop)
	}

	stoppedResult, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !stoppedResult.Terminated || stoppedResult.Status.State != StateTerminated {
		t.Errorf("stop result = %+v", stoppedResult)
	}
	session, err := h.service.manager.Get(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	gdbSession, ok := session.(*gdbSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	if !gdbSession.proc.Exited() {
		t.Error("gdb process is still running after debug_stop")
	}
	again, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if !again.AlreadyStopped || again.Status.State != StateTerminated {
		t.Errorf("second Stop() = %+v", again)
	}
}

const hangSource = `#include <unistd.h>
int main(void) {
    for (;;) {
        sleep(1);
    }
    return 0;
}
`

func TestGDBRunningPauseStackTraceAndStop(t *testing.T) {
	h := newGDBHarness(t, hangSource, false)
	started := h.start(t, false)
	if started.State != StateRunning {
		t.Fatalf("started state = %q, want running", started.State)
	}

	pauseResult, err := h.service.Pause(context.Background(), PauseRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if !pauseResult.Accepted || pauseResult.State != StateStopped {
		t.Errorf("Pause() = %+v, want an accepted stop", pauseResult)
	}
	stopped := h.waitFor(t, started.SessionID, func(status Status) bool {
		return status.State == StateStopped && status.StopGeneration > started.StopGeneration
	})
	if stopped.Stop == nil {
		t.Fatal("pause stop event is nil")
	}
	threads, err := h.service.Threads(context.Background(), ThreadsRequest{SessionID: started.SessionID})
	if err != nil || threads.Total == 0 {
		t.Fatalf("Threads() = %+v, %v", threads, err)
	}
	stack, err := h.service.StackTrace(context.Background(), StackRequest{
		SessionID: started.SessionID, ThreadID: threads.Threads[0].ID, Levels: 20,
	})
	if err != nil || stack.Returned == 0 {
		t.Fatalf("StackTrace() = %+v, %v", stack, err)
	}

	result, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !result.Terminated || result.Status.State != StateTerminated {
		t.Errorf("stop result = %+v", result)
	}
}

func TestGDBStrippedSymbolsAreExplicitlyDegraded(t *testing.T) {
	h := newGDBHarness(t, breakpointSource, true)
	started := h.start(t, true)
	if started.State != StateStopped {
		t.Fatalf("started state = %q, want stopped", started.State)
	}

	stack, err := h.service.StackTrace(context.Background(), StackRequest{
		SessionID: started.SessionID, ThreadID: 1, Levels: 20,
	})
	if err != nil {
		t.Fatalf("StackTrace() error = %v", err)
	}
	for _, frame := range stack.Frames {
		if frame.Source != nil && filepath.IsAbs(frame.Source.Path) {
			t.Errorf("absolute source path exposed for stripped frame: %+v", frame.Source)
		}
	}
	if stack.Returned > 0 && len(stack.Warnings) == 0 {
		t.Error("stripped stack has no warning; a missing source must be explicit")
	}

	breakpoints, err := h.service.SetBreakpoints(context.Background(), BreakpointsRequest{
		SessionID: started.SessionID, SourcePath: "fixture.c", Lines: []int{11},
	})
	if err != nil {
		t.Fatalf("SetBreakpoints() error = %v", err)
	}
	if len(breakpoints.Breakpoints) != 1 {
		t.Fatalf("breakpoints = %+v", breakpoints.Breakpoints)
	}
	if breakpoints.Breakpoints[0].Verified && len(breakpoints.Warnings) == 0 {
		t.Log("GDB verified the breakpoint despite stripping; source availability still depends on debug info")
	}

	if _, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestGDBStopWhileRunningTerminatesSession(t *testing.T) {
	h := newGDBHarness(t, hangSource, false)
	started := h.start(t, false)
	if started.State != StateRunning {
		t.Fatalf("started state = %q, want running", started.State)
	}
	result, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !result.Terminated || result.Status.State != StateTerminated {
		t.Fatalf("Stop() = %+v", result)
	}
	session, err := h.service.manager.Get(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	gdbSession, ok := session.(*gdbSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	if !gdbSession.proc.Exited() {
		t.Error("gdb process is still running after stopping a running session")
	}
}

const shortLivedSource = `#include <unistd.h>
int main(void) {
    usleep(300000);
    return 0;
}
`

const detachSource = `#include <stdio.h>
#include <unistd.h>

int main(void) {
    FILE *file = fopen("debuggee.pid", "w");
    if (file) {
        fprintf(file, "%d\n", getpid());
        fclose(file);
    }
    for (;;) {
        sleep(1);
    }
    return 0;
}
`

func processAlive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	text := string(data)
	index := strings.LastIndex(text, ") ")
	if index < 0 || index+2 >= len(text) {
		return false
	}
	return text[index+2] != 'Z'
}

func waitDebuggeePID(t *testing.T, h *gdbHarness) int {
	t.Helper()
	pidPath := filepath.Join(h.root, "debuggee.pid")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse debuggee pid: %v", parseErr)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("debuggee did not write its pid: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGDBDetachUnsupportedLeavesSessionRunning(t *testing.T) {
	h := newGDBHarness(t, detachSource, false)
	started := h.start(t, false)
	pid := waitDebuggeePID(t, h)

	session, err := h.service.manager.Get(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	gdbSession, ok := session.(*gdbSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	if !gdbSession.caps.TerminateDebuggee {
		t.Error("GDB did not advertise supportTerminateDebuggee=true; detach refusal documentation needs review")
	}
	gdbGroup, _ := syscall.Getpgid(gdbSession.proc.cmd.Process.Pid)
	debuggeeGroup, _ := syscall.Getpgid(pid)
	t.Logf("gdb pid=%d pgid=%d; debuggee pid=%d pgid=%d",
		gdbSession.proc.cmd.Process.Pid, gdbGroup, pid, debuggeeGroup)

	detach := false
	if _, err := h.service.Stop(context.Background(), StopRequest{
		SessionID: started.SessionID,
		Terminate: &detach,
	}); CodeOf(err) != CodeUnsupported {
		t.Fatalf("Stop(terminate=false) error = %v, want unsupported_capability", err)
	}
	if !processAlive(pid) {
		t.Fatal("debuggee died after a rejected detach")
	}
	status, err := h.service.Status(context.Background(), StatusRequest{SessionID: started.SessionID})
	if err != nil || status.State != StateRunning {
		t.Errorf("status after rejected detach = %+v, %v; want running", status, err)
	}

	result, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Stop(terminate=true) error = %v", err)
	}
	if !result.Terminated || result.Status.State != StateTerminated {
		t.Fatalf("terminate result = %+v", result)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("debuggee %d survived stop(terminate=true)", pid)
	}
}

func TestGDBServiceCloseTerminatesActiveSession(t *testing.T) {
	h := newGDBHarness(t, breakpointSource, false)
	started := h.start(t, true)
	session, err := h.service.manager.Get(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	gdbSession, ok := session.(*gdbSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	h.service.Close()
	if !gdbSession.proc.Exited() {
		t.Error("gdb process survived service shutdown")
	}
	select {
	case <-gdbSession.Done():
	case <-time.After(2 * time.Second):
		t.Error("session resources were not released on service shutdown")
	}
}

func TestGDBDebuggeeTerminationIsReported(t *testing.T) {
	h := newGDBHarness(t, shortLivedSource, false)
	started := h.start(t, false)
	final := h.waitFor(t, started.SessionID, func(status Status) bool {
		return status.State == StateTerminated
	})
	if final.Exit == nil || final.Exit.ExitCode != 0 {
		t.Errorf("exit status = %+v, want code 0", final.Exit)
	}
	session, err := h.service.manager.Get(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	gdbSession, ok := session.(*gdbSession)
	if !ok {
		t.Fatalf("session type = %T", session)
	}
	select {
	case <-gdbSession.Done():
	case <-time.After(3 * time.Second):
		t.Error("terminal session resources were not released")
	}
	result, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !result.AlreadyStopped || !result.Terminated {
		t.Errorf("Stop() = %+v, want already-stopped terminated", result)
	}
}

func TestGDBStartFailureIsNamedAndLeavesNoSession(t *testing.T) {
	root := t.TempDir()
	fakeGDB := filepath.Join(root, "fake-gdb")
	if err := os.WriteFile(fakeGDB, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{ID: "fixture", Program: fakeGDB, Cwd: root}},
	}
	resolver := policy.New(cfg)
	service, err := NewService(ServiceOptions{
		Resolver:       resolver,
		Roots:          resolver.ResolvedRoots(),
		BackendEnabled: true,
		BackendAvailable: func() (bool, string) {
			return true, ""
		},
		BinaryProvider: func() (string, string, error) {
			return fakeGDB, "fake gdb 1.0", nil
		},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxSessions:    1,
		StartupTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	_, err = service.Start(context.Background(), StartRequest{TargetID: "fixture"})
	if err == nil {
		t.Fatal("Start() with a failing adapter succeeded")
	}
	if CodeOf(err) != CodeStartFailed {
		t.Errorf("Start() error = %v (code %s), want start_failed", err, CodeOf(err))
	}
	if service.manager.LiveCount() != 0 || service.manager.HasSessions() {
		t.Errorf("failed start left a session behind: live=%d", service.manager.LiveCount())
	}
}

const stdinSource = `#include <stdio.h>
int main(void) {
    char buffer[64];
    if (fgets(buffer, sizeof(buffer), stdin)) {
        return 1;
    }
    return 0;
}
`

func TestGDBInferiorStdinIsNotTheDAPPipe(t *testing.T) {
	h := newGDBHarness(t, stdinSource, false)
	started := h.start(t, false)
	final := h.waitFor(t, started.SessionID, func(status Status) bool {
		return status.State == StateTerminated
	})
	if final.Exit == nil || final.Exit.ExitCode != 0 {
		t.Errorf("exit = %+v, want 0 (stdin must observe EOF, not DAP bytes)", final.Exit)
	}
}
