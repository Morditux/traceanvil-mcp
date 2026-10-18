package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/sanitizer"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func buildSanitizerFixture(t *testing.T, root, source string, flags ...string) string {
	t.Helper()
	cc := requireTool(t, "cc")
	src := filepath.Join("..", "..", "testdata", "fixtures", "c", source+".c")
	out := filepath.Join(root, source)
	args := append([]string{"-g", "-O0", "-fno-omit-frame-pointer"}, flags...)
	args = append(args, "-o", out, src)
	cmd := exec.Command(cc, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("sanitizer fixture %s could not be compiled in this environment: %v\n%s", source, err, output)
	}
	return out
}

func TestSanitizerRuns(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		flags    []string
		tool     sanitizer.Tool
		kindPart string
	}{
		{"asan", "sanitizer_asan", []string{"-fsanitize=address"}, sanitizer.ToolASan, "AddressSanitizer"},
		{"ubsan", "sanitizer_ubsan", []string{"-fsanitize=undefined"}, sanitizer.ToolUBSan, "UndefinedBehavior"},
		{"lsan", "sanitizer_lsan", []string{"-fsanitize=leak"}, sanitizer.ToolLSan, "Leak_"},
		{"tsan", "sanitizer_tsan", []string{"-fsanitize=thread", "-pthread"}, sanitizer.ToolTSan, "DataRace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			program := buildSanitizerFixture(t, root, test.source, test.flags...)
			resolver := policy.New(&config.Config{
				WorkspaceRoots: []string{root},
				AllowedTargets: []config.Target{{
					ID: "fixture", Program: program, Cwd: root,
					Sanitizers: []string{string(test.tool)},
				}},
			})
			store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 20 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			service, err := sanitizer.NewService(sanitizer.ServiceOptions{
				Store: store, Manager: manager, Resolver: resolver, Roots: resolver.ResolvedRoots(),
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err := service.Start(context.Background(), valgrind.Request{
				TargetID: "fixture", Tool: valgrind.Tool(test.tool),
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			report, err := run.Wait(context.Background())
			if err != nil {
				t.Fatalf("Wait() error = %v", err)
			}
			if report.Result != diagnostic.ResultErrorsFound || len(report.Diagnostics) == 0 {
				if test.tool == sanitizer.ToolTSan && sanitizerRuntimeBlocked(t, store, run.ID()) {
					t.Skip("ThreadSanitizer runtime is restricted in this environment")
				}
				t.Fatalf("report = %+v (warnings: %v)", report, report.Warnings)
			}
			if !report.AnalysisComplete {
				t.Errorf("analysis incomplete: %v", report.Warnings)
				if file, err := store.OpenArtifact(run.ID(), sanitizer.StderrArtifactName); err == nil {
					defer file.Close()
					buf := make([]byte, 8192)
					n, _ := file.Read(buf)
					t.Logf("stderr: %s", buf[:n])
				}
			}
			found := false
			for _, item := range report.Diagnostics {
				if strings.Contains(item.Kind, test.kindPart) {
					found = true
				}
				if item.Origin != test.tool.Origin() {
					t.Errorf("origin = %q", item.Origin)
				}
				if len(item.Stack) == 0 || item.Stack[0].Path == "" {
					t.Errorf("diagnostic has no source frame: %+v", item)
				}
			}
			if !found {
				t.Errorf("no %q diagnostic in %+v", test.kindPart, report.Diagnostics)
			}
			if report.Artifacts == nil || len(report.Artifacts) == 0 || report.Artifacts[0].Name != sanitizer.SanitizerArtifactName {
				t.Errorf("artifacts = %+v", report.Artifacts)
			}
			service.Close()
			manager.Close()
		})
	}
}

func TestSanitizerAnalysisBackendAndReport(t *testing.T) {
	root := t.TempDir()
	program := buildSanitizerFixture(t, root, "sanitizer_asan", "-fsanitize=address")
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root, Sanitizers: []string{"asan"},
		}},
	})
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sanitizerService, err := sanitizer.NewService(sanitizer.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver, Roots: resolver.ResolvedRoots(),
	})
	if err != nil {
		t.Fatal(err)
	}
	analysisService, err := analysis.New(analysis.Options{
		Store: store,
		Backends: map[string]analysis.Backend{
			analysis.ToolASan: analysis.NewSanitizerBackend(sanitizerService),
		},
		BackendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := analysisService.Start(context.Background(), analysis.StartRequest{
		TargetID: "fixture", Tool: analysis.ToolASan,
	})
	if err != nil {
		t.Fatalf("analysis Start() error = %v", err)
	}
	if started.Tool != analysis.ToolASan {
		t.Errorf("tool = %q", started.Tool)
	}
	final := waitForTerminalPhase(t, analysisService, started.RunID)
	if final.State != analysis.StateCompleted || !final.AnalysisComplete {
		t.Fatalf("final = %+v", final)
	}
	if final.Tool != analysis.ToolASan {
		t.Errorf("final tool = %q", final.Tool)
	}
	page, err := analysisService.Report(context.Background(), analysis.ReportRequest{RunID: started.RunID, Limit: 10})
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if page.AnalysisTool != analysis.ToolASan || page.Page.Matched == 0 {
		t.Errorf("page = %+v", page)
	}
	if page.Result != diagnostic.ResultErrorsFound {
		t.Errorf("result = %q", page.Result)
	}
	artifact, err := analysisService.Artifact(context.Background(), analysis.ArtifactRequest{
		RunID: started.RunID, ArtifactType: analysis.ArtifactRawLog, MaxBytes: 300,
	})
	if err != nil {
		t.Fatalf("Artifact(raw_log) error = %v", err)
	}
	if artifact.FileName != sanitizer.SanitizerArtifactName || !strings.Contains(artifact.Content, "AddressSanitizer") {
		t.Errorf("artifact = %+v", artifact)
	}
	analysisService.Close()
	sanitizerService.Close()
	manager.Close()
}

func TestUndeclaredSanitizerTargetIsRejectedBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	// Deliberately do not declare instrumented source: the rejection must happen
	// from configuration, before the target can run.
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{ID: "fixture", Program: program, Cwd: root}},
	})
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sanitizerService, err := sanitizer.NewService(sanitizer.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver, Roots: resolver.ResolvedRoots(),
	})
	if err != nil {
		t.Fatal(err)
	}
	analysisService, err := analysis.New(analysis.Options{
		Store: store,
		Backends: map[string]analysis.Backend{
			analysis.ToolASan: analysis.NewSanitizerBackend(sanitizerService),
		},
		BackendEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = analysisService.Start(context.Background(), analysis.StartRequest{
		TargetID: "fixture", Tool: analysis.ToolASan,
	})
	mustAnalysisCode(t, err, analysis.CodeBackendUnavailable)
	analysisService.Close()
	sanitizerService.Close()
	manager.Close()
}

func mustAnalysisCode(t *testing.T, err error, code analysis.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", code)
	}
	var apiErr *analysis.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want analysis.Error", err, err)
	}
	if apiErr.Code != code {
		t.Fatalf("error code = %s, want %s (%v)", apiErr.Code, code, err)
	}
}

