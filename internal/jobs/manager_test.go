package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

type fakeRunner struct {
	runFunc func(context.Context, runner.Request) (runner.Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, req runner.Request) (runner.Result, error) {
	return f.runFunc(ctx, req)
}

func newTestManager(t *testing.T, fake Runner, opts Options) (*Manager, *artifacts.Store) {
	t.Helper()

	store, err := artifacts.NewStore(filepath.Join(t.TempDir(), "reports"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, fake, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	return manager, store
}

func testSpec(t *testing.T) Spec {
	t.Helper()
	return Spec{
		TargetID:   "fixture",
		Executable: "/bin/true",
		Dir:        t.TempDir(),
		Env:        []string{},
	}
}

func waitJob(t *testing.T, job *Job) Snapshot {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot, err := job.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	return snapshot
}

func readManifest(t *testing.T, store *artifacts.Store, runID string) Snapshot {
	t.Helper()

	dir, err := store.Path(runID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("parse result.json: %v", err)
	}
	return snapshot
}

func TestManagerCompletesAndWritesManifest(t *testing.T) {
	manager, store := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{Started: true, ExitCode: 0, Duration: 12 * time.Millisecond}, nil
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snapshot := waitJob(t, job)

	if snapshot.State != StateCompleted {
		t.Errorf("State = %q, want %q", snapshot.State, StateCompleted)
	}
	if snapshot.Result == nil || snapshot.Result.ExitCode != 0 {
		t.Errorf("Result = %+v, want exit code 0", snapshot.Result)
	}
	if snapshot.StartedAt == nil || snapshot.FinishedAt == nil {
		t.Errorf("timestamps missing: %+v", snapshot)
	}
	if manager.Active() != 0 {
		t.Errorf("Active() = %d, want 0", manager.Active())
	}

	manifest := readManifest(t, store, job.ID())
	if manifest.SchemaVersion != SchemaVersion {
		t.Errorf("manifest schema = %d, want %d", manifest.SchemaVersion, SchemaVersion)
	}
	if manifest.State != StateCompleted {
		t.Errorf("manifest state = %q, want completed", manifest.State)
	}

	dir, err := store.Path(job.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stdout.log", "stderr.log"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
}

func TestManagerAddsTmpDirToEnvironment(t *testing.T) {
	var captured []string
	manager, store := newTestManager(t, &fakeRunner{
		runFunc: func(_ context.Context, req runner.Request) (runner.Result, error) {
			captured = append([]string(nil), req.Env...)
			return runner.Result{Started: true}, nil
		},
	}, Options{MaxJobs: 1})

	spec := testSpec(t)
	spec.Env = []string{"PATH=/bin"}
	job, err := manager.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := waitJob(t, job); snapshot.State != StateCompleted {
		t.Fatalf("State = %q, want completed", snapshot.State)
	}

	dir, err := store.Path(job.ID())
	if err != nil {
		t.Fatal(err)
	}
	want := "TMPDIR=" + dir
	found := false
	for _, entry := range captured {
		if entry == want {
			found = true
		}
	}
	if !found {
		t.Errorf("environment = %v, want %q", captured, want)
	}
}

func TestManagerProgramExitCodeIsIndependent(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{Started: true, ExitCode: 42}, nil
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitJob(t, job)

	if snapshot.State != StateCompleted {
		t.Errorf("State = %q, want completed", snapshot.State)
	}
	if snapshot.Result == nil || snapshot.Result.ExitCode != 42 {
		t.Errorf("Result = %+v, want exit code 42", snapshot.Result)
	}
}

func TestManagerTimeoutState(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{Started: true, TimedOut: true}, nil
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitJob(t, job)

	if snapshot.State != StateTimedOut {
		t.Errorf("State = %q, want timed_out", snapshot.State)
	}
}

func TestManagerCancellation(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
			<-ctx.Done()
			return runner.Result{Canceled: true}, nil
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	job.Cancel()
	snapshot := waitJob(t, job)

	if snapshot.State != StateCancelled {
		t.Errorf("State = %q, want cancelled", snapshot.State)
	}
}

func TestManagerFailureAndManifestError(t *testing.T) {
	manager, store := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{}, errors.New("boom")
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitJob(t, job)

	if snapshot.State != StateFailed {
		t.Errorf("State = %q, want failed", snapshot.State)
	}
	if !strings.Contains(snapshot.Error, "boom") {
		t.Errorf("Error = %q, want boom", snapshot.Error)
	}
	manifest := readManifest(t, store, job.ID())
	if manifest.State != StateFailed || !strings.Contains(manifest.Error, "boom") {
		t.Errorf("manifest = %+v, want failed with boom", manifest)
	}
}

func TestManagerConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
			select {
			case <-release:
				return runner.Result{Started: true}, nil
			case <-ctx.Done():
				return runner.Result{Canceled: true}, nil
			}
		},
	}, Options{MaxJobs: 1})

	first, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), testSpec(t)); err == nil {
		t.Fatal("second Start() error = nil, want a limit error")
	} else {
		var limit *LimitError
		if !errors.As(err, &limit) {
			t.Errorf("error type = %T, want *LimitError", err)
		}
	}

	close(release)
	waitJob(t, first)

	second, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatalf("Start() after completion error = %v", err)
	}
	waitJob(t, second)
}

