package valgrind

import (
	"context"
	"encoding/json"
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
)

func completedSnapshot() jobs.Snapshot {
	return jobs.Snapshot{
		State:  jobs.StateCompleted,
		Result: &jobs.Result{Started: true, ExitCode: ErrorExitCode},
	}
}

func goodXML(errors int) *XMLResult {
	return &XMLResult{
		ProtocolTool: "memcheck",
		Tool:         "memcheck",
		Complete:     true,
		ErrorSummary: &XMLErrorSummary{Errors: errors},
	}
}

func TestClassifyAnalysis(t *testing.T) {
	cases := []struct {
		name          string
		snapshot      jobs.Snapshot
		xml           *XMLResult
		parseErr      error
		adoptErr      error
		rawTruncated  bool
		buildComplete bool
		want          diagnostic.Result
		wantComplete  bool
	}{
		{"errors", completedSnapshot(), goodXML(1), nil, nil, false, true, diagnostic.ResultErrorsFound, true},
		{"no errors", completedSnapshot(), goodXML(0), nil, nil, false, true, diagnostic.ResultNoErrorsObserved, true},
		{"exit 42 without errors", completedSnapshot(), goodXML(0), nil, nil, false, true, diagnostic.ResultNoErrorsObserved, true},
		{"missing result", jobs.Snapshot{State: jobs.StateFailed}, goodXML(0), nil, nil, false, true, diagnostic.ResultToolError, false},
		{"not started", completedSnapshot(), goodXML(0), nil, nil, false, true, diagnostic.ResultToolError, false},
		{"cancelled", jobs.Snapshot{State: jobs.StateCancelled, Result: &jobs.Result{Started: true, Canceled: true}}, goodXML(1), nil, nil, false, true, diagnostic.ResultIncomplete, false},
		{"timed out", jobs.Snapshot{State: jobs.StateTimedOut, Result: &jobs.Result{Started: true, TimedOut: true}}, goodXML(1), nil, nil, false, true, diagnostic.ResultIncomplete, false},
		{"failed", jobs.Snapshot{State: jobs.StateFailed, Result: &jobs.Result{Started: true}}, goodXML(1), nil, nil, false, true, diagnostic.ResultToolError, false},
		{"no xml", completedSnapshot(), nil, nil, errors.New("missing"), false, true, diagnostic.ResultToolError, false},
		{"protocol mismatch", completedSnapshot(), &XMLResult{ProtocolTool: "helgrind", Tool: "helgrind", ErrorSummary: &XMLErrorSummary{}}, nil, nil, false, true, diagnostic.ResultToolError, false},
		{"parse error", completedSnapshot(), goodXML(1), errors.New("truncated"), nil, false, true, diagnostic.ResultIncomplete, false},
		{"raw truncated", completedSnapshot(), goodXML(1), nil, nil, true, true, diagnostic.ResultIncomplete, false},
		{"missing summary", completedSnapshot(), &XMLResult{ProtocolTool: "memcheck", Tool: "memcheck"}, nil, nil, false, true, diagnostic.ResultIncomplete, false},
		{"build incomplete", completedSnapshot(), goodXML(1), nil, nil, false, false, diagnostic.ResultIncomplete, false},
		{"output truncated", jobs.Snapshot{State: jobs.StateCompleted, Result: &jobs.Result{Started: true, StdoutTruncated: true}}, goodXML(1), nil, nil, false, true, diagnostic.ResultIncomplete, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "not started" {
				test.snapshot.Result.Started = false
			}
			if test.name == "missing result" {
				test.snapshot.Result = nil
			}
			result, complete, _ := classifyAnalysis(test.snapshot, test.xml, test.parseErr, test.adoptErr, test.rawTruncated, test.buildComplete)
			if result != test.want || complete != test.wantComplete {
				t.Errorf("classifyAnalysis() = %q/%t, want %q/%t", result, complete, test.want, test.wantComplete)
			}
		})
	}
}

