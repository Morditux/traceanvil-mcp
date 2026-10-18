package perf

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

func TestRunPerfWithFixedFakePerf(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakePerf := filepath.Join(root, "fake-perf")
	if err := os.WriteFile(fakePerf, []byte(`#!/bin/sh
out="$4"
printf '%s\n' '{"event":"task-clock","counter-value":"3.5","unit":"msec"}' '{"event":"cycles","counter-value":"1000"}' '{"event":"instructions","counter-value":"700"}' > "$out"
printf '%s\n' '{"event":"cycles","counter-value":"999999"}'
exit 0
`), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: target, Cwd: root, AllowedArgs: []string{"--ok"},
		}},
	})
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		BinaryProvider: func() (string, string, error) { return fakePerf, "perf test", nil },
		MaxTimeout:     10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background(), Request{TargetID: "fixture", Args: []string{"--ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Experimental || !report.Complete {
		t.Fatalf("report = %+v", report)
	}
	if report.Metrics["task_clock"] != 3.5 || report.Metrics["cycles"] != 1000 || report.Metrics["instructions"] != 700 {
		t.Fatalf("metrics = %+v", report.Metrics)
	}
	artifact, err := service.Artifact(context.Background(), ArtifactRequest{
		RunID: report.RunID, FileName: "stdout.log", MaxBytes: 64,
	})
	if err != nil || artifact.Content == "" {
		t.Fatalf("artifact = %+v, %v", artifact, err)
	}
}

func TestPerfTimeoutAndTargetErrors(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakePerf := filepath.Join(root, "fake-perf")
	if err := os.WriteFile(fakePerf, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	resolver := policy.New(&config.Config{
		WorkspaceRoots: []string{root},
		AllowedTargets: []config.Target{{
			ID: "fixture", Program: target, Cwd: root, AllowedArgs: []string{},
		}},
	})
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		BinaryProvider: func() (string, string, error) { return fakePerf, "test", nil },
		MaxTimeout:     time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background(), Request{TargetID: "fixture", TimeoutMS: 50})
	if err != nil {
		t.Fatalf("timeout Run() error = %v", err)
	}
	if report.Complete || !report.TimedOut {
		t.Fatalf("timeout report = %+v", report)
	}
	if _, err := service.Run(context.Background(), Request{TargetID: "missing"}); CodeOf(err) != CodeTargetUnknown {
		t.Fatalf("unknown target error = %v", err)
	}
	if _, err := service.Run(context.Background(), Request{TargetID: "fixture", TimeoutMS: 5000}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("timeout over maximum error = %v", err)
	}
}
