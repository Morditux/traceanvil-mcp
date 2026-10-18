package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperRunFlag = "-test.run=^TestHelperProcess$"

// TestHelperProcess is a re-executed test binary used as a controlled child
// process. It never runs during normal tests unless the sentinel env is set.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	switch os.Getenv("HELPER_SCENARIO") {
	case "exit0":
		os.Exit(0)
	case "exit42":
		os.Exit(42)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "stdout":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("o"), 64*1024))
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("e"), 64*1024))
		os.Exit(0)
	case "env":
		_, _ = fmt.Fprintln(os.Stdout, os.Getenv("TOP_SECRET"))
		os.Exit(0)
	case "read-stdin":
		data, _ := io.ReadAll(os.Stdin)
		_, _ = fmt.Fprintf(os.Stdout, "stdin:%q", data)
		os.Exit(0)
	case "kill-self":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		os.Exit(9)
	case "spawn-child":
		spawnHelperChild(os.Getenv("HELPER_PID_FILE"))
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "spawn-orphan-exit":
		spawnHelperChild(os.Getenv("HELPER_PID_FILE"))
		os.Exit(0)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unknown helper scenario")
		os.Exit(2)
	}
}

func spawnHelperChild(pidFile string) {
	child := exec.Command(os.Args[0], helperRunFlag)
	child.Env = []string{
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_SCENARIO=sleep",
	}
	// Inherit the runner pipes so an orphan can hold them open; this exercises
	// WaitDelay and process-group cleanup.
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "spawn child:", err)
		os.Exit(3)
	}
	if pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	}
}

func helperRequest(t *testing.T, scenario string, extraEnv ...string) Request {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}

	env := []string{
		"GO_WANT_HELPER_PROCESS=1",
		"HELPER_SCENARIO=" + scenario,
	}
	env = append(env, extraEnv...)

	return Request{
		Executable: executable,
		Args:       []string{helperRunFlag, "-test.v=false"},
		Dir:        t.TempDir(),
		Env:        env,
	}
}

func TestRunExitCodes(t *testing.T) {
	runner := New()

	for _, tt := range []struct {
		scenario string
		want     int
	}{
		{scenario: "exit0", want: 0},
		{scenario: "exit42", want: 42},
	} {
		t.Run(tt.scenario, func(t *testing.T) {
			result, err := runner.Run(context.Background(), helperRequest(t, tt.scenario))
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !result.Started {
				t.Error("Started = false, want true")
			}
			if result.PID <= 0 {
				t.Errorf("PID = %d, want > 0", result.PID)
			}
			if result.ExitCode != tt.want {
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, tt.want)
			}
			if result.TimedOut || result.Canceled {
				t.Errorf("TimedOut = %t, Canceled = %t; want both false", result.TimedOut, result.Canceled)
			}
		})
	}
}

func TestRunCapturesAndTruncatesOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	req := helperRequest(t, "stdout")
	req.Stdout = &stdout
	req.Stderr = &stderr
	req.MaxOutputBytes = 4096

	result, err := New().Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	total := result.StdoutBytes + result.StderrBytes
	if total > 4096 {
		t.Errorf("captured %d bytes, want at most 4096", total)
	}
	if result.StdoutBytes+result.StderrBytes != int64(stdout.Len()+stderr.Len()) {
		t.Errorf("result counters do not match writers")
	}
	if !result.StdoutTruncated && !result.StderrTruncated {
		t.Error("no stream marked truncated")
	}
}

func TestRunTimeoutTerminatesProcess(t *testing.T) {
	req := helperRequest(t, "sleep")
	req.Timeout = 100 * time.Millisecond

	runner := New()
	runner.GracePeriod = 200 * time.Millisecond

	start := time.Now()
	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if result.Canceled {
		t.Error("Canceled = true, want false")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout took %v, want a bounded duration", elapsed)
	}
	waitProcessGone(t, result.PID, 3*time.Second)
}

func TestRunCancellationTerminatesProcess(t *testing.T) {
	req := helperRequest(t, "sleep")
	req.Timeout = 10 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := New().Run(ctx, req)
		resultCh <- result
		errCh <- err
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case result := <-resultCh:
		if err := <-errCh; err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !result.Canceled {
			t.Error("Canceled = false, want true")
		}
		waitProcessGone(t, result.PID, 3*time.Second)
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
}

func TestRunKillsProcessIgnoringTerm(t *testing.T) {
	req := helperRequest(t, "ignore-term")
	req.Timeout = 300 * time.Millisecond

	runner := New()
	runner.GracePeriod = 100 * time.Millisecond

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	waitProcessGone(t, result.PID, 3*time.Second)
}

func TestRunKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	req := helperRequest(t, "spawn-child", "HELPER_PID_FILE="+pidFile)
	req.Timeout = time.Second

	runner := New()
	runner.GracePeriod = 300 * time.Millisecond

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	waitProcessGone(t, result.PID, 3*time.Second)

	childPID := readPID(t, pidFile)
	waitProcessGone(t, childPID, 3*time.Second)
}

