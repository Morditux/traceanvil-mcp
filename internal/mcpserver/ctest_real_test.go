package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/ctest"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

func TestCTestMCPWorkflow(t *testing.T) {
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake is not installed")
	}
	ctestBinary, err := exec.LookPath("ctest")
	if err != nil {
		t.Skip("ctest is not installed")
	}
	if !filepath.IsAbs(cmake) || !filepath.IsAbs(ctestBinary) {
		t.Skip("cmake/ctest resolved through a relative PATH entry")
	}
	root := t.TempDir()
	build := filepath.Join(root, "build")
	configure := exec.Command(cmake, "-S", filepath.Join("..", "..", "testdata", "fixtures", "ctest"), "-B", build)
	configure.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := configure.CombinedOutput(); err != nil {
		t.Skipf("configure CTest fixture: %v\n%s", err, output)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ctest.NewService(ctest.ServiceOptions{
		Store: store, Manager: manager, Runner: runner.New(),
		Binary: func() (string, string, error) { return ctestBinary, "ctest", nil },
		Projects: []config.CTestProject{{
			ID: "fixture", TestDir: build,
			AllowedTests: []string{"pass", "fail"},
		}},
		DefaultTestTimeout: 30 * time.Second, MaxTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		manager.Close()
	})
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendCTest, Kind: "backend", Backend: "ctest",
		State: capabilities.StateAvailable, Path: ctestBinary,
	}); err != nil {
		t.Fatal(err)
	}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range CTestDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	session, ctx := connect(t, server, nil)

	discoverResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ctest_discover", Arguments: map[string]any{"project_id": "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if discoverResult.IsError {
		t.Fatalf("ctest_discover failed: %s", toolText(discoverResult))
	}
	var discovery ctest.Discovery
	decodeCall(t, discoverResult, &discovery)
	if len(discovery.Tests) != 2 || !discovery.Complete {
		t.Fatalf("discovery = %+v", discovery)
	}

	runResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ctest_run",
		Arguments: map[string]any{
			"project_id": "fixture", "tests": []string{"pass", "fail"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runResult.IsError {
		t.Fatalf("ctest_run failed: %s", toolText(runResult))
	}
	var started ctest.Status
	decodeCall(t, runResult, &started)
	if started.RunID == "" {
		t.Fatal("ctest_run returned no run id")
	}

	statusCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		statusResult, err := session.CallTool(statusCtx, &mcp.CallToolParams{
			Name: "ctest_status", Arguments: map[string]any{"run_id": started.RunID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if statusResult.IsError {
			t.Fatalf("ctest_status failed: %s", toolText(statusResult))
		}
		var status ctest.Status
		decodeCall(t, statusResult, &status)
		if status.Phase == ctest.PhaseCompleted || status.Phase == ctest.PhaseFailed || status.Phase == ctest.PhaseCanceled || status.Phase == ctest.PhaseTimedOut {
			if status.Phase != ctest.PhaseCompleted || status.Result != ctest.ResultFailed || !status.Complete {
				t.Fatalf("final status = %+v", status)
			}
			if status.Summary.Passed != 1 || status.Summary.Failed != 1 {
				t.Fatalf("summary = %+v", status.Summary)
			}
			break
		}
		select {
		case <-statusCtx.Done():
			t.Fatal("ctest_status did not complete")
		case <-time.After(25 * time.Millisecond):
		}
	}

	reportResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ctest_report", Arguments: map[string]any{"run_id": started.RunID, "limit": 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reportResult.IsError {
		t.Fatalf("ctest_report failed: %s", toolText(reportResult))
	}
	var report ctest.Report
	decodeCall(t, reportResult, &report)
	if len(report.Tests) != 2 || len(report.Artifacts) == 0 {
		t.Fatalf("report = %+v", report)
	}
}
