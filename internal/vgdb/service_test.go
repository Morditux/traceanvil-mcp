package vgdb

import (
	"context"
	"os"
	"os/exec"
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

func TestVgdbFixedCommandsReal(t *testing.T) {
	valgrindPath, err := exec.LookPath("valgrind")
	if err != nil || !filepath.IsAbs(valgrindPath) {
		t.Skip("valgrind is not installed at an absolute path")
	}
	vgdbPath, err := exec.LookPath("vgdb")
	if err != nil || !filepath.IsAbs(vgdbPath) {
		t.Skip("vgdb is not installed at an absolute path")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	source := filepath.Join(root, "fixture.c")
	if err := os.WriteFile(source, []byte("int main(void){ volatile int x=0; for(int i=0;i<10;i++) x+=i; return x == 45 ? 0 : 1; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-o", program, source).CombinedOutput(); err != nil {
		t.Skipf("compile fixture: %v\n%s", err, output)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 16<<20)
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
			ID: "fixture", Program: program, Cwd: root, AllowedArgs: []string{},
		}},
	})
	service, err := NewService(ServiceOptions{
		Store: store, Manager: manager, Resolver: resolver,
		ValgrindProvider: func() (string, string, error) { return valgrindPath, "valgrind", nil },
		VgdbProvider:     func() (string, string, error) { return vgdbPath, "vgdb", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := service.Run(ctx, Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !report.Experimental || !report.Complete {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Commands) != 3 {
		t.Fatalf("commands = %+v", report.Commands)
	}
	if report.Commands[0].Command != "v.info gdbserver_status" ||
		!report.Commands[0].Complete || report.Commands[0].Output == "" {
		t.Fatalf("status command = %+v", report.Commands[0])
	}
	if report.Commands[1].Command != "v.info exectxt" || !report.Commands[1].Complete {
		t.Fatalf("exectxt command = %+v", report.Commands[1])
	}
	if manager.Active() != 0 {
		t.Fatalf("manager still has active jobs: %d", manager.Active())
	}
	runDir, _ := store.Path(report.RunID)
	entries, err := os.ReadDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "vgdb-") {
			t.Errorf("vgdb FIFO was not cleaned: %s", entry.Name())
		}
	}
	artifact, err := service.Artifact(context.Background(), ArtifactRequest{
		RunID: report.RunID, FileName: "vgdb.json", MaxBytes: 128,
	})
	if err != nil || artifact.Content == "" {
		t.Fatalf("artifact = %+v, %v", artifact, err)
	}
}

func TestVgdbUnavailableServerDoesNotHang(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeValgrind := filepath.Join(root, "valgrind")
	if err := os.WriteFile(fakeValgrind, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeVgdb := filepath.Join(root, "vgdb")
	if err := os.WriteFile(fakeVgdb, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 5 * time.Second})
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
		ValgrindProvider: func() (string, string, error) { return fakeValgrind, "test", nil },
		VgdbProvider:     func() (string, string, error) { return fakeVgdb, "test", nil },
		MaxTimeout:       time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := service.Run(ctx, Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Complete || len(report.Commands) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if manager.Active() != 0 {
		t.Fatalf("manager still active: %d", manager.Active())
	}
}

func TestVgdbCommandFailureIsIncomplete(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeValgrind := filepath.Join(root, "valgrind")
	if err := os.WriteFile(fakeValgrind, []byte(`#!/bin/sh
prefix=""
for arg in "$@"; do
  case "$arg" in --vgdb-prefix=*) prefix="${arg#--vgdb-prefix=}";; esac
done
[ -n "$prefix" ] && : > "${prefix}-from-vgdb-to-1-by-user-on-host"
sleep 5
`), 0o700); err != nil {
		t.Fatal(err)
	}
	fakeVgdb := filepath.Join(root, "vgdb")
	if err := os.WriteFile(fakeVgdb, []byte("#!/bin/sh\necho command failed >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := jobs.NewManager(store, runner.New(), jobs.Options{MaxJobs: 1, Timeout: 5 * time.Second})
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
		ValgrindProvider: func() (string, string, error) { return fakeValgrind, "test", nil },
		VgdbProvider:     func() (string, string, error) { return fakeVgdb, "test", nil },
		MaxTimeout:       time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := service.Run(ctx, Request{TargetID: "fixture"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Complete || len(report.Commands) == 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, command := range report.Commands {
		if command.Complete {
			t.Errorf("failed command marked complete: %+v", command)
		}
	}
	if manager.Active() != 0 {
		t.Fatalf("manager still active: %d", manager.Active())
	}
}