func TestPhaseFromSnapshot(t *testing.T) {
	cases := map[jobs.State]Phase{
		jobs.StateCompleted: PhaseCompleted,
		jobs.StateCancelled: PhaseCancelled,
		jobs.StateTimedOut:  PhaseTimedOut,
		jobs.StateFailed:    PhaseFailed,
	}
	for state, want := range cases {
		if got := phaseFromSnapshot(jobs.Snapshot{State: state}); got != want {
			t.Errorf("phaseFromSnapshot(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWriteReportFallsBackToCompact(t *testing.T) {
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	log, err := run.OpenWriter("stdout.log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte(strings.Repeat("x", 3000))); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	report := diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            run.ID(),
		Tool:             diagnostic.Tool{Name: "valgrind"},
		Result:           diagnostic.ResultErrorsFound,
		AnalysisComplete: true,
		Program:          diagnostic.Program{TargetID: "fixture", Path: "/ws/fixture", Args: []string{}},
		Summary:          diagnostic.Summary{Total: 1, DiagnosticRecords: 1, ByKind: map[string]int{"InvalidRead": 1}},
		Diagnostics: []diagnostic.Diagnostic{{
			ID:                   "diag_1",
			Fingerprint:          "sha256:abc",
			FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
			Origin:               Origin,
			Kind:                 "InvalidRead",
			Severity:             diagnostic.SeverityError,
			Message:              strings.Repeat("m", 3000),
			Occurrences:          1,
			Confidence:           diagnostic.ConfidenceToolReported,
		}},
		Artifacts: []diagnostic.Artifact{},
		Warnings:  []string{},
	}
	if err := writeReport(run, &report); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}
	if report.Result != diagnostic.ResultIncomplete || report.AnalysisComplete {
		t.Errorf("Result=%q complete=%t, want incomplete/false", report.Result, report.AnalysisComplete)
	}
	if !report.Summary.Truncated || len(report.Diagnostics) != 0 {
		t.Errorf("Summary=%+v diagnostics=%d, want a compact report", report.Summary, len(report.Diagnostics))
	}

	file, err := run.OpenRead(AnalysisArtifactName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var stored diagnostic.Report
	if err := json.NewDecoder(file).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Result != diagnostic.ResultIncomplete || len(stored.Diagnostics) != 0 {
		t.Errorf("stored = %+v", stored)
	}
	if !strings.Contains(strings.Join(stored.Warnings, " "), "quota") {
		t.Errorf("stored warnings = %v, want a quota warning", stored.Warnings)
	}
}

func TestResolveExecutable(t *testing.T) {
	if _, err := resolveExecutable("valgrind"); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("relative path error = %v", err)
	}
	if _, err := resolveExecutable(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("missing path error = %v", err)
	}

	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(regular); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("non-executable error = %v", err)
	}
	if err := os.Chmod(regular, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveExecutable(regular)
	if err != nil || resolved != regular {
		t.Errorf("resolveExecutable() = %q, %v", resolved, err)
	}
}

func TestResolveSuppression(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	valid := filepath.Join(root, "addr4.supp")
	if err := os.WriteFile(valid, []byte("{ x\n Memcheck:Addr4\n fun:main\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{roots: []string{root}, suppressions: map[string]string{
		"addr4":  valid,
		"escape": filepath.Join(outside, "escape.supp"),
		"dir":    root,
	}}

	if path, err := service.resolveSuppression(""); err != nil || path != "" {
		t.Errorf("empty profile = %q, %v", path, err)
	}
	path, err := service.resolveSuppression("addr4")
	if err != nil || path != valid {
		t.Errorf("valid profile = %q, %v", path, err)
	}
	if _, err := service.resolveSuppression("missing"); err == nil {
		t.Error("resolveSuppression() accepted an unknown profile")
	} else {
		var optionErr *OptionError
		if !errors.As(err, &optionErr) {
			t.Errorf("error = %T %v, want OptionError", err, err)
		}
	}
	if _, err := service.resolveSuppression("escape"); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("outside roots error = %v", err)
	}
	if _, err := service.resolveSuppression("dir"); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("directory error = %v", err)
	}

	// A symlink inside the root that resolves outside must be rejected even
	// though the configured path is lexically contained.
	link := filepath.Join(root, "link.supp")
	target := filepath.Join(outside, "target.supp")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	service.suppressions["link"] = link
	if _, err := service.resolveSuppression("link"); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("symlink escape error = %v", err)
	}

	large := filepath.Join(root, "large.supp")
	if err := os.WriteFile(large, make([]byte, config.MaxSuppressionFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	service.suppressions["large"] = large
	if _, err := service.resolveSuppression("large"); !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("oversized suppression error = %v", err)
	}
}

func TestNewServiceValidation(t *testing.T) {
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{t.TempDir()}})

	cases := []struct {
		name string
		opts ServiceOptions
	}{
		{"nil store", ServiceOptions{Manager: manager, Resolver: resolver, Binary: "/bin/true"}},
		{"nil manager", ServiceOptions{Store: store, Resolver: resolver, Binary: "/bin/true"}},
		{"nil resolver", ServiceOptions{Store: store, Manager: manager, Binary: "/bin/true"}},
		{"relative binary", ServiceOptions{Store: store, Manager: manager, Resolver: resolver, Binary: "valgrind"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewService(test.opts); err == nil {
				t.Fatal("NewService() accepted invalid wiring")
			}
		})
	}

	service, err := NewService(ServiceOptions{
		Binary:         "/bin/true",
		Store:          store,
		Manager:        manager,
		Resolver:       resolver,
		MaxRawXMLBytes: 8 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.maxRawXMLBytes != 1<<20 {
		t.Errorf("maxRawXMLBytes = %d, want clamped to the store quota", service.maxRawXMLBytes)
	}
	if service.parseOptions.MaxBytes != 1<<20 {
		t.Errorf("parseOptions.MaxBytes = %d, want clamped", service.parseOptions.MaxBytes)
	}
	service.Close()
}

func TestBackendErrorSemantics(t *testing.T) {
	base := errors.New("permission denied")
	err := &BackendError{Reason: "cannot use backend", Err: base}
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Error("BackendError does not match ErrBackendUnavailable")
	}
	if !errors.Is(err, base) {
		t.Error("BackendError does not unwrap its cause")
	}
	if !strings.Contains(err.Error(), "cannot use backend") || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Error() = %q", err.Error())
	}
	if got := (&BackendError{Reason: "plain"}).Error(); got != "valgrind: plain" {
		t.Errorf("Error() = %q", got)
	}
}

