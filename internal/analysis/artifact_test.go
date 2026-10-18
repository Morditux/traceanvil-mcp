package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func TestArtifactExcerpts(t *testing.T) {
	store := newTestStore(t)
	content := strings.Repeat("0123456789", 10)
	runID := writeArtifact(t, store, valgrind.RawArtifactName, []byte(content))
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxArtifactBytes = 64
	})
	ctx := context.Background()

	first, err := service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		MaxBytes:     10,
	})
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	if first.Content != content[:10] || first.ReturnedBytes != 10 {
		t.Errorf("content = %q returned = %d", first.Content, first.ReturnedBytes)
	}
	if first.Bytes != int64(len(content)) || !first.HasMore || first.NextOffset != 10 {
		t.Errorf("artifact = %+v", first)
	}
	if first.FileName != valgrind.RawArtifactName {
		t.Errorf("file_name = %q", first.FileName)
	}

	second, err := service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		Offset:       first.NextOffset,
		MaxBytes:     1000,
	})
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	if second.MaxBytes != 64 {
		t.Errorf("max_bytes = %d, want the configured cap 64", second.MaxBytes)
	}
	if second.Content != content[10:10+64] {
		t.Errorf("content = %q", second.Content)
	}
	if second.NextOffset != 74 || !second.HasMore {
		t.Errorf("artifact = %+v", second)
	}

	last, err := service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		Offset:       second.NextOffset,
	})
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	if last.Content != content[74:] || last.HasMore || last.NextOffset != int64(len(content)) {
		t.Errorf("last artifact = %+v", last)
	}

	atEnd, err := service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		Offset:       int64(len(content)),
	})
	if err != nil {
		t.Fatalf("Artifact() at EOF error = %v", err)
	}
	if atEnd.Content != "" || atEnd.HasMore || atEnd.ReturnedBytes != 0 {
		t.Errorf("at EOF = %+v", atEnd)
	}

	_, err = service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		Offset:       int64(len(content)) + 1,
	})
	mustCode(t, err, CodeInvalidArgument)

	_, err = service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		Offset:       -1,
	})
	mustCode(t, err, CodeInvalidArgument)

	_, err = service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactRawXML,
		MaxBytes:     -1,
	})
	mustCode(t, err, CodeInvalidArgument)
}

func TestArtifactReportAndManifest(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	report := sampleReport("", nil)
	runID := writeReportRun(t, store, report)
	output, err := service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactAnalysisReport,
		MaxBytes:     20,
	})
	if err != nil {
		t.Fatalf("Artifact(analysis_report) error = %v", err)
	}
	if !strings.HasPrefix(output.Content, `{"schema_version":1`) {
		t.Errorf("content = %q", output.Content)
	}
	if !output.HasMore {
		t.Error("has_more = false for the first 20 bytes")
	}

	_, err = service.Artifact(ctx, ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactJobManifest,
	})
	mustCode(t, err, CodeArtifactUnavailable)
}

func TestArtifactErrors(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), nil)
	ctx := context.Background()

	_, err := service.Artifact(ctx, ArtifactRequest{RunID: "bad", ArtifactType: ArtifactRawXML})
	mustCode(t, err, CodeInvalidArgument)

	_, err = service.Artifact(ctx, ArtifactRequest{
		RunID:        "run_ffffffffffffffffffffffffffffffff",
		ArtifactType: ArtifactRawXML,
	})
	mustCode(t, err, CodeRunNotFound)

	emptyRun := createRunDir(t, store)
	_, err = service.Artifact(ctx, ArtifactRequest{RunID: emptyRun.ID(), ArtifactType: ArtifactRawXML})
	mustCode(t, err, CodeArtifactUnavailable)

	_, err = service.Artifact(ctx, ArtifactRequest{RunID: emptyRun.ID(), ArtifactType: "secrets"})
	mustCode(t, err, CodeInvalidArgument)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = service.Artifact(cancelled, ArtifactRequest{RunID: emptyRun.ID(), ArtifactType: ArtifactRawXML})
	mustCode(t, err, CodeCanceled)
}

func TestArtifactRejectsSymlink(t *testing.T) {
	store := newTestStore(t)
	run := createRunDir(t, store)
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(run.Path(), "stdout.log")); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, newFakeBackend(), nil)
	_, err := service.Artifact(context.Background(), ArtifactRequest{
		RunID:        run.ID(),
		ArtifactType: ArtifactStdout,
	})
	mustCode(t, err, CodeArtifactUnavailable)
}

func TestArtifactRejectsFIFOWithoutBlocking(t *testing.T) {
	store := newTestStore(t)
	run := createRunDir(t, store)
	if err := syscall.Mkfifo(filepath.Join(run.Path(), "stdout.log"), 0o600); err != nil {
		t.Skipf("mkfifo is not available: %v", err)
	}
	service := newTestService(t, store, newFakeBackend(), nil)

	done := make(chan error, 1)
	go func() {
		_, err := service.Artifact(context.Background(), ArtifactRequest{
			RunID:        run.ID(),
			ArtifactType: ArtifactStdout,
		})
		done <- err
	}()
	select {
	case err := <-done:
		mustCode(t, err, CodeArtifactUnavailable)
	case <-time.After(2 * time.Second):
		t.Fatal("Artifact() blocked on a planted FIFO")
	}
}

func TestArtifactReplacesInvalidUTF8(t *testing.T) {
	store := newTestStore(t)
	runID := writeArtifact(t, store, "stdout.log", []byte{0xff, 0xfe, 'o', 'k'})
	service := newTestService(t, store, newFakeBackend(), nil)

	output, err := service.Artifact(context.Background(), ArtifactRequest{
		RunID:        runID,
		ArtifactType: ArtifactStdout,
	})
	if err != nil {
		t.Fatalf("Artifact() error = %v", err)
	}
	if !output.InvalidUTF8Replaced {
		t.Error("invalid_utf8_replaced = false")
	}
	if output.ReturnedBytes != 4 {
		t.Errorf("returned_bytes = %d, want 4", output.ReturnedBytes)
	}
	if !strings.Contains(output.Content, "\uFFFD") || !strings.HasSuffix(output.Content, "ok") {
		t.Errorf("content = %q", output.Content)
	}
}