func TestRunKillsOrphanHoldingPipes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	req := helperRequest(t, "spawn-orphan-exit", "HELPER_PID_FILE="+pidFile)
	req.Timeout = 5 * time.Second

	runner := New()
	runner.GracePeriod = 100 * time.Millisecond

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The group is killed before the leader is reaped, so a grandchild holding
	// the pipes cannot stall the wait.
	if result.Duration >= 3*time.Second {
		t.Errorf("Duration = %v, want the orphan to be killed promptly", result.Duration)
	}
	childPID := readPID(t, pidFile)
	waitProcessGone(t, result.PID, 3*time.Second)
	waitProcessGone(t, childPID, 3*time.Second)
}

func TestRunDoesNotInheritEnvironmentOrStdin(t *testing.T) {
	t.Setenv("TOP_SECRET", "do-not-leak")

	t.Run("environment", func(t *testing.T) {
		var stdout bytes.Buffer
		req := helperRequest(t, "env")
		req.Stdout = &stdout

		if _, err := New().Run(context.Background(), req); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if strings.Contains(stdout.String(), "do-not-leak") {
			t.Errorf("target inherited the server environment: %q", stdout.String())
		}
	})

	t.Run("stdin", func(t *testing.T) {
		var stdout bytes.Buffer
		req := helperRequest(t, "read-stdin")
		req.Stdout = &stdout

		if _, err := New().Run(context.Background(), req); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if !strings.Contains(stdout.String(), `stdin:""`) {
			t.Errorf("stdout = %q, want an empty stdin", stdout.String())
		}
	})
}

func TestRunAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := New().Run(ctx, helperRequest(t, "sleep"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Started {
		t.Error("Started = true, want false")
	}
	if !result.Canceled {
		t.Error("Canceled = false, want true")
	}
	if result.PID != 0 {
		t.Errorf("PID = %d, want 0 (no process launched)", result.PID)
	}
}

func TestRunRejectsInvalidRequests(t *testing.T) {
	if _, err := New().Run(nil, helperRequest(t, "exit0")); err == nil {
		t.Error("nil context error = nil, want rejection")
	}
	if _, err := New().Run(context.Background(), Request{Executable: "relative"}); err == nil {
		t.Error("relative executable error = nil, want rejection")
	}
	if _, err := New().Run(context.Background(), Request{
		Executable: "/bin/true",
		Args:       []string{"bad\x00arg"},
	}); err == nil {
		t.Error("NUL argument error = nil, want rejection")
	}
	if _, err := New().Run(context.Background(), Request{
		Executable: filepath.Join(t.TempDir(), "missing"),
	}); err == nil {
		t.Error("missing executable error = nil, want rejection")
	}
}

func TestRunSignal(t *testing.T) {
	result, err := New().Run(context.Background(), helperRequest(t, "kill-self"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Signal != syscall.SIGKILL {
		t.Errorf("Signal = %v, want SIGKILL", result.Signal)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a signaled process", result.ExitCode)
	}
}

func TestNoLeakedProcessesAfter100Runs(t *testing.T) {
	const trueBinary = "/bin/true"
	if _, err := os.Stat(trueBinary); err != nil {
		t.Skipf("%s is not available", trueBinary)
	}

	marker := fmt.Sprintf("mcp-nd-leak-%d", time.Now().UnixNano())
	runner := New()

	for range 100 {
		result, err := runner.Run(context.Background(), Request{
			Executable: trueBinary,
			Dir:        t.TempDir(),
			Env:        []string{"LEAK_MARKER=" + marker},
			Timeout:    2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for processWithEnvMarker(marker) {
		if time.Now().After(deadline) {
			t.Fatal("a child process survived 100 runs")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid: %v", err)
	}
	return pid
}

func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still alive", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}

	// A zombie is not running; /proc reports state Z.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	stat := string(data)
	if index := strings.LastIndex(stat, ")"); index >= 0 && index+2 < len(stat) {
		return stat[index+2] != 'Z'
	}
	return true
}

func processWithEnvMarker(marker string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	needle := []byte(marker)
	for _, entry := range entries {
		if !isDigits(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		if bytes.Contains(data, needle) {
			return true
		}
	}
	return false
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func TestCaptureWriterDiscardsAfterDestinationError(t *testing.T) {
	writer := newCapture(failingWriter{}, newByteBudget(1024))
	if n, err := writer.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first Write() = %d, %v, want 3, nil", n, err)
	}
	if n, err := writer.Write([]byte("def")); n != 3 || err != nil {
		t.Fatalf("second Write() = %d, %v, want 3, nil", n, err)
	}
	if writer.Err() == nil {
		t.Error("Err() = nil, want the destination error")
	}
	if !writer.Truncated() {
		t.Error("Truncated() = false, want true after data was discarded")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