func TestManagerCloseCancelsAndRejectsNewJobs(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
			<-ctx.Done()
			return runner.Result{Canceled: true}, nil
		},
	}, Options{MaxJobs: 2})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()
	snapshot := waitJob(t, job)
	if snapshot.State != StateCancelled {
		t.Errorf("State = %q, want cancelled", snapshot.State)
	}

	if _, err := manager.Start(context.Background(), testSpec(t)); !errors.Is(err, ErrClosed) {
		t.Errorf("Start() error = %v, want ErrClosed", err)
	}
}

func TestManagerRejectsInvalidSpecs(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{}, nil
		},
	}, Options{MaxJobs: 1})

	for _, spec := range []Spec{
		{TargetID: "", Executable: "/bin/true", Dir: t.TempDir()},
		{TargetID: "fixture", Executable: "relative", Dir: t.TempDir()},
		{TargetID: "fixture", Executable: "/bin/true", Dir: "relative"},
	} {
		if _, err := manager.Start(context.Background(), spec); err == nil {
			t.Errorf("Start(%+v) error = nil, want rejection", spec)
		}
	}
}

func TestManagerWithRealRunner(t *testing.T) {
	if _, err := os.Stat("/bin/true"); err != nil {
		t.Skip("/bin/true is not available")
	}

	manager, store := newTestManager(t, runner.New(), Options{MaxJobs: 1, Timeout: 2 * time.Second})
	spec := testSpec(t)
	spec.Executable = "/bin/true"

	job, err := manager.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := waitJob(t, job)
	if snapshot.State != StateCompleted {
		t.Fatalf("State = %q, want completed (error: %s)", snapshot.State, snapshot.Error)
	}
	if snapshot.Result == nil || snapshot.Result.ExitCode != 0 {
		t.Errorf("Result = %+v, want exit 0", snapshot.Result)
	}

	manifest := readManifest(t, store, job.ID())
	if manifest.State != StateCompleted {
		t.Errorf("manifest state = %q, want completed", manifest.State)
	}
}

func TestManagerConcurrentJobs(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			time.Sleep(time.Millisecond)
			return runner.Result{Started: true}, nil
		},
	}, Options{MaxJobs: 2})

	specs := make([]Spec, 8)
	for i := range specs {
		specs[i] = testSpec(t)
	}

	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Add(1)
		go func(spec Spec) {
			defer wg.Done()
			job, err := manager.Start(context.Background(), spec)
			if err != nil {
				var limit *LimitError
				if !errors.As(err, &limit) {
					t.Errorf("Start() error = %v", err)
				}
				return
			}
			waitJob(t, job)
		}(spec)
	}
	wg.Wait()

	if manager.Active() != 0 {
		t.Errorf("Active() = %d, want 0", manager.Active())
	}
}

func TestManagerJobOutlivesStartContext(t *testing.T) {
	release := make(chan struct{})
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
			select {
			case <-release:
				return runner.Result{Started: true}, nil
			case <-ctx.Done():
				return runner.Result{Canceled: true}, nil
			}
		},
	}, Options{MaxJobs: 1})

	reqCtx, cancelReq := context.WithCancel(context.Background())
	job, err := manager.Start(reqCtx, testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	cancelReq()
	time.Sleep(50 * time.Millisecond)
	close(release)

	if snapshot := waitJob(t, job); snapshot.State != StateCompleted {
		t.Errorf("State = %q, want completed after the request context ended", snapshot.State)
	}
}