func TestFormatTime(t *testing.T) {
	if got := formatTime(nil); got != "" {
		t.Errorf("formatTime(nil) = %q", got)
	}
	zero := time.Time{}
	if got := formatTime(&zero); got != "" {
		t.Errorf("formatTime(zero) = %q", got)
	}
	value := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if got := formatTime(&value); got != "2026-10-18T12:00:00Z" {
		t.Errorf("formatTime() = %q", got)
	}
}

func TestHashHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("hashFile() = %q", hash)
	}
	if _, err := hashFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("hashFile() accepted a missing file")
	}

	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := run.WriteFileAtomic("data", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if runHash, err := hashRunFile(run, "data"); err != nil || runHash != hash {
		t.Errorf("hashRunFile() = %q, %v", runHash, err)
	}
}

func TestNewServiceUsesResolverRoots(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	resolver := policy.New(&config.Config{WorkspaceRoots: []string{root}})
	service, err := NewService(ServiceOptions{
		Binary: "/bin/true", Store: store, Manager: manager, Resolver: resolver,
		Suppressions: map[string]string{"addr4": "/tmp/x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if len(service.roots) != 1 || service.roots[0] != root {
		t.Errorf("roots = %v, want the resolver root", service.roots)
	}
	if service.suppressions["addr4"] != "/tmp/x" {
		t.Errorf("suppressions were not copied")
	}
}

func TestWriteReportNormalPath(t *testing.T) {
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	report := diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            run.ID(),
		Tool:             diagnostic.Tool{Name: "valgrind"},
		Result:           diagnostic.ResultNoErrorsObserved,
		AnalysisComplete: true,
		Program:          diagnostic.Program{TargetID: "fixture", Path: "/ws/fixture", Args: []string{}},
		Summary:          diagnostic.Summary{Total: 0, DiagnosticRecords: 0, ByKind: map[string]int{}, BySeverity: map[string]int{}},
		Diagnostics:      []diagnostic.Diagnostic{},
		Artifacts:        []diagnostic.Artifact{},
		Warnings:         []string{},
	}
	if err := writeReport(run, &report); err != nil {
		t.Fatalf("writeReport() error = %v", err)
	}
	if report.Result != diagnostic.ResultNoErrorsObserved || !report.AnalysisComplete {
		t.Errorf("report mutated: %+v", report)
	}
	file, err := run.OpenRead(AnalysisArtifactName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var stored diagnostic.Report
	if err := json.NewDecoder(file).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.RunID != report.RunID || stored.Result != report.Result {
		t.Errorf("stored = %+v", stored)
	}
}

func TestAnalysisWaitAndErrorString(t *testing.T) {
	if got := errorString(nil); got != "" {
		t.Errorf("errorString(nil) = %q", got)
	}
	if got := errorString(errors.New("boom")); got != "boom" {
		t.Errorf("errorString() = %q", got)
	}

	analysis := &Analysis{done: make(chan struct{}), phase: PhaseRunning}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analysis.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait(cancelled) error = %v", err)
	}
	close(analysis.done)
	if report, err := analysis.Wait(context.Background()); report != nil || err != nil {
		t.Errorf("Wait(done) = %+v, %v", report, err)
	}
}

func TestWithinAnyRoot(t *testing.T) {
	root := "/home/dev/project"
	cases := []struct {
		path string
		want bool
	}{
		{"/home/dev/project", true},
		{"/home/dev/project/src/main.c", true},
		{"/home/dev/project-other/main.c", false},
		{"/home/dev/other", false},
	}
	for _, test := range cases {
		if got := withinAnyRoot([]string{root}, test.path); got != test.want {
			t.Errorf("withinAnyRoot(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}

func TestResolveExecutableSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveExecutable(link)
	if err != nil || resolved != target {
		t.Errorf("resolveExecutable(symlink) = %q, %v", resolved, err)
	}
}

func TestServiceGetAndCloseAreSafe(t *testing.T) {
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	service, err := NewService(ServiceOptions{
		Binary: "/bin/true", Store: store, Manager: manager,
		Resolver: policy.New(&config.Config{WorkspaceRoots: []string{t.TempDir()}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := service.Get("run_0123456789abcdef0123456789abcdef"); ok {
		t.Error("Get() returned an unknown analysis")
	}
	service.Close()
	service.Close()
}

func hashRunFile(runDir *artifacts.RunDir, name string) (string, error) {
	file, err := runDir.OpenRead(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	return hashReader(file)
}
