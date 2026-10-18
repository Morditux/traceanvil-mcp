package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func TestPurgeCommandDryRunAndApply(t *testing.T) {
	workspace := t.TempDir()
	reportDir := filepath.Join(t.TempDir(), "reports")
	store, err := artifacts.NewStore(reportDir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	oldRun, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	keepRun, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []*artifacts.RunDir{oldRun, keepRun} {
		if err := run.WriteFileAtomic("result.json", []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldRun.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [],
		"storage": {"max_age_days": 1, "max_runs": 16, "max_bytes": 8388608},
		"limits": {}
	}`, config.SchemaVersion, workspace, reportDir)
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"purge", "--config", configPath, "--dry-run"}, nil, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("dry-run exit = %d, stderr = %s", code, stderr.String())
	}
	var dry artifacts.PurgeResult
	if err := json.Unmarshal(stdout.Bytes(), &dry); err != nil {
		t.Fatalf("dry-run output = %q: %v", stdout.String(), err)
	}
	if !dry.DryRun || len(dry.Removed) != 1 || !store.RunExists(oldRun.ID()) {
		t.Fatalf("dry result = %+v", dry)
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"purge", "--config", configPath}, nil, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("purge exit = %d, stderr = %s", code, stderr.String())
	}
	if store.RunExists(oldRun.ID()) {
		t.Fatal("expired run was not removed")
	}
	if !store.RunExists(keepRun.ID()) {
		t.Fatal("recent run was removed")
	}
}

func TestPurgeRefusesLockedReportDir(t *testing.T) {
	workspace := t.TempDir()
	reportDir := filepath.Join(t.TempDir(), "reports")
	store, err := artifacts.NewStore(reportDir, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireLock(); err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseLock()
	configPath := filepath.Join(t.TempDir(), "config.json")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [],
		"limits": {}
	}`, config.SchemaVersion, workspace, reportDir)
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"purge", "--config", configPath, "--dry-run"}, nil, &stdout, &stderr); code != ExitFailure {
		t.Fatalf("locked purge exit = %d, stdout = %s", code, stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("locked")) {
		t.Fatalf("stderr = %s", stderr.String())
	}
}
