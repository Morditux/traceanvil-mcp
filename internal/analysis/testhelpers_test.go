package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

const testRunID = "run_00000000000000000000000000000001"

type fakeRun struct {
	mu       sync.Mutex
	id       string
	snapshot valgrind.AnalysisSnapshot
	job      jobs.Snapshot
	canceled bool
}

func (r *fakeRun) ID() string { return r.id }

func (r *fakeRun) Snapshot() valgrind.AnalysisSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

func (r *fakeRun) JobSnapshot() jobs.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}

func (r *fakeRun) Cancel() {
	r.mu.Lock()
	r.canceled = true
	r.mu.Unlock()
}

func (r *fakeRun) set(snapshot valgrind.AnalysisSnapshot, job jobs.Snapshot) {
	r.mu.Lock()
	r.snapshot = snapshot
	r.job = job
	r.mu.Unlock()
}

type fakeBackend struct {
	mu      sync.Mutex
	runs    map[string]*fakeRun
	startFn func(context.Context, valgrind.Request) (Run, error)
	last    valgrind.Request
	nextID  int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{runs: make(map[string]*fakeRun)}
}

func (b *fakeBackend) Start(ctx context.Context, req valgrind.Request) (Run, error) {
	b.mu.Lock()
	b.last = req
	fn := b.startFn
	b.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := fmt.Sprintf("run_%032x", b.nextID)
	now := time.Now().UTC()
	run := &fakeRun{
		id: id,
		snapshot: valgrind.AnalysisSnapshot{
			RunID: id,
			Tool:  req.Tool,
			Phase: valgrind.PhaseRunning,
		},
		job: jobs.Snapshot{
			SchemaVersion: jobs.SchemaVersion,
			RunID:         id,
			TargetID:      req.TargetID,
			State:         jobs.StateRunning,
			CreatedAt:     now,
			StartedAt:     &now,
		},
	}
	b.runs[id] = run
	return run, nil
}

func (b *fakeBackend) Get(runID string) (Run, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	run, ok := b.runs[runID]
	if !ok {
		return nil, false
	}
	return run, true
}

func (b *fakeBackend) lastRequest() valgrind.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func newTestStore(t *testing.T) *artifacts.Store {
	t.Helper()
	store, err := artifacts.NewStore(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func newTestService(t *testing.T, store *artifacts.Store, backend Backend, mutate func(*Options)) *Service {
	t.Helper()
	opts := Options{
		Store:          store,
		Backend:        backend,
		BackendEnabled: true,
		Timeout:        5 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	service, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(service.Close)
	return service
}

func sampleDiagnostic(i int) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		ID:                   fmt.Sprintf("diag_%d", i),
		Fingerprint:          "sha256:" + strings.Repeat("ab", 32),
		FingerprintAlgorithm: diagnostic.FingerprintAlgorithmV1,
		Origin:               valgrind.Origin,
		Kind:                 "InvalidRead",
		Severity:             diagnostic.SeverityError,
		Message:              "Invalid read of size 4",
		Location:             &diagnostic.Location{Function: "main", Path: "invalid_read.c", Line: 8},
		Stack:                []diagnostic.Frame{{Function: "main", Path: "invalid_read.c", Line: 8}},
		Occurrences:          1,
		Evidence: diagnostic.Evidence{
			Artifact:    valgrind.RawArtifactName,
			RecordID:    "0x0",
			RecordIndex: i,
			RawKind:     "InvalidRead",
		},
		Confidence: diagnostic.ConfidenceToolReported,
	}
}

func sampleReport(runID string, diagnostics []diagnostic.Diagnostic) diagnostic.Report {
	return diagnostic.Report{
		SchemaVersion:    diagnostic.SchemaVersion,
		RunID:            runID,
		Tool:             diagnostic.Tool{Name: "valgrind", Path: "/usr/bin/valgrind", Version: "valgrind-3.26.0"},
		Result:           diagnostic.ResultErrorsFound,
		AnalysisComplete: true,
		Program: diagnostic.Program{
			TargetID: "fixture",
			Path:     "/workspace/fixture",
			SHA256:   "sha256:" + strings.Repeat("cd", 32),
			Args:     []string{},
			ExitCode: 42,
		},
		Summary:     diagnostic.BuildSummary(diagnostics, nil, false),
		Diagnostics: diagnostics,
		Artifacts: []diagnostic.Artifact{{
			Name:   valgrind.RawArtifactName,
			Kind:   "raw_xml",
			Bytes:  2637,
			SHA256: "sha256:" + strings.Repeat("ef", 32),
		}},
		Warnings: []string{},
	}
}

func createRunDir(t *testing.T, store *artifacts.Store) *artifacts.RunDir {
	t.Helper()
	runDir, err := store.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return runDir
}

func writeReportRun(t *testing.T, store *artifacts.Store, report diagnostic.Report) string {
	t.Helper()
	runDir := createRunDir(t, store)
	if report.RunID == "" {
		report.RunID = runDir.ID()
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := runDir.WriteFileAtomic(valgrind.AnalysisArtifactName, data); err != nil {
		t.Fatalf("write analysis.json: %v", err)
	}
	return runDir.ID()
}

func writeRawReport(t *testing.T, store *artifacts.Store, content string) string {
	t.Helper()
	runDir := createRunDir(t, store)
	if err := runDir.WriteFileAtomic(valgrind.AnalysisArtifactName, []byte(content)); err != nil {
		t.Fatalf("write analysis.json: %v", err)
	}
	return runDir.ID()
}

func writeManifest(t *testing.T, store *artifacts.Store, snapshot jobs.Snapshot) string {
	t.Helper()
	runDir := createRunDir(t, store)
	if snapshot.RunID == "" {
		snapshot.RunID = runDir.ID()
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := runDir.WriteFileAtomic(jobs.ManifestName, data); err != nil {
		t.Fatalf("write result.json: %v", err)
	}
	return runDir.ID()
}

func writeArtifact(t *testing.T, store *artifacts.Store, name string, data []byte) string {
	t.Helper()
	runDir := createRunDir(t, store)
	if err := runDir.WriteFileAtomic(name, data); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return runDir.ID()
}

func mustCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	if got := CodeOf(err); got != want {
		t.Fatalf("error code = %q (%v), want %q", got, err, want)
	}
}

func runDirPath(t *testing.T, store *artifacts.Store, runID string) string {
	t.Helper()
	path, err := store.Path(runID)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	return path
}

// writeFilesAt creates a run directory with the given run id and writes files
// directly. It is used for persisted-run tests where the run id cannot be chosen
// through Store.Create.
func writeFilesAt(t *testing.T, store *artifacts.Store, runID string, files map[string][]byte) {
	t.Helper()
	path := runDirPath(t, store, runID)
	for name, data := range files {
		if err := os.WriteFile(path+"/"+name, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