func TestCleanSanitizerRunIsObservationNotProof(t *testing.T) {
	root := t.TempDir()
	program := buildSanitizerFixture(t, root, "sanitizer_clean", "-fsanitize=address")
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root, Sanitizers: []string{"asan"},
		}},
	})
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := sanitizer.NewService(sanitizer.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver, Roots: resolver.ResolvedRoots(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := service.Start(context.Background(), valgrind.Request{
		TargetID: "fixture", Tool: valgrind.Tool(sanitizer.ToolASan),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := run.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != diagnostic.ResultNoErrorsObserved || !report.AnalysisComplete {
		t.Fatalf("report = %+v (warnings: %v)", report, report.Warnings)
	}
	if len(report.Warnings) == 0 || !strings.Contains(strings.Join(report.Warnings, " "), "not proof") {
		t.Errorf("clean run must carry an absence-is-not-proof warning: %v", report.Warnings)
	}
	service.Close()
	manager.Close()
}

func sanitizerRuntimeBlocked(t *testing.T, store *artifacts.Store, runID string) bool {
	t.Helper()
	for _, name := range []string{sanitizer.SanitizerArtifactName, sanitizer.StderrArtifactName} {
		file, err := store.OpenArtifact(runID, name)
		if err != nil {
			continue
		}
		buffer := make([]byte, 4096)
		n, _ := file.Read(buffer)
		_ = file.Close()
		content := string(buffer[:n])
		if strings.Contains(content, "unexpected memory mapping") ||
			strings.Contains(content, "FATAL: ThreadSanitizer") ||
			strings.Contains(content, "ASLR") {
			return true
		}
	}
	return false
}
