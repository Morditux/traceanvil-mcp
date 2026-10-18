package mcpserver

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/perf"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
	"github.com/Morditux/traceanvil-mcp/internal/version"
	"github.com/Morditux/traceanvil-mcp/internal/vgdb"
)

func experimentalServices(t *testing.T) (*perf.Service, *vgdb.Service) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture"), []byte("#!/bin/sh\n"), 0o700); err != nil {
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
	perfService, err := perf.NewService(perf.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		BinaryProvider: func() (string, string, error) { return "/usr/bin/perf", "test", nil },
		MaxTimeout:     10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	vgdbService, err := vgdb.NewService(vgdb.ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		ValgrindProvider: func() (string, string, error) { return "/usr/bin/valgrind", "test", nil },
		VgdbProvider:     func() (string, string, error) { return "/usr/bin/vgdb", "test", nil },
		MaxTimeout:       10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return perfService, vgdbService
}

func TestExperimentalToolPublicationAndSchemas(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	perfService, vgdbService := experimentalServices(t)
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range PerfDefinitions(perfService, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	for _, definition := range VgdbDefinitions(vgdbService, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	session, ctx := connect(t, server, nil)
	names := listToolNames(t, session, ctx)
	for _, hidden := range []string{"profile_perf", "vgdb_inspect"} {
		if contains(names, hidden) {
			t.Errorf("%s is published without its capability", hidden)
		}
	}
	for _, visible := range []string{"perf_artifact", "vgdb_artifact"} {
		if !contains(names, visible) {
			t.Errorf("%s is not published for persisted evidence", visible)
		}
	}
	for _, cap := range []capabilities.Capability{
		{ID: capabilities.BackendValgrind, Kind: "backend", Backend: "valgrind", State: capabilities.StateAvailable, Path: "/usr/bin/valgrind"},
		{ID: capabilities.BackendPerf, Kind: "backend", Backend: "perf", State: capabilities.StateAvailable, Path: "/usr/bin/perf"},
		{ID: capabilities.BackendVgdb, Kind: "backend", Backend: "vgdb", State: capabilities.StateAvailable, Path: "/usr/bin/vgdb"},
	} {
		if err := registry.Set(cap); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"profile_perf", "vgdb_inspect"} {
		var found bool
		for _, tool := range tools.Tools {
			if tool.Name != name {
				continue
			}
			found = true
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok {
				t.Fatalf("%s schema = %T", name, tool.InputSchema)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("%s properties = %T", name, schema["properties"])
			}
			for _, field := range []string{"target_id", "args", "timeout_ms"} {
				if _, ok := properties[field]; !ok {
					t.Errorf("%s schema missing %q", name, field)
				}
			}
			if _, ok := properties["command"]; ok {
				t.Errorf("%s exposes a free monitor command", name)
			}
		}
		if !found {
			t.Errorf("%s is not published with all capabilities", name)
		}
	}
}
