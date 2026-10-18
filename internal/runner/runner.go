// Package runner executes one allowlisted program with bounded output,
// timeouts, cancellation and process-group cleanup. It never invokes a shell
// and accepts no command string: arguments are passed as a list.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Defaults bound an execution even when the caller omits limits.
const (
	DefaultTimeout        = 120 * time.Second
	DefaultGracePeriod    = time.Second
	DefaultMaxOutputBytes = 4 << 20
)

// Request describes one process to launch. Env is used exactly as provided;
// nil means an empty environment (never os.Environ).
type Request struct {
	Executable     string
	Args           []string
	Dir            string
	Env            []string
	Timeout        time.Duration
	MaxOutputBytes int64
	Stdout         io.Writer
	Stderr         io.Writer
}

// Result is the outcome of one process. A non-zero exit code or a signal is a
// program result, not a runner error.
type Result struct {
	Started          bool
	PID              int
	ExitCode         int
	Signal           syscall.Signal
	TimedOut         bool
	Canceled         bool
	StartTime        time.Time
	EndTime          time.Time
	Duration         time.Duration
	StdoutBytes      int64
	StderrBytes      int64
	StdoutTruncated  bool
	StderrTruncated  bool
	OutputIncomplete bool
}

// Runner applies defaults and process-group behavior.
type Runner struct {
	GracePeriod           time.Duration
	DefaultTimeout        time.Duration
	DefaultMaxOutputBytes int64
}

// New returns a runner with safe defaults.
func New() *Runner {
	return &Runner{
		GracePeriod:           DefaultGracePeriod,
		DefaultTimeout:        DefaultTimeout,
		DefaultMaxOutputBytes: DefaultMaxOutputBytes,
	}
}

// Run executes one process and waits for it. Cancellation and timeouts return a
// Result with TimedOut or Canceled set; they are not returned as errors.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("runner: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Result{
			Canceled: errors.Is(err, context.Canceled),
			TimedOut: errors.Is(err, context.DeadlineExceeded),
		}, nil
	}
	if !filepath.IsAbs(req.Executable) {
		return Result{}, errors.New("runner: executable must be an absolute path")
	}
	if req.Dir != "" && !filepath.IsAbs(req.Dir) {
		return Result{}, errors.New("runner: working directory must be an absolute path")
	}
	if strings.ContainsRune(req.Executable, 0) {
		return Result{}, errors.New("runner: executable contains NUL")
	}
	for _, arg := range req.Args {
		if strings.ContainsRune(arg, 0) {
			return Result{}, errors.New("runner: argument contains NUL")
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.defaultTimeout()
	}
	maxOutput := req.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = r.defaultMaxOutput()
	}
	grace := r.gracePeriod()

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	budget := newByteBudget(maxOutput)
	stdout := newCapture(req.Stdout, budget)
	stderr := newCapture(req.Stderr, budget)

	cmd := exec.Command(req.Executable, req.Args...)
	cmd.Dir = req.Dir
	// A nil Env would inherit os.Environ; always pass an explicit slice.
	cmd.Env = req.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	// Never connect the server's stdin to the target.
	cmd.Stdin = nil
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Pdeathsig is deliberately not used: Go documents that it is delivered on
	// creating-thread termination, which may happen before process termination.
	// Process-group cleanup and the manager's shutdown path cover normal exits;
	// a hard server crash may leave detached children (documented limitation).
	// If the direct child exits while a grandchild keeps the pipes open, bound
	// the wait and close the pipes instead of hanging forever.
	cmd.WaitDelay = grace

	start := time.Now()
	if err := cmd.Start(); err != nil {
		end := time.Now()
		return Result{StartTime: start, EndTime: end, Duration: end.Sub(start)},
			fmt.Errorf("runner: start %q: %w", req.Executable, err)
	}
	pid := cmd.Process.Pid

	// Supervise without reaping: while the leader is a zombie its pid and
	// process group id cannot be reused, so group signals stay safe.
	exited := make(chan struct{})
	go func() {
		// On failure fall through to cmd.Wait, which reports the real error.
		_ = waitExited(pid)
		close(exited)
	}()
	terminated, termCause := supervise(runCtx, pid, exited, grace)

	// Kill any remaining group members, including children of a process that
	// exited normally but left descendants behind, then reap the leader.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitErr := cmd.Wait()

	end := time.Now()
	result := Result{
		Started:         true,
		PID:             pid,
		StartTime:       start,
		EndTime:         end,
		Duration:        end.Sub(start),
		StdoutBytes:     stdout.Bytes(),
		StderrBytes:     stderr.Bytes(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if state := cmd.ProcessState; state != nil {
		result.ExitCode = state.ExitCode()
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal()
		}
	}
	if terminated {
		switch {
		case errors.Is(termCause, context.DeadlineExceeded):
			result.TimedOut = true
		case errors.Is(termCause, context.Canceled):
			result.Canceled = true
		}
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		result.OutputIncomplete = true
	}

	if captureErr := firstError(stdout.Err(), stderr.Err()); captureErr != nil {
		return result, fmt.Errorf("runner: capture output: %w", captureErr)
	}
	if waitErr != nil && !isExitError(waitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return result, fmt.Errorf("runner: wait: %w", waitErr)
	}
	return result, nil
}

