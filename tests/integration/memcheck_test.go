package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

type memcheckHarness struct {
	service  *valgrind.Service
	store    *artifacts.Store
	manager  *jobs.Manager
	root     string
	program  string
	resolver *policy.Resolver
	valgrind string
	version  string
}

func requireTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed; skipping real-tool test", name)
	}
	if !filepath.IsAbs(path) {
		t.Skipf("%s resolved through a relative PATH entry: %q", name, path)
	}
	return path
}

func buildFixture(t *testing.T, outputDir, sourceDir, source string) string {
	t.Helper()
	cc := requireTool(t, "cc")
	src := filepath.Join(sourceDir, source+".c")
	out := filepath.Join(outputDir, source)
	command := exec.Command(cc, "-g", "-O0", "-fno-omit-frame-pointer", "-o", out, src)
	command.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile %s: %v\n%s", source, err, output)
	}
	return out
}

func newMemcheckHarness(t *testing.T, source string, timeout time.Duration, rawLimit int64) *memcheckHarness {
	t.Helper()
	valgrindPath := requireTool(t, "valgrind")
	versionOutput, err := exec.Command(valgrindPath, "--version").Output()
	if err != nil {
		t.Fatalf("valgrind --version: %v", err)
	}

	root := t.TempDir()
	program := buildFixture(t, root, filepath.Join("..", "..", "testdata", "fixtures", "c"), source)

	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID:      "fixture",
			Program: program,
			Cwd:     root,
		}},
	}
	resolver := policy.New(cfg)

	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{
		MaxJobs:        1,
		Timeout:        timeout,
		MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := valgrind.NewService(valgrind.ServiceOptions{
		Binary:         valgrindPath,
		Version:        strings.TrimSpace(string(versionOutput)),
		Store:          store,
		Manager:        manager,
		Resolver:       resolver,
		Roots:          []string{root},
		MaxRawXMLBytes: rawLimit,
		WatchInterval:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		manager.Close()
	})

	return &memcheckHarness{
		service:  service,
		store:    store,
		manager:  manager,
		root:     root,
		program:  program,
		resolver: resolver,
		valgrind: valgrindPath,
		version:  strings.TrimSpace(string(versionOutput)),
	}
}

func (h *memcheckHarness) run(t *testing.T, options valgrind.Options) (*valgrind.Analysis, diagnostic.Report) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	analysis, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture", Options: &options})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	report, err := analysis.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if report == nil {
		t.Fatal("Wait() returned a nil report")
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("invalid report: %v", err)
	}
	return analysis, *report
}

func TestMemcheckInvalidReadEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "invalid_read", 10*time.Second, 0)
	analysis, report := h.run(t, valgrind.DefaultOptions())

	if report.Result != diagnostic.ResultErrorsFound {
		t.Fatalf("Result = %q, want errors_found (warnings: %v)", report.Result, report.Warnings)
	}
	if !report.AnalysisComplete {
		t.Fatalf("AnalysisComplete = false (warnings: %v)", report.Warnings)
	}
	if len(report.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1", len(report.Diagnostics))
	}
	diag := report.Diagnostics[0]
	if diag.Kind != "InvalidRead" || diag.Severity != diagnostic.SeverityError {
		t.Errorf("diagnostic = %+v", diag)
	}
	if diag.Location == nil || diag.Location.Function != "main" || diag.Location.Line == 0 {
		t.Errorf("Location = %+v, want a symbolized location", diag.Location)
	}
	if diag.Evidence.Artifact != valgrind.RawArtifactName {
		t.Errorf("Evidence = %+v", diag.Evidence)
	}
	if report.Program.ExitCode != valgrind.ErrorExitCode {
		t.Errorf("ExitCode = %d, want the error-exitcode marker %d", report.Program.ExitCode, valgrind.ErrorExitCode)
	}
	if report.Program.SHA256 == "" {
		t.Error("Program.SHA256 is empty")
	}
	if report.Tool.Path == "" {
		t.Error("Tool.Path is empty")
	}
	if report.Metadata.StartTime == "" || report.Metadata.DurationMS < 0 {
		t.Errorf("Metadata = %+v", report.Metadata)
	}
	if snapshot := analysis.Snapshot(); snapshot.Phase != valgrind.PhaseCompleted {
		t.Errorf("Phase = %q, want completed", snapshot.Phase)
	}

	dir, err := h.store.Path(analysis.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"result.json", "analysis.json", "memcheck.xml", "stdout.log", "stderr.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("artifact %q missing: %v", name, err)
		}
	}

	var job jobs.Snapshot
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if job.State != jobs.StateCompleted || job.Result == nil || job.Result.ExitCode != valgrind.ErrorExitCode {
		t.Errorf("job = %+v, want completed with exit code 42", job)
	}

	var stored diagnostic.Report
	data, err = os.ReadFile(filepath.Join(dir, "analysis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("parse analysis.json: %v", err)
	}
	if stored.RunID != report.RunID || stored.Result != report.Result || len(stored.Diagnostics) != 1 {
		t.Errorf("stored report = %+v, want the returned report", stored)
	}

	raw, err := os.ReadFile(filepath.Join(dir, valgrind.RawArtifactName))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if len(report.Artifacts) != 1 || report.Artifacts[0].SHA256 != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("Artifacts = %+v, want the raw XML digest", report.Artifacts)
	}
}

