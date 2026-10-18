package capabilities

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/dap"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
)

// DAPProbe verifies that a debug adapter accepts the DAP initialize handshake.
// It never launches a user program: the adapter is started with fixed arguments
// and stopped after the handshake.
type DAPProbe struct {
	id        string
	backend   string
	binary    string
	adapterID string
	args      []string
	enabled   bool
	features  []string
	note      string
}

// NewGDBProbe returns the GDB DAP availability probe. It starts GDB with
// --nx (no init files) and --interpreter=dap, then verifies the initialize
// response. No target or user configuration is involved.
func NewGDBProbe(enabled bool) *DAPProbe {
	return &DAPProbe{
		id:        BackendGDB,
		backend:   "gdb",
		binary:    "gdb",
		adapterID: "gdb",
		args:      append([]string{"--quiet", "--nx", "--interpreter=dap"}, debugger.GDBHardeningArguments()...),
		enabled:   enabled,
		features: []string{
			"launch", "breakpoints", "continue", "pause", "step",
			"threads", "stack", "scopes", "variables", "terminate",
		},
	}
}

// WithNote attaches a human-readable explanation to the discovery result.
func (p *DAPProbe) WithNote(note string) *DAPProbe {
	p.note = note
	return p
}

// ID implements Probe.
func (p *DAPProbe) ID() string { return p.id }

// Backend implements Probe.
func (p *DAPProbe) Backend() string { return p.backend }

// Features implements Probe.
func (p *DAPProbe) Features() []string { return append([]string(nil), p.features...) }

// Run implements Probe.
func (p *DAPProbe) Run(ctx context.Context) Result {
	if !p.enabled {
		return Result{
			State:  StateDisabled,
			Reason: "disabled by configuration",
			OK:     true,
		}
	}

	resolved, err := exec.LookPath(p.binary)
	if err != nil {
		state := StateUnavailable
		reason := fmt.Sprintf("%s not found on PATH", p.binary)
		if errors.Is(err, exec.ErrDot) {
			reason = fmt.Sprintf("%s resolved through a relative PATH entry", p.binary)
		}
		return Result{State: state, Reason: reason, OK: true}
	}
	if !filepath.IsAbs(resolved) {
		return Result{
			State:  StateUnavailable,
			Reason: fmt.Sprintf("%s resolved through a relative PATH entry", p.binary),
			OK:     true,
		}
	}

	version, err := binaryVersion(ctx, resolved)
	if err != nil {
		if ctx.Err() != nil {
			return Result{State: StateUnknown, Path: resolved, Reason: "version probe timed out", OK: false}
		}
		return Result{
			State:  StateUnknown,
			Path:   resolved,
			Reason: fmt.Sprintf("binary found but version probe failed: %v", err),
			Detail: err.Error(),
			OK:     false,
		}
	}

	process, err := startProbeProcess(resolved, p.args)
	if err != nil {
		return Result{
			State:   StateUnknown,
			Path:    resolved,
			Version: version,
			Reason:  fmt.Sprintf("cannot start %s for a DAP probe: %v", p.binary, err),
			Detail:  err.Error(),
			OK:      false,
		}
	}
	defer process.Close()

	client, err := dap.New(process, dap.Options{
		ClientID:           "mcp-native-diagnostics-probe",
		ClientName:         "capability probe",
		WriteTimeout:       time.Second,
		EventBuffer:        16,
		MaxPendingRequests: 4,
		ReverseRequestHandler: func(context.Context, dap.ReverseRequest) (any, error) {
			return nil, errors.New("reverse requests are not accepted during a capability probe")
		},
	})
	if err != nil {
		return Result{
			State:   StateUnknown,
			Path:    resolved,
			Version: version,
			Reason:  fmt.Sprintf("cannot initialize DAP client: %v", err),
			OK:      false,
		}
	}

	initializeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	capabilities, err := client.Initialize(initializeCtx, dap.InitializeRequestArguments{
		AdapterID: p.adapterID,
	})
	closeErr := client.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		detail := process.Stderr()
		state := StateUnknown
		reason := fmt.Sprintf("DAP initialize failed: %v", err)
		if errors.Is(err, dap.ErrClosed) || errors.Is(err, dap.ErrEOF) {
			state = StateUnsupported
			reason = "adapter exited before completing the DAP initialize handshake"
		}
		var responseErr *dap.ResponseError
		if errors.As(err, &responseErr) {
			state = StateUnsupported
		}
		if detail != "" && strings.Contains(strings.ToLower(detail), "unrecognized") {
			state = StateUnsupported
			reason = "adapter rejected its DAP launch arguments"
		}
		return Result{
			State:   state,
			Path:    resolved,
			Version: version,
			Reason:  reason,
			Detail:  truncate(detail, 500),
			OK:      initializeCtx.Err() == nil,
		}
	}

	if err := debugger.ValidateGDBHardening(process.Stderr()); err != nil {
		return Result{
			State:   StateUnsupported,
			Path:    resolved,
			Version: version,
			Reason:  "GDB rejected a required hardening command",
			Detail:  truncate(err.Error(), 500),
			OK:      true,
		}
	}

	detail := fmt.Sprintf("DAP initialize succeeded; configurationDone=%t",
		capabilities.SupportsConfigurationDoneRequest)
	if p.note != "" {
		detail += "; " + p.note
	}
	return Result{
		State:   StateAvailable,
		Path:    resolved,
		Version: version,
		Reason:  discoveryReason("DAP initialize verified", p.note),
		Detail:  detail,
		OK:      true,
	}
}

// probeProcess is the byte stream and process cleanup for one DAP probe.
type probeProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *boundedBuffer

	done      chan struct{}
	closeOnce sync.Once
}

func startProbeProcess(path string, args []string) (*probeProcess, error) {
	home, err := os.MkdirTemp("", "mcp-dap-probe-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}

	cmd := exec.Command(path, args...)
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"HOME=" + home,
		"TMPDIR=" + home,
	}
	cmd.WaitDelay = 250 * time.Millisecond

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(home)
		return nil, err
	}
	// Avoid StdoutPipe: cmd.Wait would close the read side when the adapter
	// exits and could race the DAP reader. Keep the read side owned by the
	// probe process instead.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = os.RemoveAll(home)
		return nil, err
	}
	stderr := &boundedBuffer{limit: probeOutputLimit}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = os.RemoveAll(home)
		return nil, err
	}
	_ = stdoutWriter.Close()

	process := &probeProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		done:   make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(process.done)
	}()
	return process, nil
}

func (p *probeProcess) Read(data []byte) (int, error)  { return p.stdout.Read(data) }
func (p *probeProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }

// Close closes the pipes and reaps the probe process. A process that does not
// exit after its standard input closes is killed. It is idempotent.
func (p *probeProcess) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		_ = p.stdout.Close()

		select {
		case <-p.done:
		case <-time.After(300 * time.Millisecond):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			select {
			case <-p.done:
			case <-time.After(time.Second):
			}
		}

		home := ""
		for _, entry := range p.cmd.Env {
			if strings.HasPrefix(entry, "HOME=") {
				home = strings.TrimPrefix(entry, "HOME=")
			}
		}
		if home != "" {
			_ = os.RemoveAll(home)
		}
	})
	return nil
}

// Stderr returns the bounded adapter stderr tail.
func (p *probeProcess) Stderr() string { return p.stderr.String() }