func TestManagerCloseWaitsForJobs(t *testing.T) {
	manager, store := newTestManager(t, &fakeRunner{
		runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
			<-ctx.Done()
			time.Sleep(100 * time.Millisecond)
			return runner.Result{Canceled: true}, nil
		},
	}, Options{MaxJobs: 1})

	job, err := manager.Start(context.Background(), testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.Close()

	if manager.Active() != 0 {
		t.Errorf("Active() = %d after Close, want 0", manager.Active())
	}
	if manifest := readManifest(t, store, job.ID()); manifest.State != StateCancelled {
		t.Errorf("manifest state = %q, want cancelled", manifest.State)
	}
}

func TestManagerEvictsOldestFinishedJobs(t *testing.T) {
	manager, _ := newTestManager(t, &fakeRunner{
		runFunc: func(context.Context, runner.Request) (runner.Result, error) {
			return runner.Result{Started: true}, nil
		},
	}, Options{MaxJobs: 1, MaxRetained: 2})

	var ids []string
	for range 3 {
		job, err := manager.Start(context.Background(), testSpec(t))
		if err != nil {
			t.Fatal(err)
		}
		waitJob(t, job)
		ids = append(ids, job.ID())
	}
	if _, ok := manager.Get(ids[0]); ok {
		t.Error("oldest finished job still retained, want evicted")
	}
	for _, id := range ids[1:] {
		if _, ok := manager.Get(id); !ok {
			t.Errorf("job %s evicted, want retained", id)
		}
	}
}

func TestManagerStartInDir(t *testing.T) {
	fake := &fakeRunner{runFunc: func(_ context.Context, req runner.Request) (runner.Result, error) {
		return runner.Result{Started: true, ExitCode: 0}, nil
	}}
	manager, store := newTestManager(t, fake, Options{MaxJobs: 1, Timeout: time.Second, MaxOutputBytes: 4096})
	runDir, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}

	job, err := manager.StartInDir(context.Background(), runDir, testSpec(t))
	if err != nil {
		t.Fatalf("StartInDir() error = %v", err)
	}
	if job.ID() != runDir.ID() {
		t.Errorf("job id = %q, want the supplied run dir %q", job.ID(), runDir.ID())
	}
	snapshot := waitJob(t, job)
	if snapshot.State != StateCompleted {
		t.Errorf("State = %q, want completed", snapshot.State)
	}
	if _, err := os.Stat(filepath.Join(runDir.Path(), "result.json")); err != nil {
		t.Errorf("result.json missing from the supplied run dir: %v", err)
	}
}

func TestManagerStartInDirRejectsForeignDirectory(t *testing.T) {
	fake := &fakeRunner{runFunc: func(context.Context, runner.Request) (runner.Result, error) {
		return runner.Result{Started: true}, nil
	}}
	manager, _ := newTestManager(t, fake, Options{MaxJobs: 1, Timeout: time.Second, MaxOutputBytes: 4096})
	otherStore, err := artifacts.NewStore(filepath.Join(t.TempDir(), "other"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := otherStore.Create()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.StartInDir(context.Background(), foreign, testSpec(t)); err == nil {
		t.Fatal("StartInDir() accepted a directory from another store")
	}
	if _, err := manager.StartInDir(context.Background(), nil, testSpec(t)); err == nil {
		t.Fatal("StartInDir() accepted a nil directory")
	}
}

func TestManagerStartInDirRespectsQuota(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeRunner{runFunc: func(ctx context.Context, _ runner.Request) (runner.Result, error) {
		select {
		case <-release:
			return runner.Result{Started: true}, nil
		case <-ctx.Done():
			return runner.Result{Started: true, Canceled: true}, nil
		}
	}}
	manager, store := newTestManager(t, fake, Options{MaxJobs: 1, Timeout: time.Second, MaxOutputBytes: 4096})

	first, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartInDir(context.Background(), first, testSpec(t)); err != nil {
		t.Fatal(err)
	}
	second, err := store.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartInDir(context.Background(), second, testSpec(t)); err == nil {
		t.Fatal("StartInDir() accepted a job beyond max_jobs")
	} else {
		var limit *LimitError
		if !errors.As(err, &limit) {
			t.Fatalf("StartInDir() error = %T %v, want LimitError", err, err)
		}
	}
	close(release)
}
