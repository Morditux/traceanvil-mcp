package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPurgeByAgeCountBytesAndDryRun(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var runs []*RunDir
	for i := 0; i < 3; i++ {
		run, err := store.Create()
		if err != nil {
			t.Fatal(err)
		}
		if err := run.WriteFileAtomic("result.json", []byte("0123456789")); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(runs[0].Path(), old, old); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-23 * time.Hour)
	if err := os.Chtimes(runs[1].Path(), older, older); err != nil {
		t.Fatal(err)
	}
	newest := time.Now().Add(-time.Hour)
	if err := os.Chtimes(runs[2].Path(), newest, newest); err != nil {
		t.Fatal(err)
	}
	dry, err := store.Purge(context.Background(), RetentionPolicy{MaxAge: 24 * time.Hour}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || len(dry.Removed) != 1 || dry.Removed[0] != runs[0].ID() {
		t.Fatalf("dry run = %+v", dry)
	}
	if !store.RunExists(runs[0].ID()) {
		t.Fatal("dry run removed a directory")
	}
	result, err := store.Purge(context.Background(), RetentionPolicy{MaxAge: 24 * time.Hour}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || store.RunExists(runs[0].ID()) {
		t.Fatalf("age purge = %+v", result)
	}
	result, err = store.Purge(context.Background(), RetentionPolicy{MaxRuns: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Kept != 1 {
		t.Fatalf("count purge = %+v", result)
	}
	if !store.RunExists(runs[2].ID()) {
		t.Fatalf("newest run was removed: %+v", result)
	}
}

func TestPurgeIgnoresSymlinkAndUnknownEntries(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "run_00000000000000000000000000000001")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-run"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.Purge(context.Background(), RetentionPolicy{MaxRuns: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !store.RunExists(run.ID()) {
		t.Fatal("valid run was removed")
	}
	if info, err := os.Lstat(filepath.Join(root, "run_00000000000000000000000000000001")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink entry was removed or followed")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a warning about the symlink")
	}
}

func TestStoreLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	first, err := NewStore(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AcquireLock(); err != nil {
		t.Fatal(err)
	}
	if err := second.AcquireLock(); err == nil {
		t.Fatal("second store acquired the same report_dir lock")
	}
	first.ReleaseLock()
	if err := second.AcquireLock(); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	second.ReleaseLock()
}

func TestPurgeProtectsRecentIncompleteAndNewest(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := incomplete.WriteFileAtomic("stdout.log", []byte("partial")); err != nil {
		t.Fatal(err)
	}
	large, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := large.WriteFileAtomic("result.json", []byte(strings.Repeat("x", 2048))); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(incomplete.Path(), now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(large.Path(), now, now); err != nil {
		t.Fatal(err)
	}
	result, err := store.Purge(context.Background(), RetentionPolicy{MaxBytes: 1024}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !store.RunExists(incomplete.ID()) {
		t.Fatal("recent incomplete run was purged")
	}
	if !store.RunExists(large.ID()) {
		t.Fatal("newest run was purged despite exceeding the byte bound")
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected over-bound/incomplete warnings")
	}
}