func TestMemcheckKindsEndToEnd(t *testing.T) {
	cases := []struct {
		fixture string
		kind    string
	}{
		{"invalid_write", "InvalidWrite"},
		{"use_after_free", "InvalidRead"},
		{"double_free", "InvalidFree"},
		{"uninit", "UninitCondition"},
	}
	for _, test := range cases {
		t.Run(test.fixture, func(t *testing.T) {
			h := newMemcheckHarness(t, test.fixture, 10*time.Second, 0)
			_, report := h.run(t, valgrind.DefaultOptions())
			if report.Result != diagnostic.ResultErrorsFound {
				t.Fatalf("Result = %q, want errors_found (warnings: %v)", report.Result, report.Warnings)
			}
			found := false
			for _, diag := range report.Diagnostics {
				if diag.Kind == test.kind {
					found = true
					if diag.Location == nil || diag.Location.Line == 0 {
						t.Errorf("%s diagnostic has no source line: %+v", test.kind, diag.Location)
					}
				}
			}
			if !found {
				t.Errorf("kinds = %+v, want %s", report.Summary.ByKind, test.kind)
			}
		})
	}
}

func TestMemcheckLeaksEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "leak", 10*time.Second, 0)
	_, report := h.run(t, valgrind.DefaultOptions())

	kinds := report.Summary.ByKind
	if kinds["Leak_DefinitelyLost"] != 1 || kinds["Leak_StillReachable"] != 1 {
		t.Fatalf("ByKind = %+v, want one definitely-lost and one still-reachable", kinds)
	}
	if report.Summary.LeakedBytes < 8 {
		t.Errorf("LeakedBytes = %d, want at least the definitely-lost block", report.Summary.LeakedBytes)
	}
	for _, diag := range report.Diagnostics {
		if diag.Kind == "Leak_StillReachable" && diag.Severity != diagnostic.SeverityInfo {
			t.Errorf("still-reachable severity = %q, want info", diag.Severity)
		}
	}
}

func TestMemcheckCleanProgramExitCodeEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "clean", 10*time.Second, 0)
	_, report := h.run(t, valgrind.DefaultOptions())

	if report.Result != diagnostic.ResultNoErrorsObserved {
		t.Fatalf("Result = %q, want no_errors_observed (warnings: %v)", report.Result, report.Warnings)
	}
	if !report.AnalysisComplete {
		t.Errorf("AnalysisComplete = false (warnings: %v)", report.Warnings)
	}
	if report.Summary.Total != 0 {
		t.Errorf("Summary.Total = %d, want 0", report.Summary.Total)
	}
	if report.Program.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want the program's own 7", report.Program.ExitCode)
	}
}

func TestMemcheckSigsegvEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "sigsegv", 10*time.Second, 0)
	_, report := h.run(t, valgrind.DefaultOptions())

	if report.Result != diagnostic.ResultErrorsFound {
		t.Fatalf("Result = %q, want errors_found (warnings: %v)", report.Result, report.Warnings)
	}
	if report.Program.Signal != 11 {
		t.Errorf("Signal = %d, want 11", report.Program.Signal)
	}
	found := false
	for _, diag := range report.Diagnostics {
		if diag.Kind == "InvalidWrite" {
			found = true
		}
	}
	if !found {
		t.Errorf("kinds = %+v, want InvalidWrite", report.Summary.ByKind)
	}
}

func TestMemcheckTruncatedXMLIsNotClean(t *testing.T) {
	h := newMemcheckHarness(t, "many_errors", 10*time.Second, 1024)
	_, report := h.run(t, valgrind.DefaultOptions())

	if report.Result == diagnostic.ResultNoErrorsObserved {
		t.Fatal("truncated XML was reported as no_errors_observed")
	}
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true after truncation")
	}
	if report.Result != diagnostic.ResultIncomplete && report.Result != diagnostic.ResultToolError {
		t.Errorf("Result = %q, want incomplete or tool_error", report.Result)
	}
	if len(report.Artifacts) != 1 || !report.Artifacts[0].Truncated {
		t.Errorf("Artifacts = %+v, want a truncated raw artifact", report.Artifacts)
	}
}

func TestMemcheckTimeoutEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "hang", 400*time.Millisecond, 0)
	analysis, report := h.run(t, valgrind.DefaultOptions())

	if report.Result != diagnostic.ResultIncomplete {
		t.Errorf("Result = %q, want incomplete", report.Result)
	}
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true after a timeout")
	}
	if snapshot := analysis.Snapshot(); snapshot.Phase != valgrind.PhaseTimedOut {
		t.Errorf("Phase = %q, want timed_out", snapshot.Phase)
	}
}

func TestMemcheckCancellationEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "hang", 30*time.Second, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	analysis, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	analysis.Cancel()
	report, err := analysis.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report == nil {
		t.Fatal("Wait() returned a nil report")
	}
	if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete {
		t.Errorf("Result=%q complete=%t, want an incomplete analysis", report.Result, report.AnalysisComplete)
	}
	if snapshot := analysis.Snapshot(); snapshot.Phase != valgrind.PhaseCancelled {
		t.Errorf("Phase = %q, want cancelled", snapshot.Phase)
	}
}

func TestMemcheckSuppressionProfileEndToEnd(t *testing.T) {
	h := newMemcheckHarness(t, "invalid_read", 10*time.Second, 0)
	suppression := filepath.Join(h.root, "addr4.supp")
	content := "{\n   ignore_addr4_main\n   Memcheck:Addr4\n   fun:main\n}\n"
	if err := os.WriteFile(suppression, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	service, err := valgrind.NewService(valgrind.ServiceOptions{
		Binary:       h.valgrind,
		Version:      h.version,
		Store:        h.store,
		Manager:      h.manager,
		Resolver:     h.resolver,
		Roots:        []string{h.root},
		Suppressions: map[string]string{"addr4": suppression},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	options := valgrind.DefaultOptions()
	options.SuppressionsProfile = "addr4"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	analysis, err := service.Start(ctx, valgrind.Request{TargetID: "fixture", Options: &options})
	if err != nil {
		t.Fatal(err)
	}
	report, err := analysis.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != diagnostic.ResultNoErrorsObserved {
		t.Fatalf("Result = %q (warnings: %v)", report.Result, report.Warnings)
	}
	if report.Summary.Suppressed != 1 {
		t.Errorf("Suppressed = %d, want 1", report.Summary.Suppressed)
	}
	found := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "not evidence of absence") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a suppression warning", report.Warnings)
	}
}

func TestMemcheckUnknownTargetAndOptions(t *testing.T) {
	h := newMemcheckHarness(t, "clean", 10*time.Second, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := h.service.Start(ctx, valgrind.Request{TargetID: "missing"}); err == nil {
		t.Fatal("Start() accepted an unknown target")
	} else {
		var denied *policy.DeniedError
		if !errors.As(err, &denied) {
			t.Errorf("error = %T %v, want policy.DeniedError", err, err)
		}
	}

	options := valgrind.DefaultOptions()
	options.TraceChildren = true
	if _, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture", Options: &options}); err == nil {
		t.Fatal("Start() accepted trace_children=true")
	} else {
		var optionErr *valgrind.OptionError
		if !errors.As(err, &optionErr) {
			t.Errorf("error = %T %v, want OptionError", err, err)
		}
	}

	options = valgrind.DefaultOptions()
	options.SuppressionsProfile = "missing"
	if _, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture", Options: &options}); err == nil {
		t.Fatal("Start() accepted an unknown suppression profile")
	}
}

