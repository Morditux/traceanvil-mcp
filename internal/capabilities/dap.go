package capabilities

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	binaries  []string
	discover  func() []string
	adapterID string
	args      []string
	enabled   bool
	features  []string
	note      string

	// versionRequired rejects a candidate when --version fails. lldb-dap is
	// probed with a timeout and an optional version because older adapters do
	// not guarantee terminating --version behavior; initialization is the
	// functional proof in either case.
	versionRequired bool
	// hardeningCheck rejects a candidate when its stderr proves a hardening
	// command was not understood. Nil means the fixed arguments are sufficient.
	hardeningCheck func(stderr string) error
}

// NewGDBProbe returns the GDB DAP availability probe. It starts GDB with
// --nx (no init files) and --interpreter=dap, then verifies the initialize
// response. No target or user configuration is involved.
func NewGDBProbe(enabled bool) *DAPProbe {
	return &DAPProbe{
		id:              BackendGDB,
		backend:         "gdb",
		binaries:        []string{"gdb"},
		adapterID:       "gdb",
		args:            append([]string{"--quiet", "--nx", "--interpreter=dap"}, debugger.GDBHardeningArguments()...),
		enabled:         enabled,
		versionRequired: true,
		hardeningCheck:  debugger.ValidateGDBHardening,
		features: []string{
			"launch", "breakpoints", "continue", "pause", "step",
			"threads", "stack", "scopes", "variables", "terminate",
		},
	}
}

// NewLLDBProbe returns the lldb-dap availability probe. It discovers
// "lldb-dap" and versioned "lldb-dap-<N>" executables in absolute PATH
// directories, then verifies a DAP initialize handshake. No target is loaded.
func NewLLDBProbe(enabled bool) *DAPProbe {
	return &DAPProbe{
		id:              BackendLLDB,
		backend:         "lldb",
		discover:        discoverLLDBDAP,
		adapterID:       "lldb",
		args:            debugger.LLDBHardeningArguments(),
		enabled:         enabled,
		versionRequired: false,
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

	candidates := p.candidates()
	if len(candidates) == 0 {
		return Result{
			State:  StateUnavailable,
			Reason: fmt.Sprintf("%s not found on an absolute PATH entry", p.backend),
			OK:     true,
		}
	}

	var last Result
	for _, resolved := range candidates {
		if err := ctx.Err(); err != nil {
			return Result{State: StateUnknown, Path: resolved, Reason: "capability probe was canceled", OK: false}
		}
		if !filepath.IsAbs(resolved) {
			last = Result{
				State:  StateUnavailable,
				Reason: fmt.Sprintf("%s resolved through a relative PATH entry", resolved),
				OK:     true,
			}
			continue
		}

		version := ""
		versionErr := error(nil)
		if p.versionRequired {
			version, versionErr = binaryVersion(ctx, resolved)
			if versionErr != nil {
				last = Result{
					State:  StateUnknown,
					Path:   resolved,
					Reason: fmt.Sprintf("binary found but version probe failed: %v", versionErr),
					Detail: versionErr.Error(),
					OK:     false,
				}
				continue
			}
		} else {
			// Do not let an adapter whose --version does not terminate consume
			// the whole probe budget before the functional DAP attempt.
			versionCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			version, _ = binaryVersion(versionCtx, resolved)
			cancel()
			if ctx.Err() != nil {
				return Result{State: StateUnknown, Path: resolved, Reason: "capability probe was canceled", OK: false}
			}
		}

		result := p.probeCandidate(ctx, resolved, version)
		if result.State == StateAvailable {
			return result
		}
		last = result
	}
	return last
}

func (p *DAPProbe) candidates() []string {
	if p.discover != nil {
		return dedupeAbsolute(p.discover())
	}
	var paths []string
	for _, name := range p.binaries {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		paths = append(paths, path)
	}
	return dedupeAbsolute(paths)
}

func (p *DAPProbe) probeCandidate(ctx context.Context, resolved, version string) Result {
	process, err := startProbeProcess(resolved, p.args)
	if err != nil {
		return Result{
			State:   StateUnknown,
			Path:    resolved,
			Version: version,
			Reason:  fmt.Sprintf("cannot start %s for a DAP probe: %v", p.backend, err),
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

	if p.hardeningCheck != nil {
		if err := p.hardeningCheck(process.Stderr()); err != nil {
			return Result{
				State:   StateUnsupported,
				Path:    resolved,
				Version: version,
				Reason:  p.backend + " rejected a required hardening command",
				Detail:  truncate(err.Error(), 500),
				OK:      true,
			}
		}
	}

	detail := fmt.Sprintf("DAP initialize succeeded; configurationDone=%t",
		capabilities.SupportsConfigurationDoneRequest)
	if p.backend == BackendLLDB {
		detail += "; fixed pre-init hardening settings are not independently verified by lldb-dap"
	}
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

// discoverLLDBDAP returns lldb-dap and versioned lldb-dap-<N> candidates from
// absolute PATH directories. The unsuffixed name is tried first, then
// version-suffixed names in descending numeric order (newest first). Relative
// PATH entries are skipped rather than aborting the search. At most
// maxLLDBCandidates are returned so a few broken adapters cannot consume the
// whole capability probe budget.
func discoverLLDBDAP() []string {
	var plain, versioned []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(dir, name)
			if !isExecutableRegular(path) {
				continue
			}
			switch {
			case name == "lldb-dap":
				plain = append(plain, path)
			case strings.HasPrefix(name, "lldb-dap-") && allDigits(strings.TrimPrefix(name, "lldb-dap-")):
				versioned = append(versioned, path)
			}
		}
	}
	sort.SliceStable(versioned, func(i, j int) bool {
		return lldbVersionNumber(versioned[i]) > lldbVersionNumber(versioned[j])
	})
	candidates := dedupeAbsolute(append(plain, versioned...))
	if len(candidates) > maxLLDBCandidates {
		candidates = candidates[:maxLLDBCandidates]
	}
	return candidates
}

const maxLLDBCandidates = 2

func lldbVersionNumber(path string) int {
	name := filepath.Base(path)
	number, err := strconv.Atoi(strings.TrimPrefix(name, "lldb-dap-"))
	if err != nil {
		return 0
	}
	return number
}

func isExecutableRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func allDigits(value string) bool {
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

func dedupeAbsolute(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	return result
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
		case <-time.After(100 * time.Millisecond):
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			select {
			case <-p.done:
			case <-time.After(500 * time.Millisecond):
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
