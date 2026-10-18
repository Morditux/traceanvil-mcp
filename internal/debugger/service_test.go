package debugger

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

func setGDBBackend(opts *ServiceOptions, update func(*BackendOptions)) {
	backend := opts.Backends[BackendGDB]
	update(&backend)
	opts.Backends[BackendGDB] = backend
}

type serviceHarness struct {
	service  *Service
	root     string
	program  string
	resolver *policy.Resolver
	captured *sessionOptions
	session  *fakeSession
}

func newServiceHarness(t *testing.T, configure func(*ServiceOptions)) *serviceHarness {
	t.Helper()
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.c"), []byte("int main(void) { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID:          "fixture",
			Program:     program,
			Cwd:         root,
			AllowedArgs: []string{"--flag"},
		}},
	}
	resolver := policy.New(cfg)
	captured := &sessionOptions{}
	h := &serviceHarness{root: root, program: program, resolver: resolver, captured: captured}

	opts := ServiceOptions{
		Resolver: resolver,
		Roots:    resolver.ResolvedRoots(),
		Backends: map[string]BackendOptions{
			BackendGDB: {
				Enabled:        true,
				Available:      func() (bool, string) { return true, "" },
				BinaryProvider: func() (string, string, error) { return program, "fake-gdb 1.0", nil },
			},
		},
		MaxSessions:      2,
		MaxResponseItems: 5,
		ReapInterval:     time.Hour,
		Starter: func(_ context.Context, sessionOpts sessionOptions) (Session, error) {
			*captured = sessionOpts
			h.session = newFakeSession(sessionOpts.ID)
			h.session.dir = sessionOpts.Dir
			h.session.status.Backend = sessionOpts.Backend
			h.session.status.BackendVersion = sessionOpts.Version
			h.session.status.TargetID = sessionOpts.TargetID
			return h.session, nil
		},
	}
	if configure != nil {
		configure(&opts)
	}
	service, err := NewService(opts)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	h.service = service
	t.Cleanup(service.Close)
	return h
}

func (h *serviceHarness) start(t *testing.T, req StartRequest) Status {
	t.Helper()
	status, err := h.service.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start(%+v) error = %v", req, err)
	}
	return status
}

func TestServiceStartDefaultsAndPolicy(t *testing.T) {
	h := newServiceHarness(t, nil)
	status := h.start(t, StartRequest{TargetID: "fixture", Args: []string{"--flag"}})
	if status.SessionID != h.captured.ID {
		t.Errorf("status session = %q, captured = %q", status.SessionID, h.captured.ID)
	}
	if status.Backend != BackendGDB || status.TargetID != "fixture" {
		t.Errorf("status = %+v", status)
	}
	if !h.captured.StopOnEntry {
		t.Error("stop_on_entry default = false, want true")
	}
	if h.captured.Program != h.program || h.captured.Binary != h.program {
		t.Errorf("captured program/binary = %q/%q", h.captured.Program, h.captured.Binary)
	}
	if len(h.captured.Args) != 1 || h.captured.Args[0] != "--flag" {
		t.Errorf("captured args = %v", h.captured.Args)
	}
	if len(h.captured.Roots) == 0 || h.captured.Roots[0] != h.root {
		t.Errorf("captured roots = %v", h.captured.Roots)
	}

	falseValue := false
	h.start(t, StartRequest{TargetID: "fixture", StopOnEntry: &falseValue})
	if h.captured.StopOnEntry {
		t.Error("stop_on_entry = true, want explicit false")
	}
	if h.captured.Version != "fake-gdb 1.0" {
		t.Errorf("captured version = %q", h.captured.Version)
	}
}

func TestServiceStartErrors(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	if _, err := h.service.Start(ctx, StartRequest{TargetID: "missing"}); CodeOf(err) != CodeTargetUnknown {
		t.Errorf("unknown target error = %v, want target_unknown", err)
	}
	if _, err := h.service.Start(ctx, StartRequest{TargetID: "fixture", Backend: "lldb"}); CodeOf(err) != CodeUnsupported {
		t.Errorf("lldb backend error = %v, want unsupported_capability", err)
	}
	if _, err := h.service.Start(ctx, StartRequest{TargetID: "fixture", Args: []string{"--not-allowed"}}); CodeOf(err) != CodePolicyDenied {
		t.Errorf("denied args error = %v, want policy_denied", err)
	}
	if _, err := h.service.Start(ctx, StartRequest{TargetID: ""}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("empty target error = %v, want invalid_argument", err)
	}
}

