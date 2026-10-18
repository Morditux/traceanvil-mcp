package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreCreateAndReadWrite(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "reports"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if store.MaxBytesPerRun() != 4096 {
		t.Errorf("MaxBytesPerRun() = %d, want 4096", store.MaxBytesPerRun())
	}

	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if !runIDPattern.MatchString(run.ID()) {
		t.Errorf("run id = %q, want run_<32 hex>", run.ID())
	}
	info, err := os.Stat(run.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("run dir mode = %o, want 700", perm)
	}

	if err := run.WriteFileAtomic("result.json", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	data, err := run.ReadFile("result.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("content = %q", data)
	}
	if run.Written() != int64(len(data)) {
		t.Errorf("Written() = %d, want %d", run.Written(), len(data))
	}
	path, err := store.Path(run.ID())
	if err != nil || path != run.Path() {
		t.Errorf("Path() = %q, %v; want %q", path, err, run.Path())
	}
}

func TestRunDirClosedWriterAndMissingRead(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	writer, err := run.OpenWriter("out")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Errorf("second Close() error = %v, want nil", err)
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Write() after Close error = %v, want os.ErrClosed", err)
	}

	if _, err := run.ReadFile("missing"); err == nil {
		t.Error("ReadFile(missing) error = nil, want an error")
	}
	if _, err := run.OpenRead("missing"); err == nil {
		t.Error("OpenRead(missing) error = nil, want an error")
	}
}

func TestStoreRejectsUnsafeNames(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"", ".", "..", "../escape", "dir/file", "/absolute", "with space", "é"} {
		t.Run(name, func(t *testing.T) {
			if _, err := run.OpenWriter(name); err == nil {
				t.Errorf("OpenWriter(%q) error = nil, want rejection", name)
			}
			if _, err := run.ReadFile(name); err == nil {
				t.Errorf("ReadFile(%q) error = nil, want rejection", name)
			}
			if err := run.WriteFileAtomic(name, []byte("x")); err == nil {
				t.Errorf("WriteFileAtomic(%q) error = nil, want rejection", name)
			}
		})
	}
}

