package jobs

import (
	"context"
	"sync"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/runner"
)

// SchemaVersion versions result.json and Snapshot payloads.
const SchemaVersion = 1

// State is the lifecycle of one job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateCancelled State = "cancelled"
	StateFailed    State = "failed"
	StateTimedOut  State = "timed_out"
)

// Spec describes one execution request after policy resolution.
type Spec struct {
	TargetID   string
	Executable string
	Args       []string
	Dir        string
	Env        []string
	// Timeout optionally shortens the manager-wide timeout for this job. It
	// can never extend it: the manager clamps larger values. The request layer
	// rejects values above the configured maximum instead of relying on that
	// clamp.
	Timeout time.Duration
}

// Result is the program outcome, separate from the job state.
type Result struct {
	Started          bool  `json:"started"`
	ExitCode         int   `json:"exit_code"`
	Signal           int   `json:"signal,omitempty"`
	TimedOut         bool  `json:"timed_out"`
	Canceled         bool  `json:"canceled"`
	DurationMS       int64 `json:"duration_ms"`
	StdoutBytes      int64 `json:"stdout_bytes"`
	StderrBytes      int64 `json:"stderr_bytes"`
	StdoutTruncated  bool  `json:"stdout_truncated"`
	StderrTruncated  bool  `json:"stderr_truncated"`
	OutputIncomplete bool  `json:"output_incomplete"`
}

// Snapshot is the externally visible job record. It is also the content of
// result.json inside the run directory.
type Snapshot struct {
	SchemaVersion int        `json:"schema_version"`
	RunID         string     `json:"run_id"`
	TargetID      string     `json:"target_id"`
	State         State      `json:"state"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Result        *Result    `json:"program_result,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// Job is a handle to one asynchronous execution.
type Job struct {
	mu sync.Mutex

	runID     string
	targetID  string
	createdAt time.Time

	state      State
	startedAt  time.Time
	finishedAt time.Time
	result     *Result
	errText    string
	finished   bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// ID returns the opaque run identifier.
func (j *Job) ID() string { return j.runID }

// Snapshot returns a consistent copy of the current job record.
func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked(j.finishedAt, j.state, j.result, j.errText)
}

// Wait blocks until the job finishes or the caller's context is done.
func (j *Job) Wait(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-j.done:
		return j.Snapshot(), nil
	case <-ctx.Done():
		return j.Snapshot(), ctx.Err()
	}
}

// Cancel requests cancellation. It is safe to call after completion.
func (j *Job) Cancel() {
	j.mu.Lock()
	if !j.finished {
		j.cancel()
	}
	j.mu.Unlock()
}

func (j *Job) markRunning() {
	j.mu.Lock()
	if !j.finished {
		j.state = StateRunning
		j.startedAt = time.Now().UTC()
	}
	j.mu.Unlock()
}

func (j *Job) finish(state State, result *Result, errText string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	j.state = state
	j.result = copyResult(result)
	j.errText = errText
	j.finishedAt = at
	j.finished = true
	close(j.done)
}

func (j *Job) snapshotLocked(finishedAt time.Time, state State, result *Result, errText string) Snapshot {
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		RunID:         j.runID,
		TargetID:      j.targetID,
		State:         state,
		CreatedAt:     j.createdAt,
		Error:         errText,
		Result:        copyResult(result),
	}
	if !j.startedAt.IsZero() {
		started := j.startedAt
		snapshot.StartedAt = &started
	}
	if !finishedAt.IsZero() {
		finished := finishedAt
		snapshot.FinishedAt = &finished
	}
	return snapshot
}

func (j *Job) snapshotFor(finishedAt time.Time, state State, result *Result, errText string) Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked(finishedAt, state, result, errText)
}

func (j *Job) context() context.Context {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ctx
}

func copyResult(result *Result) *Result {
	if result == nil {
		return nil
	}
	copied := *result
	return &copied
}

func resultFromRunner(result runner.Result) *Result {
	return &Result{
		Started:          result.Started,
		ExitCode:         result.ExitCode,
		Signal:           int(result.Signal),
		TimedOut:         result.TimedOut,
		Canceled:         result.Canceled,
		DurationMS:       result.Duration.Milliseconds(),
		StdoutBytes:      result.StdoutBytes,
		StderrBytes:      result.StderrBytes,
		StdoutTruncated:  result.StdoutTruncated,
		StderrTruncated:  result.StderrTruncated,
		OutputIncomplete: result.OutputIncomplete,
	}
}