func TestServiceBackendGates(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) { setGDBBackend(opts, func(gdb *BackendOptions) { gdb.Enabled = false }) })
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeBackendDisabled {
		t.Errorf("disabled backend error = %v, want backend_disabled", err)
	}

	h = newServiceHarness(t, func(opts *ServiceOptions) {
		setGDBBackend(opts, func(gdb *BackendOptions) {
			gdb.Available = func() (bool, string) { return false, "probe failed" }
		})
	})
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeBackendUnavailable {
		t.Errorf("unavailable backend error = %v, want backend_unavailable", err)
	}

	h = newServiceHarness(t, func(opts *ServiceOptions) {
		setGDBBackend(opts, func(gdb *BackendOptions) {
			gdb.BinaryProvider = func() (string, string, error) { return "/does/not/exist", "", nil }
		})
	})
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeBackendUnavailable {
		t.Errorf("bad binary error = %v, want backend_unavailable", err)
	}
}

func TestServiceQuotaAndSessionLifetime(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) { opts.MaxSessions = 1 })
	first := h.start(t, StartRequest{TargetID: "fixture"})
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeQuotaExceeded {
		t.Errorf("second start error = %v, want quota_exceeded", err)
	}
	if _, err := h.service.Stop(context.Background(), StopRequest{SessionID: first.SessionID}); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	h.session.setState(StateTerminated)
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); err != nil {
		t.Errorf("Start() after terminal session error = %v", err)
	}
	if !h.service.HasSessions() {
		t.Error("HasSessions() = false with retained sessions")
	}
}

func TestServiceStatusAndStopSurviveBackendLoss(t *testing.T) {
	available := true
	h := newServiceHarness(t, func(opts *ServiceOptions) {
		setGDBBackend(opts, func(gdb *BackendOptions) {
			gdb.Available = func() (bool, string) {
				if available {
					return true, ""
				}
				return false, "gdb removed"
			}
		})
	})
	started := h.start(t, StartRequest{TargetID: "fixture"})
	available = false

	if _, err := h.service.Status(context.Background(), StatusRequest{SessionID: started.SessionID}); err != nil {
		t.Errorf("Status() after backend loss error = %v", err)
	}
	if _, err := h.service.Stop(context.Background(), StopRequest{SessionID: started.SessionID}); err != nil {
		t.Errorf("Stop() after backend loss error = %v", err)
	}
}

func TestServiceBreakpointsResolveAndDeduplicate(t *testing.T) {
	h := newServiceHarness(t, nil)
	started := h.start(t, StartRequest{TargetID: "fixture"})

	result, err := h.service.SetBreakpoints(context.Background(), BreakpointsRequest{
		SessionID:  started.SessionID,
		SourcePath: "src/main.c",
		Lines:      []int{10, 10, 20},
	})
	if err != nil {
		t.Fatalf("SetBreakpoints() error = %v", err)
	}
	wantPath := filepath.Join(h.root, "src", "main.c")
	if h.session.breakpointsReq.SourcePath != wantPath {
		t.Errorf("resolved source = %q, want %q", h.session.breakpointsReq.SourcePath, wantPath)
	}
	if h.session.breakpointsReq.SourceDisplay != "src/main.c" {
		t.Errorf("display source = %q", h.session.breakpointsReq.SourceDisplay)
	}
	if len(h.session.breakpointsReq.Lines) != 2 || h.session.breakpointsReq.Lines[0] != 10 || h.session.breakpointsReq.Lines[1] != 20 {
		t.Errorf("lines = %v, want [10 20]", h.session.breakpointsReq.Lines)
	}
	if result.Source != "src/main.c" {
		t.Errorf("result source = %q", result.Source)
	}
}

