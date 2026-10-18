package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptChargesAndSecuresFile(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.Path(), "memcheck.xml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	adopted, err := run.Adopt("memcheck.xml", 512)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted.Bytes != 100 || adopted.Truncated {
		t.Errorf("Adopt() = %+v, want 100 bytes without truncation", adopted)
	}
	if run.Written() != 100 {
		t.Errorf("Written() = %d, want 100", run.Written())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestAdoptTruncatesToLimitAndQuota(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 50)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.Path(), "memcheck.xml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	adopted, err := run.Adopt("memcheck.xml", 1000)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if !adopted.Truncated || adopted.Bytes != 50 {
		t.Errorf("Adopt() = %+v, want truncation to the 50-byte quota", adopted)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 50 {
		t.Errorf("on-disk size = %d, want 50", info.Size())
	}

	other := filepath.Join(run.Path(), "other.xml")
	if err := os.WriteFile(other, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	adopted, err = run.Adopt("other.xml", 10)
	if err != nil {
		t.Fatalf("Adopt() error = %v", err)
	}
	if adopted.Bytes != 0 || !adopted.Truncated {
		t.Errorf("Adopt() with exhausted quota = %+v, want a truncated empty file", adopted)
	}
}

func TestAdoptRejectsSymlinkAndMissingFile(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := run.Adopt("missing.xml", 100); err == nil {
		t.Error("Adopt() accepted a missing file")
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(run.Path(), "link.xml")); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Adopt("link.xml", 100); err == nil {
		t.Error("Adopt() followed a symlink")
	}
}

func TestAdoptRejectsDirectoryAndBadName(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(run.Path(), "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Adopt("directory", 100); err == nil {
		t.Error("Adopt() accepted a directory")
	}
	if _, err := run.Adopt("../escape", 100); err == nil {
		t.Error("Adopt() accepted a traversal name")
	}
}

func TestAdoptFileQuota(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DefaultMaxFilesPerRun; i++ {
		if err := run.WriteFileAtomic(fileName(i), []byte("x")); err != nil {
			t.Fatalf("write file %d: %v", i, err)
		}
	}
	path := filepath.Join(run.Path(), "extra.xml")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run.Adopt("extra.xml", 100); !errors.Is(err, ErrFileQuota) {
		t.Errorf("Adopt() error = %v, want ErrFileQuota", err)
	}
}

func fileName(i int) string {
	return fmt.Sprintf("file-%02d.log", i)
}

func TestStoreOwns(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	store, err := NewStore(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !store.Owns(run.Path()) {
		t.Errorf("Owns(%q) = false, want true", run.Path())
	}
	for _, path := range []string{
		root,
		filepath.Join(root, "run_nothex"),
		filepath.Join(root, "sub", run.ID()),
		filepath.Join(t.TempDir(), run.ID()),
		"",
	} {
		if store.Owns(path) {
			t.Errorf("Owns(%q) = true, want false", path)
		}
	}
}

func TestAdoptEmptyFileIsCountedOnce(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path(), "memcheck.xml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := run.Adopt("memcheck.xml", 0); err != nil {
			t.Fatalf("Adopt() #%d error = %v", i, err)
		}
	}
	run.mu.Lock()
	files := run.files
	run.mu.Unlock()
	if files != 1 {
		t.Errorf("files = %d, want 1", files)
	}
}

func TestAdoptNegativeLimitKeepsNothing(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path(), "memcheck.xml"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	adopted, err := run.Adopt("memcheck.xml", -1)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Bytes != 0 || !adopted.Truncated {
		t.Errorf("Adopt() = %+v, want 0 bytes truncated", adopted)
	}
}
