package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/artifacts"
	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

// ManifestName is the job manifest file written inside every run directory.
const ManifestName = "result.json"

const manifestReserve int64 = 64 << 10

// Runner is the execution dependency used by the manager.
type Runner interface {
	Run(context.Context, runner.Request) (runner.Result, error)
}

// Options bounds concurrent jobs and captured output.
type Options struct {
	MaxJobs        int
	Timeout        time.Duration
	MaxOutputBytes int64
	// MaxRetained bounds how many finished jobs stay queryable; the oldest
	// finished jobs are forgotten first. Their artifacts stay on disk.
	MaxRetained int
}

const defaultMaxRetained = 256

// LimitError reports that the concurrent job quota is exhausted.
type LimitError struct {
	Max int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("jobs: concurrency limit reached (%d active)", e.Max)
}

// ErrClosed is returned when Start is called after Close.
var ErrClosed = errors.New("jobs: manager is closed")

// Manager owns job lifecycle and the artifact directories.
type Manager struct {
	mu     sync.Mutex
	store  *artifacts.Store
	runner Runner
	opts   Options
	jobs   map[string]*Job
	active int
	closed bool

	// base owns every job context so a job outlives the request that started it.
	base     context.Context
	baseStop context.CancelFunc
	wg       sync.WaitGroup
	finished []string
}

// NewManager builds a manager. Output is bounded below the artifact quota so a
// result manifest always has room.
func NewManager(store *artifacts.Store, processRunner Runner, opts Options) (*Manager, error) {
	if store == nil {
		return nil, errors.New("jobs: nil artifact store")
	}
	if processRunner == nil {
		return nil, errors.New("jobs: nil runner")
	}
	if opts.MaxJobs <= 0 {
		opts.MaxJobs = 1
	}
	if opts.Timeout <= 0 {
		opts.Timeout = runner.DefaultTimeout
	}
	quota := store.MaxBytesPerRun()
	outputLimit := quota - manifestReserve
	if outputLimit < 1024 {
		outputLimit = quota / 2
	}
	if outputLimit < 1 {
		outputLimit = 1
	}
	if opts.MaxOutputBytes <= 0 || opts.MaxOutputBytes > outputLimit {
		opts.MaxOutputBytes = outputLimit
	}

	if opts.MaxRetained <= 0 {
		opts.MaxRetained = defaultMaxRetained
	}

	base, stop := context.WithCancel(context.Background())
	return &Manager{
		store:    store,
		runner:   processRunner,
		opts:     opts,
		jobs:     make(map[string]*Job),
		base:     base,
		baseStop: stop,
	}, nil
}

// Start creates an artifact directory and launches one job asynchronously. The
// context only gates admission: the job is bound to the manager lifetime and is
// stopped by Job.Cancel, its timeout or Close, not by the caller's context.
func (m *Manager) Start(ctx context.Context, spec Spec) (*Job, error) {
	if err := validateStart(ctx, spec); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if err := m.admitLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	runDir, err := m.store.Create()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	job := m.newJobLocked(runDir, spec)
	m.mu.Unlock()

	jobSpec := cloneSpec(spec)
	go m.execute(job, runDir, jobSpec)
	return job, nil
}

// StartInDir launches one job in a run directory that the caller already
// created through the manager's artifact store. It exists so a backend can
// build arguments that reference files inside the run directory (Valgrind XML)
// before the process starts. The manager keeps owning the manifest and the
// lifecycle; if admission fails, the caller still owns the directory.
func (m *Manager) StartInDir(ctx context.Context, runDir *artifacts.RunDir, spec Spec) (*Job, error) {
	if err := validateStart(ctx, spec); err != nil {
		return nil, err
	}
	if runDir == nil {
		return nil, errors.New("jobs: nil run directory")
	}
	if !m.store.Owns(runDir.Path()) {
		return nil, errors.New("jobs: run directory does not belong to this manager's store")
	}

	m.mu.Lock()
	if err := m.admitLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if _, exists := m.jobs[runDir.ID()]; exists {
		m.mu.Unlock()
		return nil, errors.New("jobs: run directory already has a job")
	}
	job := m.newJobLocked(runDir, spec)
	m.mu.Unlock()

	jobSpec := cloneSpec(spec)
	go m.execute(job, runDir, jobSpec)
	return job, nil
}

func cloneSpec(spec Spec) Spec {
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string(nil), spec.Env...)
	return spec
}

