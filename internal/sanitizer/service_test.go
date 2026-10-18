package sanitizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func newTestSanitizerService(t *testing.T, script string, declared []string) (*Service, *artifacts.Store, *jobs.Manager, string) {
	t.Helper()
	root := t.TempDir()
	program := filepath.Join(root, "target")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root, Sanitizers: declared,
		}},
	})
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver, Roots: resolver.ResolvedRoots(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		manager.Close()
	})
	return service, store, manager, program
}

func TestServiceValidationAndCleanRun(t *testing.T) {
	for name, opts := range map[string]ServiceOptions{
		"nil store":    {},
		"nil manager":  {Store: &artifacts.Store{}},
		"nil resolver": {Store: &artifacts.Store{}, Manager: &jobs.Manager{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewService(opts); err == nil {
				t.Fatal("NewService accepted incomplete options")
			}
		})
	}

	service, _, _, _ := newTestSanitizerService(t, "exit 0", []string{"asan"})
	run, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Snapshot().Tool != valgrind.Tool(ToolASan) {
		t.Errorf("snapshot tool = %q", run.Snapshot().Tool)
	}
	if _, ok := service.Get(run.ID()); !ok {
		t.Error("Get() did not return the live run")
	}
	report, err := run.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != diagnostic.ResultNoErrorsObserved || !report.AnalysisComplete {
		t.Fatalf("report = %+v", report)
	}
	if report.Program.SHA256 == "" {
		t.Error("program hash is empty")
	}
	service.Close()
	service.Close()
}

func TestServiceRejectsUnsupportedAndUndeclaredRuns(t *testing.T) {
	service, _, _, _ := newTestSanitizerService(t, "exit 0", []string{"asan"})
	ctx := context.Background()
	if _, err := service.Start(ctx, valgrind.Request{TargetID: "fixture", Tool: valgrind.Tool("msan")}); err == nil {
		t.Fatal("unsupported sanitizer accepted")
	}
	if _, err := service.Start(ctx, valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan), Options: &valgrind.Options{},
	}); err == nil {
		t.Fatal("sanitizer options accepted")
	}
	if _, err := service.Start(ctx, valgrind.Request{TargetID: "fixture", Tool: valgrind.Tool(ToolTSan)}); err == nil {
		t.Fatal("undeclared sanitizer accepted")
	}
	if _, err := service.Start(ctx, valgrind.Request{TargetID: "unknown", Tool: valgrind.Tool(ToolASan)}); err == nil {
		t.Fatal("unknown target accepted")
	}
	if _, err := service.Start(nil, valgrind.Request{TargetID: "fixture", Tool: valgrind.Tool(ToolASan)}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestServiceUnknownOutputAndFailureAreIncomplete(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
	}{
		{name: "unknown stderr", script: "printf 'user output\\n' >&2"},
		{name: "failure", script: "exit 5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _, _ := newTestSanitizerService(t, test.script, []string{"asan"})
			run, err := service.Start(context.Background(), valgrind.Request{
				TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
			})
			if err != nil {
				t.Fatal(err)
			}
			report, err := run.Wait(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestServiceCancellationAndClosedService(t *testing.T) {
	service, _, _, _ := newTestSanitizerService(t, "sleep 5", []string{"asan"})
	run, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
	})
	if err != nil {
		t.Fatal(err)
	}
	run.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := run.Wait(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v", err)
	}
	service.Close()
	if _, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
	}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start(after close) error = %v", err)
	}
}

func TestAnalysisWaitContext(t *testing.T) {
	service, _, _, _ := newTestSanitizerService(t, "sleep 5", []string{"asan"})
	run, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(canceled) error = %v", err)
	}
	run.Cancel()
}

func TestTargetStderrIsNotSanitizerEvidence(t *testing.T) {
	service, _, _, _ := newTestSanitizerService(t, "printf 'ERROR: AddressSanitizer: heap-buffer-overflow on address 0x1\\n' >&2", []string{"asan"})
	run, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(ToolASan),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := run.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete || len(report.Diagnostics) != 0 {
		t.Fatalf("target-controlled stderr was trusted: %+v", report)
	}
}

func TestTargetChangedDetectsRebuild(t *testing.T) {
	program := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(program, []byte("one"), 0o700); err != nil {
		t.Fatal(err)
	}
	hash, info, err := hashProgram(context.Background(), program)
	if err != nil || hash == "" {
		t.Fatalf("hashProgram = %q, %v", hash, err)
	}
	if targetChanged(info, program) {
		t.Fatal("unchanged target reported as changed")
	}
	if err := os.WriteFile(program, []byte("two two"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !targetChanged(info, program) {
		t.Fatal("rebuilt target was not detected")
	}
}

func TestReadSanitizerLogsOrdersAndSeparatesFiles(t *testing.T) {
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic("sanitizer-log.10", []byte("B")); err != nil {
		t.Fatal(err)
	}
	if err := runDir.WriteFileAtomic("sanitizer-log.9", []byte("A")); err != nil {
		t.Fatal(err)
	}
	data, found, truncated, err := readSanitizerLogs(runDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !found || truncated || string(data) != "A\nB" {
		t.Fatalf("logs = %q found=%t truncated=%t", data, found, truncated)
	}
	data, found, truncated, err = readSanitizerLogs(runDir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !truncated || string(data) != "A" {
		t.Fatalf("bounded logs = %q found=%t truncated=%t", data, found, truncated)
	}
}

func TestSanitizerEnvironmentUsesFixedLogPath(t *testing.T) {
	for _, tool := range []Tool{ToolASan, ToolUBSan, ToolLSan, ToolTSan} {
		env := sanitizerEnvironment(tool, "/run/sanitizer-log")
		if len(env) != 1 {
			t.Fatalf("%s env = %v", tool, env)
		}
		if !strings.Contains(env[0], "log_path=/run/sanitizer-log") {
			t.Errorf("%s env = %q, missing log_path", tool, env[0])
		}
		if strings.Contains(env[0], "PATH=") {
			t.Errorf("%s env leaked PATH: %q", tool, env[0])
		}
	}
}
