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
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/profile"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

func newProfileServer(t *testing.T, registry *capabilities.Registry) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture"), []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: filepath.Join(root, "fixture"), Cwd: root,
		}},
	})
	service, err := profile.NewService(profile.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		BinaryProvider: func() (string, string, error) { return "/usr/bin/valgrind", "test", nil },
		MaxTimeout:     10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range ProfileDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	return server
}

func TestProfileToolPublicationAndSchema(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendValgrind, Kind: "backend", Backend: "valgrind",
		State: capabilities.StateAvailable, Path: "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendValgrindCallgrind, Kind: "backend", Backend: "valgrind",
		State: capabilities.StateUnavailable, Reason: "callgrind missing",
	}); err != nil {
		t.Fatal(err)
	}
	server := newProfileServer(t, registry)
	session, ctx := connect(t, server, nil)
	names := listToolNames(t, session, ctx)
	for _, name := range []string{"profile_callgrind", "profile_massif"} {
		if contains(names, name) {
			t.Errorf("%s is published without its capability", name)
		}
	}
	for _, name := range []string{"profile_compare", "profile_artifact"} {
		if !contains(names, name) {
			t.Errorf("%s is not published for persisted evidence", name)
		}
	}
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendValgrindCallgrind, Kind: "backend", Backend: "valgrind",
		State: capabilities.StateAvailable, Path: "/usr/bin/valgrind",
	}); err != nil {
		t.Fatal(err)
	}
	server.SyncTools()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "profile_callgrind" {
			found = tool
		}
	}
	if found == nil {
		t.Fatal("profile_callgrind is not published")
	}
	schema, ok := found.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("schema = %T", found.InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T", schema["properties"])
	}
	for _, field := range []string{"target_id", "args", "timeout_ms"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("profile_callgrind schema missing %q", field)
		}
	}
	if contains(listToolNames(t, session, ctx), "profile_massif") {
		t.Error("profile_massif is published without its capability")
	}
}