func TestServiceBreakpointsRejectEscapes(t *testing.T) {
	h := newServiceHarness(t, nil)
	started := h.start(t, StartRequest{TargetID: "fixture"})

	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.c")
	if err := os.WriteFile(outside, []byte("int outside;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.root, "src", "link.c")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	messages := make(map[string]string)
	for _, source := range []string{"../outside.c", "src/link.c", "src/absent.c"} {
		_, err := h.service.SetBreakpoints(context.Background(), BreakpointsRequest{
			SessionID: started.SessionID, SourcePath: source, Lines: []int{1},
		})
		if CodeOf(err) != CodeSourceDenied {
			t.Errorf("SetBreakpoints(%q) error = %v, want source_denied", source, err)
			continue
		}
		messages[source] = err.Error()
	}
	var expected string
	for source, message := range messages {
		if expected == "" {
			expected = message
			continue
		}
		if message != expected {
			t.Errorf("source denial messages differ (%q vs %q); details must stay in logs", expected, message)
			break
		}
		_ = source
	}
}

func TestServiceBreakpointAndPaginationBounds(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) { opts.MaxResponseItems = 5 })
	started := h.start(t, StartRequest{TargetID: "fixture"})
	ctx := context.Background()

	for _, lines := range [][]int{nil, {0}, {1000001}} {
		if _, err := h.service.SetBreakpoints(ctx, BreakpointsRequest{SessionID: started.SessionID, SourcePath: "src/main.c", Lines: lines}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("lines %v error = %v, want invalid_argument", lines, err)
		}
	}
	tooMany := make([]int, maxBreakpoints+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	if _, err := h.service.SetBreakpoints(ctx, BreakpointsRequest{SessionID: started.SessionID, SourcePath: "src/main.c", Lines: tooMany}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("too many lines error = %v, want invalid_argument", err)
	}

	if _, err := h.service.Threads(ctx, ThreadsRequest{SessionID: started.SessionID, Count: 1000}); err != nil {
		t.Fatal(err)
	}
	if h.session.threadsReq.Count != 5 {
		t.Errorf("threads count = %d, want 5", h.session.threadsReq.Count)
	}
	if _, err := h.service.StackTrace(ctx, StackRequest{SessionID: started.SessionID, ThreadID: 1, Levels: 1000}); err != nil {
		t.Fatal(err)
	}
	if h.session.stackReq.Levels != 5 {
		t.Errorf("stack levels = %d, want 5", h.session.stackReq.Levels)
	}
	if _, err := h.service.Variables(ctx, VariablesRequest{SessionID: started.SessionID, VariablesReference: 1, Count: 1000}); err != nil {
		t.Fatal(err)
	}
	if h.session.variablesReq.Count != 5 {
		t.Errorf("variables count = %d, want 5", h.session.variablesReq.Count)
	}
	if _, err := h.service.Variables(ctx, VariablesRequest{SessionID: started.SessionID, VariablesReference: 0}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("zero variables reference error = %v, want invalid_argument", err)
	}
	if _, err := h.service.Threads(ctx, ThreadsRequest{SessionID: started.SessionID, Start: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("negative thread start error = %v, want invalid_argument", err)
	}
}

func TestServiceSessionNotFoundAndClosed(t *testing.T) {
	h := newServiceHarness(t, nil)
	if _, err := h.service.Status(context.Background(), StatusRequest{SessionID: "dbg_missing"}); CodeOf(err) != CodeSessionNotFound {
		t.Errorf("Status(unknown) error = %v, want session_not_found", err)
	}
	if _, err := h.service.Status(context.Background(), StatusRequest{}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Status(empty) error = %v, want invalid_argument", err)
	}
	h.service.Close()
	if _, err := h.service.Status(context.Background(), StatusRequest{SessionID: "dbg_missing"}); CodeOf(err) != CodeClosed {
		t.Errorf("Status() after Close error = %v, want server_closed", err)
	}
}

func TestServiceStepValidation(t *testing.T) {
	h := newServiceHarness(t, nil)
	started := h.start(t, StartRequest{TargetID: "fixture"})
	if _, err := h.service.Step(context.Background(), StepRequest{SessionID: started.SessionID, Kind: "sideways"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("bad step kind error = %v, want invalid_argument", err)
	}
	if _, err := h.service.Step(context.Background(), StepRequest{SessionID: started.SessionID, Kind: "next"}); err != nil {
		t.Errorf("Step(next) error = %v", err)
	}
	if !strings.HasPrefix(h.session.stepReq.Kind, "next") {
		t.Errorf("captured step = %+v", h.session.stepReq)
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(ServiceOptions{}); err == nil {
		t.Error("NewService without resolver succeeded")
	}
	if _, err := NewService(ServiceOptions{Resolver: policy.New(nil)}); err == nil {
		t.Error("NewService without binary provider succeeded")
	}
}

func TestServiceStartValidationEdges(t *testing.T) {
	h := newServiceHarness(t, nil)
	if _, err := h.service.Start(nil, StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Start(nil) error = %v", err)
	}
	if !h.service.HasTargets() {
		t.Error("HasTargets() = false")
	}
	h.service.Close()
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"}); CodeOf(err) != CodeClosed {
		t.Errorf("Start() after Close error = %v", err)
	}
	h.service.Close()
}

func TestServiceInputValidationEdges(t *testing.T) {
	h := newServiceHarness(t, nil)
	started := h.start(t, StartRequest{TargetID: "fixture"})
	ctx := context.Background()

	if _, err := h.service.Continue(ctx, ContinueRequest{SessionID: started.SessionID, ThreadID: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Continue(negative thread) error = %v", err)
	}
	if _, err := h.service.Pause(ctx, PauseRequest{SessionID: started.SessionID, ThreadID: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Pause(negative thread) error = %v", err)
	}
	if _, err := h.service.StackTrace(ctx, StackRequest{SessionID: started.SessionID, ThreadID: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("StackTrace(negative thread) error = %v", err)
	}
	if _, err := h.service.StackTrace(ctx, StackRequest{SessionID: started.SessionID, StartFrame: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("StackTrace(negative frame) error = %v", err)
	}
	if _, err := h.service.Scopes(ctx, ScopesRequest{SessionID: started.SessionID, FrameID: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Scopes(negative frame) error = %v", err)
	}
	if _, err := h.service.Variables(ctx, VariablesRequest{SessionID: started.SessionID, VariablesReference: 1, Start: -1}); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("Variables(negative start) error = %v", err)
	}
	if _, err := h.service.Stop(ctx, StopRequest{SessionID: "dbg_missing"}); CodeOf(err) != CodeSessionNotFound {
		t.Errorf("Stop(unknown) error = %v", err)
	}
	if _, err := h.service.SetBreakpoints(ctx, BreakpointsRequest{SessionID: "dbg_missing", SourcePath: "src/main.c", Lines: []int{1}}); CodeOf(err) != CodeSessionNotFound {
		t.Errorf("SetBreakpoints(unknown) error = %v", err)
	}
}

func TestServiceHasTargetsRevalidatesFilesystem(t *testing.T) {
	h := newServiceHarness(t, nil)
	if !h.service.HasTargets() {
		t.Fatal("HasTargets() = false for a valid target")
	}
	if err := os.Remove(h.program); err != nil {
		t.Fatal(err)
	}
	if h.service.HasTargets() {
		t.Error("HasTargets() = true after the target program disappeared")
	}
}

func TestServiceLLDBBackendSelection(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) {
		opts.Backends[BackendLLDB] = BackendOptions{
			Enabled:        true,
			Available:      func() (bool, string) { return true, "" },
			BinaryProvider: func() (string, string, error) { return "/bin/true", "fake-lldb 1.0", nil },
		}
	})
	status, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture", Backend: BackendLLDB})
	if err != nil {
		t.Fatalf("Start(lldb) error = %v", err)
	}
	if h.captured.Backend != BackendLLDB {
		t.Errorf("captured backend = %q", h.captured.Backend)
	}
	if status.Backend != BackendLLDB {
		t.Errorf("status backend = %q", status.Backend)
	}
}

func TestServiceLLDBDisabled(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) {
		opts.Backends[BackendLLDB] = BackendOptions{Enabled: false}
	})
	if _, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture", Backend: BackendLLDB}); CodeOf(err) != CodeBackendDisabled {
		t.Fatalf("Start(lldb) error = %v, want backend_disabled", err)
	}
}

func TestServiceDefaultsToAvailableBackend(t *testing.T) {
	h := newServiceHarness(t, func(opts *ServiceOptions) {
		setGDBBackend(opts, func(gdb *BackendOptions) { gdb.Enabled = false })
		opts.Backends[BackendLLDB] = BackendOptions{
			Enabled:        true,
			Available:      func() (bool, string) { return true, "" },
			BinaryProvider: func() (string, string, error) { return "/bin/true", "fake-lldb 1.0", nil },
		}
	})
	status, err := h.service.Start(context.Background(), StartRequest{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Start(default) error = %v", err)
	}
	if h.captured.Backend != BackendLLDB || status.Backend != BackendLLDB {
		t.Fatalf("captured backend = %q, status backend = %q; want lldb fallback", h.captured.Backend, status.Backend)
	}
}
