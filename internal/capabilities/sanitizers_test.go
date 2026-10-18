package capabilities

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

func TestSanitizerProbeRequiresDeclaredTarget(t *testing.T) {
	disabled := NewSanitizerProbe(false, nil, "asan").Run(context.Background())
	if disabled.State != StateDisabled {
		t.Errorf("disabled = %+v", disabled)
	}
	emptyResolver := policy.New(&config.Config{AllowedTargets: []config.Target{{ID: "plain"}}})
	missing := NewSanitizerProbe(true, emptyResolver, "asan").Run(context.Background())
	if missing.State != StateUnavailable || !strings.Contains(missing.Reason, "declares") {
		t.Errorf("missing = %+v", missing)
	}
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root, Sanitizers: []string{"tsan", "asan"},
		}},
	})
	available := NewSanitizerProbe(true, resolver, "asan").Run(context.Background())
	if available.State != StateAvailable {
		t.Fatalf("available = %+v", available)
	}
	if !strings.Contains(available.Reason, "asan") {
		t.Errorf("reason = %q", available.Reason)
	}
	tsan := NewSanitizerProbe(true, resolver, "tsan").Run(context.Background())
	if tsan.State != StateAvailable {
		t.Errorf("tsan = %+v", tsan)
	}
	ubsan := NewSanitizerProbe(true, resolver, "ubsan").Run(context.Background())
	if ubsan.State != StateUnavailable {
		t.Errorf("ubsan = %+v", ubsan)
	}
	if !strings.Contains(available.Detail, "trusted") {
		t.Errorf("detail = %q, want the declaration limitation", available.Detail)
	}
}

func TestSanitizerProbeIgnoresUnusableTargets(t *testing.T) {
	root := t.TempDir()
	probe := NewSanitizerProbe(true, policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "missing", Program: filepath.Join(root, "missing"), Cwd: root,
			Sanitizers: []string{"asan"},
		}},
	}), "asan")
	result := probe.Run(context.Background())
	if result.State != StateUnavailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestConfigProbesExposeOnlyDeclaredSanitizers(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		Backends:       config.Backends{Sanitizers: true},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: program, Cwd: root, Sanitizers: []string{"asan"},
		}},
	}
	registry := NewRegistry(NewConfigProbes(cfg), 0)
	records := registry.Refresh(context.Background())
	states := make(map[string]State)
	for _, record := range records {
		states[record.ID] = record.State
	}
	if states[BackendSanitizerASan] != StateAvailable {
		t.Errorf("asan = %q", states[BackendSanitizerASan])
	}
	for _, id := range []string{BackendSanitizerUBSan, BackendSanitizerLSan, BackendSanitizerTSan} {
		if states[id] != StateUnavailable {
			t.Errorf("%s = %q, want unavailable", id, states[id])
		}
	}
}
