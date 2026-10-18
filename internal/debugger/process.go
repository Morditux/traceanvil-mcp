package debugger

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// gdbBaseArgs is the fixed GDB invocation. The server never accepts additional
// GDB flags, scripts or commands from an MCP client.
var gdbBaseArgs = []string{"--quiet", "--nx", "--interpreter=dap"}

// gdbHardeningArgs disables user init/auto-load scripts, debuginfod and shell
// startup, and prevents the inferior from touching the DAP pipe. The same list
// is used by the capability probe so a backend that rejects hardening is not
// announced as available.
var gdbHardeningArgs = []string{
	"-ex", "set startup-with-shell off",
	"-ex", "set confirm off",
	"-ex", "set pagination off",
	"-ex", "set auto-load python-scripts off",
	"-ex", "set auto-load gdb-scripts off",
	"-ex", "set auto-load local-gdbinit off",
	"-ex", "set debuginfod enabled off",
	"-ex", "set history save off",
	// The debuggee must never read from or write to the DAP pipe. /dev/null
	// gives GDB's inferior a deterministic, non-interactive stdin/stdout.
	// LLDB uses a private PTY instead; neither terminal is exposed over MCP.
	"-ex", "set inferior-tty /dev/null",
}

func gdbCommandArgs() []string {
	args := make([]string, 0, len(gdbBaseArgs)+len(gdbHardeningArgs))
	args = append(args, gdbBaseArgs...)
	args = append(args, gdbHardeningArgs...)
	return args
}

// lldbHardeningArgs disables the user/cwd .lldbinit loading and debuginfod
// symbol lookup. The user init file is also unreachable through the private
// HOME used by the session. lldb-dap itself is started without client flags.
var lldbHardeningArgs = []string{
	"-c", "settings set target.load-cwd-lldbinit false",
	"-c", "settings set symbols.enable-external-lookup false",
}

func lldbCommandArgs() []string {
	return append([]string(nil), lldbHardeningArgs...)
}

// LLDBHardeningArguments returns a copy of the debugger hardening arguments
// for the capability probe. It is not a client-facing API.
func LLDBHardeningArguments() []string {
	return append([]string(nil), lldbHardeningArgs...)
}

// GDBHardeningArguments returns a copy of the hardening arguments for the
// capability probe. It is not a client-facing API.
func GDBHardeningArguments() []string {
	return append([]string(nil), gdbHardeningArgs...)
}

// ValidateGDBHardening reports whether GDB rejected a hardening command. It is
// used both at session startup and by the capability probe.
func ValidateGDBHardening(stderr string) error {
	lower := strings.ToLower(stderr)
	for _, marker := range []string{"undefined command", "undefined set command", "unrecognized command"} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("GDB rejected a hardening command: %s", truncateText(strings.TrimSpace(stderr), 500))
		}
	}
	return nil
}

// debugProcess owns one debugger process (GDB or lldb-dap) and its stdio pipes.
// It runs in its own process group so shutdown can terminate the adapter and
// helpers that remain in that group. The inferiors may be in a different process
// group; debuggee termination therefore relies on the adapter's disconnect and
// teardown behavior (and verified PID checks), not on the debugger group alone.
type debugProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer

	closeOnce sync.Once
	done      chan struct{}
	waitErr   error
}

func startDebugProcess(binary string, args []string, dir, home string) (*debugProcess, error) {
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"TERM=dumb",
		"HOME=" + home,
		"TMPDIR=" + home,
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 500 * time.Millisecond

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Use an explicit pipe instead of StdoutPipe. exec.Cmd.Wait closes a
	// StdoutPipe as soon as the process exits, which can race the DAP reader and
	// lose the final response or terminated event. With an os.Pipe, Wait cannot
	// close the read side; EOF arrives only after the debugger closes its
	// inherited writer.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := newTailBuffer(8 << 10)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, err
	}
	_ = stdoutWriter.Close()

	process := &debugProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go func() {
		process.waitErr = cmd.Wait()
		close(process.done)
	}()
	return process, nil
}

// Read reads adapter DAP bytes from the debugger stdout.
func (p *debugProcess) Read(data []byte) (int, error) { return p.stdout.Read(data) }

// Write writes adapter DAP bytes to the debugger stdin.
func (p *debugProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }

// Close closes the stdio pipes. It is the transport Close used by the DAP
// client and does not by itself terminate the process; session shutdown calls
// Terminate or Detach explicitly.
func (p *debugProcess) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdout.Close()
	})
	return nil
}

// Done is closed when the debugger process exits.
func (p *debugProcess) Done() <-chan struct{} { return p.done }

// Exited reports whether the debugger process already exited.
func (p *debugProcess) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Wait waits up to timeout for the debugger to exit and returns its wait error.
func (p *debugProcess) Wait(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(timeout):
		return errors.New("debugger process did not exit before the deadline")
	}
}

// Stderr returns the bounded debugger stderr tail.
func (p *debugProcess) Stderr() string { return p.stderr.String() }

// Terminate terminates the debugger and any helpers remaining in its process
// group: TERM first, then KILL after a short grace period. An inferior in a
// different process group is normally stopped by the adapter's disconnect.
func (p *debugProcess) Terminate() {
	if p.Exited() {
		return
	}
	pid := p.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-p.done:
		return
	case <-time.After(250 * time.Millisecond):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		_ = p.Wait(2 * time.Second)
	}
}

// Detach releases GDB without signaling the debuggee's process group. GDB
// itself is killed if closing its pipes did not make it exit; the debuggee is
// intentionally left running.
func (p *debugProcess) Detach() {
	_ = p.Close()
	select {
	case <-p.done:
		return
	case <-time.After(250 * time.Millisecond):
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.Wait(2 * time.Second)
}

// tailBuffer keeps the last limit bytes written by a child process. It never
// blocks the child and is safe to read after the process exits.
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer {
	if limit <= 0 {
		limit = 4 << 10
	}
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if over := len(b.data) + len(p) - b.limit; over > 0 {
		copy(b.data, b.data[over:])
		b.data = b.data[:len(b.data)-over]
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.data...))
}

// removeProcessTemp removes a session temporary directory best-effort.
func removeProcessTemp(path string) {
	if path != "" {
		_ = os.RemoveAll(path)
	}
}