func TestMemcheckRejectsMissingBackend(t *testing.T) {
	dir := t.TempDir()
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	service, err := valgrind.NewService(valgrind.ServiceOptions{
		Binary:   filepath.Join(dir, "missing-valgrind"),
		Store:    store,
		Manager:  manager,
		Resolver: policy.New(&config.Config{WorkspaceRoots: []string{dir}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	if _, err := service.Start(context.Background(), valgrind.Request{TargetID: "fixture"}); !errors.Is(err, valgrind.ErrBackendUnavailable) {
		t.Errorf("Start() error = %v, want ErrBackendUnavailable", err)
	}
}

func TestMemcheckAnalyzeConvenienceAndGet(t *testing.T) {
	h := newMemcheckHarness(t, "clean", 10*time.Second, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report, err := h.service.Analyze(ctx, valgrind.Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if report.Result != diagnostic.ResultNoErrorsObserved {
		t.Fatalf("Result = %q (warnings: %v)", report.Result, report.Warnings)
	}
	analysis, ok := h.service.Get(report.RunID)
	if !ok {
		t.Fatalf("Get(%q) = false, want the retained analysis", report.RunID)
	}
	if analysis.ID() != report.RunID {
		t.Errorf("ID() = %q, want %q", analysis.ID(), report.RunID)
	}
	if snapshot := analysis.Snapshot(); snapshot.Report == nil || snapshot.Phase != valgrind.PhaseCompleted {
		t.Errorf("Snapshot() = %+v", snapshot)
	}

	h.service.Close()
	h.service.Close() // idempotent
	if _, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture"}); !errors.Is(err, valgrind.ErrClosed) {
		t.Errorf("Start() after Close error = %v, want ErrClosed", err)
	}
}

func TestMemcheckBrokenSuppressionIsNotClean(t *testing.T) {
	h := newMemcheckHarness(t, "invalid_read", 10*time.Second, 0)
	suppression := filepath.Join(h.root, "broken.supp")
	if err := os.WriteFile(suppression, []byte("this is not a suppression file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := valgrind.NewService(valgrind.ServiceOptions{
		Binary:       h.valgrind,
		Version:      h.version,
		Store:        h.store,
		Manager:      h.manager,
		Resolver:     h.resolver,
		Roots:        []string{h.root},
		Suppressions: map[string]string{"broken": suppression},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	options := valgrind.DefaultOptions()
	options.SuppressionsProfile = "broken"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	analysis, err := service.Start(ctx, valgrind.Request{TargetID: "fixture", Options: &options})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	report, err := analysis.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if report.Result == diagnostic.ResultNoErrorsObserved {
		t.Fatal("a broken suppression file was reported as no_errors_observed")
	}
	if report.AnalysisComplete {
		t.Error("AnalysisComplete = true after Valgrind rejected the suppression file")
	}
	if report.Result != diagnostic.ResultIncomplete && report.Result != diagnostic.ResultToolError {
		t.Errorf("Result = %q, want incomplete or tool_error", report.Result)
	}
	dir, err := h.store.Path(analysis.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "analysis.json")); err != nil {
		t.Errorf("analysis.json missing after a tool failure: %v", err)
	}
}

func TestMemcheckCloseCancelsActiveAnalysis(t *testing.T) {
	h := newMemcheckHarness(t, "hang", 30*time.Second, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	analysis, err := h.service.Start(ctx, valgrind.Request{TargetID: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		h.service.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Service.Close() did not return after cancelling the active analysis")
	}

	report, err := analysis.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete {
		t.Errorf("Result=%q complete=%t, want an incomplete analysis", report.Result, report.AnalysisComplete)
	}
}