func validateStart(ctx context.Context, spec Spec) error {
	if ctx == nil {
		return errors.New("jobs: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if spec.TargetID == "" {
		return errors.New("jobs: target id must not be empty")
	}
	if !filepath.IsAbs(spec.Executable) {
		return errors.New("jobs: executable must be an absolute path")
	}
	if !filepath.IsAbs(spec.Dir) {
		return errors.New("jobs: working directory must be an absolute path")
	}
	if spec.Timeout < 0 {
		return errors.New("jobs: timeout must not be negative")
	}
	return nil
}

// admitLocked checks the manager state and the concurrency quota. The caller
// holds m.mu.
func (m *Manager) admitLocked() error {
	if m.closed {
		return ErrClosed
	}
	if m.active >= m.opts.MaxJobs {
		return &LimitError{Max: m.opts.MaxJobs}
	}
	return nil
}

// newJobLocked registers a job and reserves its concurrency slot. The caller
// holds m.mu and must have called admitLocked successfully.
func (m *Manager) newJobLocked(runDir *artifacts.RunDir, spec Spec) *Job {
	jobCtx, cancel := context.WithCancel(m.base)
	job := &Job{
		runID:     runDir.ID(),
		targetID:  spec.TargetID,
		tool:      spec.Tool,
		createdAt: time.Now().UTC(),
		state:     StateQueued,
		ctx:       jobCtx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.jobs[job.runID] = job
	m.active++
	m.wg.Add(1)
	return job
}

// Get returns a job handle by run id.
func (m *Manager) Get(runID string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[runID]
	return job, ok
}

// Active returns the number of queued or running jobs.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// Close prevents new jobs, cancels active ones and waits until each has
// terminated its process group and written its manifest.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	m.baseStop()
	m.wg.Wait()
}

func (m *Manager) execute(job *Job, runDir *artifacts.RunDir, spec Spec) {
	defer m.wg.Done()
	defer job.cancel()

	job.markRunning()

	stdout, err := runDir.OpenWriter("stdout.log")
	if err != nil {
		m.finalize(job, runDir, StateFailed, nil, err)
		return
	}
	stderr, err := runDir.OpenWriter("stderr.log")
	if err != nil {
		_ = stdout.Close()
		m.finalize(job, runDir, StateFailed, nil, err)
		return
	}

	timeout := m.opts.Timeout
	if spec.Timeout > 0 && spec.Timeout < timeout {
		// A per-job timeout may only shorten the manager-wide limit.
		timeout = spec.Timeout
	}

	runResult, runErr := m.runner.Run(job.context(), runner.Request{
		Executable:     spec.Executable,
		Args:           append([]string(nil), spec.Args...),
		Dir:            spec.Dir,
		Env:            environmentWithTmpDir(spec.Env, runDir.Path()),
		Timeout:        timeout,
		MaxOutputBytes: m.opts.MaxOutputBytes,
		Stdout:         stdout,
		Stderr:         stderr,
	})

	closeErr := firstError(stdout.Close(), stderr.Close())
	result := resultFromRunner(runResult)

	state := StateCompleted
	errText := ""
	switch {
	case runResult.TimedOut:
		state = StateTimedOut
	case runResult.Canceled:
		state = StateCancelled
	case runErr != nil:
		switch {
		case errors.Is(job.context().Err(), context.DeadlineExceeded):
			state = StateTimedOut
		case errors.Is(job.context().Err(), context.Canceled):
			state = StateCancelled
		default:
			state = StateFailed
		}
	}
	if runErr != nil {
		errText = runErr.Error()
	}
	if closeErr != nil {
		state = StateFailed
		if errText == "" {
			errText = closeErr.Error()
		} else {
			errText += "; " + closeErr.Error()
		}
	}

	var finalErr error
	if errText != "" {
		finalErr = errors.New(errText)
	}
	m.finalize(job, runDir, state, result, finalErr)
}

func (m *Manager) finalize(job *Job, runDir *artifacts.RunDir, state State, result *Result, finalErr error) {
	now := time.Now().UTC()
	errText := ""
	if finalErr != nil {
		errText = truncate(finalErr.Error(), 4096)
	}

	snapshot := job.snapshotFor(now, state, result, errText)
	data, marshalErr := json.MarshalIndent(snapshot, "", "  ")
	if marshalErr != nil {
		state = StateFailed
		errText = truncate("marshal result manifest: "+marshalErr.Error(), 4096)
	} else if writeErr := runDir.WriteFileAtomic(ManifestName, data); writeErr != nil {
		// The program outcome is still valid; report that the evidence on disk
		// is missing instead of rewriting the outcome.
		note := truncate("result manifest not persisted: "+writeErr.Error(), 4096)
		if errText == "" {
			errText = note
		} else {
			errText = truncate(errText+"; "+note, 4096)
		}
	}

	// Release the concurrency slot before closing done so Wait returning means
	// the job no longer counts against max_jobs.
	m.release(job)
	job.finish(state, result, errText, now)
}

// release frees the concurrency slot and evicts the oldest finished jobs
// beyond the retention bound.
func (m *Manager) release(job *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active > 0 {
		m.active--
	}
	m.finished = append(m.finished, job.runID)
	for len(m.finished) > m.opts.MaxRetained {
		delete(m.jobs, m.finished[0])
		m.finished = m.finished[1:]
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// environmentWithTmpDir isolates temporary files inside the run directory when
// the caller did not choose a TMPDIR explicitly.
func environmentWithTmpDir(env []string, runDir string) []string {
	result := append([]string(nil), env...)
	for _, entry := range result {
		if strings.HasPrefix(entry, "TMPDIR=") {
			return result
		}
	}
	return append(result, "TMPDIR="+runDir)
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
