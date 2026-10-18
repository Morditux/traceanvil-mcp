package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func writeExecutable(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func testResolver(root string, targets ...config.Target) *Resolver {
	return New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: targets,
	})
}

func TestResolveValidTarget(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	writeExecutable(t, program)

	resolver := testResolver(root, config.Target{
		ID:          "fixture",
		Program:     program,
		Cwd:         root,
		AllowedArgs: []string{"--one", "--two"},
	})

	resolved, err := resolver.Resolve("fixture", []string{"--one"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Program != program {
		t.Errorf("Program = %q, want %q", resolved.Program, program)
	}
	if resolved.Dir != root {
		t.Errorf("Dir = %q, want %q", resolved.Dir, root)
	}
	if len(resolved.Args) != 1 || resolved.Args[0] != "--one" {
		t.Errorf("Args = %v, want [--one]", resolved.Args)
	}
}

func TestResolveAllowsNoArguments(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	writeExecutable(t, program)

	resolver := testResolver(root, config.Target{
		ID: "fixture", Program: program, Cwd: root,
	})

	if _, err := resolver.Resolve("fixture", nil); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveDenials(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	writeExecutable(t, program)

	outside := t.TempDir()
	outsideProgram := filepath.Join(outside, "outside")
	writeExecutable(t, outsideProgram)

	programLink := filepath.Join(root, "program-link")
	if err := os.Symlink(outsideProgram, programLink); err != nil {
		t.Fatal(err)
	}
	cwdLink := filepath.Join(root, "cwd-link")
	if err := os.Symlink(outside, cwdLink); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(root, "plain")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		resolveID string
		target    config.Target
		args      []string
		wantErr   string
	}{
		{
			name:      "unknown target",
			resolveID: "unknown",
			target:    config.Target{ID: "other", Program: program, Cwd: root},
			wantErr:   "unknown target",
		},
		{
			name:    "program symlink escape",
			target:  config.Target{ID: "fixture", Program: programLink, Cwd: root},
			wantErr: "outside workspace roots",
		},
		{
			name:    "cwd symlink escape",
			target:  config.Target{ID: "fixture", Program: program, Cwd: cwdLink},
			wantErr: "outside workspace roots",
		},
		{
			name:    "missing program",
			target:  config.Target{ID: "fixture", Program: filepath.Join(root, "missing"), Cwd: root},
			wantErr: "program is not accessible",
		},
		{
			name:    "plain file",
			target:  config.Target{ID: "fixture", Program: plainFile, Cwd: root},
			wantErr: "not executable",
		},
		{
			name:    "directory program",
			target:  config.Target{ID: "fixture", Program: directory, Cwd: root},
			wantErr: "regular file",
		},
		{
			name:    "argument not allowed",
			target:  config.Target{ID: "fixture", Program: program, Cwd: root, AllowedArgs: []string{"--one"}},
			args:    []string{"--two"},
			wantErr: "not allowed",
		},
		{
			name:    "argument repeated",
			target:  config.Target{ID: "fixture", Program: program, Cwd: root, AllowedArgs: []string{"--one"}},
			args:    []string{"--one", "--one"},
			wantErr: "not allowed",
		},
		{
			name:    "argument with NUL",
			target:  config.Target{ID: "fixture", Program: program, Cwd: root, AllowedArgs: []string{"--one"}},
			args:    []string{"--one\x00"},
			wantErr: "NUL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := testResolver(root, tt.target)

			resolveID := tt.resolveID
			if resolveID == "" {
				resolveID = "fixture"
			}
			_, err := resolver.Resolve(resolveID, tt.args)
			if err == nil {
				t.Fatal("Resolve() error = nil, want a denial")
			}
			var denied *DeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("error type = %T, want *DeniedError (%v)", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveDeniesMissingWorkspaceRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	program := filepath.Join(root, "fixture")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, program)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	resolver := testResolver(root, config.Target{ID: "fixture", Program: program, Cwd: root})
	_, err := resolver.Resolve("fixture", nil)
	if err == nil {
		t.Fatal("Resolve() error = nil, want a denial")
	}
	if !strings.Contains(err.Error(), "no workspace root") {
		t.Errorf("error = %q, want a missing root denial", err)
	}
}

func TestEnvironmentOmitsSecrets(t *testing.T) {
	t.Setenv("TOP_SECRET", "do-not-leak")

	env := Environment("/tmp/run")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "TOP_SECRET") || strings.Contains(joined, "do-not-leak") {
		t.Errorf("Environment() leaked a server secret: %v", env)
	}
	if !strings.Contains(joined, "TMPDIR=/tmp/run") {
		t.Errorf("Environment() = %v, want TMPDIR", env)
	}
}

func TestTargetIDs(t *testing.T) {
	resolver := New(&config.Config{
		WorkspaceRoots: []string{"/tmp/ws"},
		AllowedTargets: []config.Target{
			{ID: "b"},
			{ID: "a"},
		},
	})

	ids := resolver.TargetIDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("TargetIDs() = %v, want [a b]", ids)
	}
}
