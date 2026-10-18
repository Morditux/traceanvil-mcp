package mcpserver

import (
	"os"
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

func newCTestServer(t *testing.T, registry *capabilities.Registry) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CTestTestfile.cmake"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(t.TempDir(), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ctest.NewService(ctest.ServiceOptions{
		Store: store, Manager: manager, Runner: runner.New(),
		Binary: func() (string, string, error) { return "/usr/bin/ctest", "ctest", nil },
		Projects: []config.CTestProject{{
			ID: "fixture", TestDir: root, AllowedTests: []string{"pass"},
		}},
		DefaultTestTimeout: time.Second, MaxTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		manager.Close()
	})
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range CTestDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	return server
}

func TestCTestToolPublicationFollowsCapability(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendCTest, Kind: "backend", Backend: "ctest",
		State: capabilities.StateUnavailable, Reason: "not installed",
	}); err != nil {
		t.Fatal(err)
	}
	server := newCTestServer(t, registry)
	session, ctx := connect(t, server, nil)
	names := listToolNames(t, session, ctx)
	for _, name := range []string{"ctest_discover", "ctest_run"} {
		if contains(names, name) {
			t.Errorf("%s is published without an available CTest capability", name)
		}
	}
	for _, name := range []string{"ctest_status", "ctest_cancel", "ctest_report", "ctest_artifact"} {
		if !contains(names, name) {
			t.Errorf("evidence tool %q is missing", name)
		}
	}

	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendCTest, Kind: "backend", Backend: "ctest",
		State: capabilities.StateAvailable, Path: "/usr/bin/ctest",
	}); err != nil {
		t.Fatal(err)
	}
	server.SyncTools()
	names = listToolNames(t, session, ctx)
	for _, name := range CTestToolNames() {
		if !contains(names, name) {
			t.Errorf("available CTest tools = %v, missing %q", names, name)
		}
	}
}

func TestCTestRunAndDiscoverySchemas(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendCTest, Kind: "backend", Backend: "ctest",
		State: capabilities.StateAvailable, Path: "/usr/bin/ctest",
	}); err != nil {
		t.Fatal(err)
	}
	server := newCTestServer(t, registry)
	session, ctx := connect(t, server, nil)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*mcp.Tool)
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	for name, fields := range map[string][]string{
		"ctest_discover": {"project_id"},
		"ctest_run":      {"project_id", "tests", "profile", "timeout_ms", "test_timeout_ms"},
		"ctest_artifact": {"run_id", "artifact_type", "offset", "max_bytes"},
		"ctest_report":   {"run_id", "limit"},
	} {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s input schema = %T", name, tool.InputSchema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %T", name, schema["properties"])
		}
		for _, field := range fields {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s schema missing %q", name, field)
			}
		}
	}
}
