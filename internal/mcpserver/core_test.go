package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/core"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

func newCoreServer(t *testing.T, registry *capabilities.Registry) *Server {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "target")
	corePath := filepath.Join(root, "core")
	for _, path := range []string{executable, corePath} {
		if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service, err := core.NewService(core.ServiceOptions{
		Runner: runner.New(),
		Binary: func() (string, string, error) { return "/usr/bin/gdb", "gdb", nil },
		Profiles: []config.CoreProfile{{
			ID: "fixture", Executable: executable, Core: corePath,
		}},
		Roots: []string{root}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range CoreDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	return server
}

func TestCoreToolPublicationAndSchema(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendGDB, Kind: "backend", Backend: "gdb",
		State: capabilities.StateUnavailable, Reason: "gdb missing",
	}); err != nil {
		t.Fatal(err)
	}
	server := newCoreServer(t, registry)
	session, ctx := connect(t, server, nil)
	if contains(listToolNames(t, session, ctx), "core_analyze") {
		t.Fatal("core_analyze is published without GDB")
	}
	if !contains(listToolNames(t, session, ctx), "core_artifact") {
		t.Fatal("core_artifact evidence tool is not published")
	}
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendGDB, Kind: "backend", Backend: "gdb",
		State: capabilities.StateAvailable, Path: "/usr/bin/gdb",
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
		if tool.Name == "core_analyze" {
			found = tool
		}
	}
	if found == nil {
		t.Fatal("core_analyze is not published")
	}
	schema, ok := found.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("core schema = %T", found.InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("core properties = %T", schema["properties"])
	}
	if _, ok := properties["profile_id"]; !ok {
		t.Error("core schema is missing profile_id")
	}
}