func TestStoreRejectsSymlinkArtifacts(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(run.Path(), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if _, err := run.OpenRead("link"); err == nil {
		t.Error("OpenRead() followed a symlink, want rejection")
	}
	if _, err := run.OpenWriter("link"); err == nil {
		t.Error("OpenWriter() accepted an existing symlink, want rejection")
	}
}

func TestStoreQuota(t *testing.T) {
	store, err := NewStore(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	writer, err := run.OpenWriter("stdout.log")
	if err != nil {
		t.Fatal(err)
	}
	n, err := writer.Write(make([]byte, 2048))
	if !errors.Is(err, ErrQuota) {
		t.Errorf("Write() error = %v, want ErrQuota", err)
	}
	if n != 1024 {
		t.Errorf("Write() wrote %d bytes, want 1024", n)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if run.Written() != 1024 {
		t.Errorf("Written() = %d, want 1024", run.Written())
	}

	if err := run.WriteFileAtomic("result.json", []byte("x")); !errors.Is(err, ErrQuota) {
		t.Errorf("WriteFileAtomic() error = %v, want ErrQuota", err)
	}
}

func TestWriteFileAtomicLeavesNoTemporaryFile(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	if err := run.WriteFileAtomic("run.json", []byte("data")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(run.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "run.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("run dir entries = %v, want [run.json]", names)
	}
}

func TestStoreRemove(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Remove(run.ID()); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(run.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("run directory still exists: %v", err)
	}

	for _, id := range []string{"", "../x", "run_short"} {
		if err := store.Remove(id); err == nil {
			t.Errorf("Remove(%q) error = nil, want rejection", id)
		}
		if _, err := store.Path(id); err == nil {
			t.Errorf("Path(%q) error = nil, want rejection", id)
		}
	}
}

func TestStoreFileQuota(t *testing.T) {
	store, err := NewStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	for i := range DefaultMaxFilesPerRun {
		writer, err := run.OpenWriter(fmt.Sprintf("file-%d", i))
		if err != nil {
			t.Fatalf("open file %d: %v", i, err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run.OpenWriter("overflow"); !errors.Is(err, ErrFileQuota) {
		t.Errorf("OpenWriter() error = %v, want ErrFileQuota", err)
	}
	if err := run.WriteFileAtomic("new-file", []byte("x")); !errors.Is(err, ErrFileQuota) {
		t.Errorf("WriteFileAtomic(new-file) error = %v, want ErrFileQuota", err)
	}
	// Replacing an existing file does not consume an extra file slot.
	if err := run.WriteFileAtomic("file-0", []byte("replacement")); err != nil {
		t.Errorf("WriteFileAtomic(existing) error = %v, want nil", err)
	}
}

func TestWriteFileAtomicReplacesWithoutExtraFile(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	if err := run.WriteFileAtomic("a", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteFileAtomic("a", []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := run.ReadFile("a")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Errorf("content = %q, want two", data)
	}
	// The replaced content is credited back to the quota.
	if run.Written() != 3 {
		t.Errorf("Written() = %d, want 3", run.Written())
	}
}

func TestStoreCreateAfterRootRemoved(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	store, err := NewStore(root, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(); err == nil {
		t.Fatal("Create() after root removal error = nil, want an error")
	}
}

func TestQuotaWriterSecondWrite(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := run.OpenWriter("out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("12345678")); !errors.Is(err, ErrQuota) {
		t.Fatalf("first Write() error = %v, want ErrQuota", err)
	}
	if n, err := writer.Write([]byte("x")); n != 0 || !errors.Is(err, ErrQuota) {
		t.Errorf("second Write() = %d, %v; want 0, ErrQuota", n, err)
	}
}

func TestReadFileRejectsContentOverQuota(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path(), "large"), []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run.ReadFile("large"); !errors.Is(err, ErrQuota) {
		t.Errorf("ReadFile() error = %v, want ErrQuota", err)
	}
}

func TestWriteFileAtomicRenameFailure(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(run.Path(), "destination"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteFileAtomic("destination", []byte("x")); err == nil {
		t.Error("WriteFileAtomic() over a directory error = nil, want an error")
	}
}

func TestWriteFileAtomicPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(run.Path(), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(run.Path(), 0o700) })

	if err := run.WriteFileAtomic("x", []byte("x")); err == nil {
		t.Error("WriteFileAtomic() in a read-only directory error = nil, want an error")
	}
}

func TestWriteFileAtomicAfterRunDirRemoved(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(run.Path()); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteFileAtomic("x", []byte("x")); err == nil {
		t.Fatal("WriteFileAtomic() after directory removal error = nil, want an error")
	}
}

func TestNewStoreErrors(t *testing.T) {
	if _, err := NewStore("relative", 1024); err == nil {
		t.Error("NewStore(relative) error = nil, want rejection")
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(file, 1024); err == nil {
		t.Error("NewStore(file) error = nil, want rejection")
	}
}

func TestNewStoreDefaultQuota(t *testing.T) {
	store, err := NewStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if store.MaxBytesPerRun() != DefaultMaxBytesPerRun {
		t.Errorf("MaxBytesPerRun() = %d, want %d", store.MaxBytesPerRun(), DefaultMaxBytesPerRun)
	}
}

func TestRandomRunIDsAreUnique(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for range 32 {
		id, err := newRunID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "run_") {
			t.Errorf("id = %q, want run_ prefix", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate run id %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestWriteFileAtomicRejectsOpenStream(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := run.OpenWriter("stdout.log")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := run.WriteFileAtomic("stdout.log", []byte("x")); err == nil {
		t.Error("WriteFileAtomic() on an open stream succeeded, want error")
	}
}

func TestQuotaWriterEmptyWriteAtQuota(t *testing.T) {
	store, err := NewStore(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := run.OpenWriter("out.log")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) = %d, %v, want 0, nil", n, err)
	}
}

func TestNewStoreRootSecurity(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(link, 4096); err == nil {
		t.Error("NewStore(symlink root) succeeded, want error")
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(open, 4096); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(open)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("root mode = %v, want 0700", info.Mode().Perm())
	}
}