func (r *Runner) gracePeriod() time.Duration {
	if r.GracePeriod <= 0 {
		return DefaultGracePeriod
	}
	return r.GracePeriod
}

func (r *Runner) defaultTimeout() time.Duration {
	if r.DefaultTimeout <= 0 {
		return DefaultTimeout
	}
	return r.DefaultTimeout
}

func (r *Runner) defaultMaxOutput() int64 {
	if r.DefaultMaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return r.DefaultMaxOutputBytes
}

// supervise waits for the leader to exit. When ctx ends first it sends TERM to
// the process group, then KILL after the grace period. It reports whether it
// terminated the process and why; a natural exit that races with ctx expiry is
// not counted as terminated.
func supervise(ctx context.Context, pid int, exited <-chan struct{}, grace time.Duration) (bool, error) {
	select {
	case <-exited:
		return false, nil
	case <-ctx.Done():
	}
	select {
	case <-exited:
		return false, nil
	default:
	}

	cause := ctx.Err()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-timer.C:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
	case <-exited:
	}
	return true, cause
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// byteBudget is shared by stdout and stderr so the total captured output is
// bounded regardless of stream.
type byteBudget struct {
	mu        sync.Mutex
	remaining int64
}

func newByteBudget(max int64) *byteBudget {
	return &byteBudget{remaining: max}
}

func (b *byteBudget) take(n int64) int64 {
	if n <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.remaining {
		n = b.remaining
	}
	b.remaining -= n
	return n
}

// captureWriter writes at most the shared budget and discards excess bytes so
// the child is never blocked by a full pipe.
type captureWriter struct {
	dst       io.Writer
	budget    *byteBudget
	written   int64
	truncated bool
	err       error
}

func newCapture(dst io.Writer, budget *byteBudget) *captureWriter {
	if dst == nil {
		dst = io.Discard
	}
	return &captureWriter{dst: dst, budget: budget}
}

// Write never fails: after a destination error the data is discarded so the
// child is not blocked on a full pipe, and the error is reported through Err.
func (w *captureWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		w.truncated = true
		return len(p), nil
	}
	allowed := w.budget.take(int64(len(p)))
	if allowed > 0 {
		n, err := w.dst.Write(p[:allowed])
		w.written += int64(n)
		if err == nil && int64(n) != allowed {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			w.truncated = true
			return len(p), nil
		}
	}
	if allowed < int64(len(p)) {
		w.truncated = true
	}
	return len(p), nil
}

func (w *captureWriter) Bytes() int64 { return w.written }

func (w *captureWriter) Truncated() bool { return w.truncated }

func (w *captureWriter) Err() error { return w.err }
