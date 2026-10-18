// Package integration exercises policy, runner, artifacts and jobs together
// without exposing an MCP tool.
package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

func TestPolicyToArtifactPipeline(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "fixture")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'hello\\n'\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID:          "fixture",
			Program:     script,
			Cwd:         root,
			AllowedArgs: []string{"--only"},
		}},
	}

	resolved, err := policy.New(cfg).Resolve("fixture", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{
		MaxJobs:        1,
		Timeout:        2 * time.Second,
		MaxOutputBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	job, err := manager.Start(context.Background(), jobs.Spec{
		TargetID:   resolved.TargetID,
		Executable: resolved.Program,
		Args:       resolved.Args,
		Dir:        resolved.Dir,
		Env:        policy.Environment(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := job.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}

	if snapshot.State != jobs.StateCompleted {
		t.Fatalf("State = %q, want completed (error: %s)", snapshot.State, snapshot.Error)
	}
	if snapshot.Result == nil || !snapshot.Result.Started || snapshot.Result.ExitCode != 7 {
		t.Fatalf("Result = %+v, want a started program with exit code 7", snapshot.Result)
	}

	dir, err := store.Path(job.ID())
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var manifest jobs.Snapshot
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	if manifest.SchemaVersion != jobs.SchemaVersion || manifest.State != jobs.StateCompleted {
		t.Errorf("manifest = %+v, want schema 1 and completed", manifest)
	}

	stdout, err := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	if strings.TrimSpace(string(stdout)) != "hello" {
		t.Errorf("stdout = %q, want hello", stdout)
	}
}

func TestJobFailureWritesManifest(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "fixture")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: script, Cwd: root,
		}},
	}
	resolved, err := policy.New(cfg).Resolve("fixture", nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}

	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{
		MaxJobs: 1, Timeout: 2 * time.Second, MaxOutputBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	job, err := manager.Start(context.Background(), jobs.Spec{
		TargetID:   resolved.TargetID,
		Executable: resolved.Program,
		Args:       resolved.Args,
		Dir:        resolved.Dir,
		Env:        policy.Environment(""),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := job.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if snapshot.State != jobs.StateFailed {
		t.Fatalf("State = %q, want failed", snapshot.State)
	}
	if snapshot.Error == "" {
		t.Error("Error is empty, want the start failure")
	}
	if snapshot.Result == nil || snapshot.Result.Started {
		t.Errorf("Result = %+v, want Started=false", snapshot.Result)
	}

	dir, err := store.Path(job.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "result.json")); err != nil {
		t.Errorf("result.json missing after failure: %v", err)
	}
}
