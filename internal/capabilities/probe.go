package capabilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Result is the outcome of one probe. OK reports whether the probe mechanism
// itself completed; a probe may complete successfully while the capability
// remains unknown (for example, a binary was found but its functionality was
// not verified).
type Result struct {
	State   State
	Path    string
	Version string
	Reason  string
	Detail  string
	OK      bool
}

// Probe inspects one capability. Implementations must not run user programs.
type Probe interface {
	ID() string
	Backend() string
	Features() []string
	Run(ctx context.Context) Result
}

// BinaryProbe discovers an executable and, optionally, its version.
//
// At this milestone a discovered binary is reported as StateUnknown: finding
// an executable does not prove that its analysis features work. Functional
// probes are added with their backend milestones.
type BinaryProbe struct {
	id        string
	backend   string
	binary    string
	enabled   bool
	features  []string
	note      string
	noVersion bool

	checkArgs     []string
	checkContains []string
	checkNote     string
}

// NewBinaryProbe returns a probe for one external executable.
func NewBinaryProbe(id, backend, binary string, enabled bool, features []string) *BinaryProbe {
	return &BinaryProbe{
		id:       id,
		backend:  backend,
		binary:   binary,
		enabled:  enabled,
		features: append([]string(nil), features...),
	}
}

// WithNote attaches a human-readable explanation to successful discovery.
func (p *BinaryProbe) WithNote(note string) *BinaryProbe {
	p.note = note
	return p
}

// WithoutVersionProbe skips executing the binary during discovery. It is used
// for adapters such as lldb-dap whose --version behaviour is not guaranteed.
func (p *BinaryProbe) WithoutVersionProbe() *BinaryProbe {
	p.noVersion = true
	return p
}

// WithFunctionalCheck verifies a feature without running a user program. The
// binary is executed with a fixed argument list and every required substring
// must appear in its output. Success promotes the capability to available;
// failure reports unsupported (or unknown for infrastructure failures).
func (p *BinaryProbe) WithFunctionalCheck(args []string, required []string) *BinaryProbe {
	p.checkArgs = append([]string(nil), args...)
	p.checkContains = append([]string(nil), required...)
	return p
}

// ID implements Probe.
func (p *BinaryProbe) ID() string { return p.id }

// Backend implements Probe.
func (p *BinaryProbe) Backend() string { return p.backend }

// Features implements Probe.
func (p *BinaryProbe) Features() []string { return append([]string(nil), p.features...) }

// Run implements Probe.
func (p *BinaryProbe) Run(ctx context.Context) Result {
	if !p.enabled {
		return Result{
			State:  StateDisabled,
			Reason: "disabled by configuration",
			OK:     true,
		}
	}

	resolved, err := exec.LookPath(p.binary)
	if err != nil {
		if errors.Is(err, exec.ErrDot) {
			return Result{
				State:  StateUnavailable,
				Reason: fmt.Sprintf("%s resolved through a relative PATH entry", p.binary),
				OK:     true,
			}
		}
		return Result{
			State:  StateUnavailable,
			Reason: fmt.Sprintf("%s not found on PATH", p.binary),
			OK:     true,
		}
	}
	// Reject relative PATH entries so a binary planted in the process working
	// directory cannot be picked up implicitly.
	if !filepath.IsAbs(resolved) {
		return Result{
			State:  StateUnavailable,
			Reason: fmt.Sprintf("%s resolved through a relative PATH entry", p.binary),
			OK:     true,
		}
	}

	if p.noVersion {
		return Result{
			State:  StateUnknown,
			Path:   resolved,
			Reason: discoveryReason("binary detected but not executed", p.note),
			OK:     true,
		}
	}

	version, err := binaryVersion(ctx, resolved)
	if err != nil {
		return Result{
			State:  StateUnknown,
			Path:   resolved,
			Reason: fmt.Sprintf("binary found but version probe failed: %v", err),
			Detail: err.Error(),
			OK:     false,
		}
	}

	if len(p.checkArgs) == 0 {
		return Result{
			State:   StateUnknown,
			Path:    resolved,
			Version: version,
			Reason:  discoveryReason("binary detected; functional support not verified", p.note),
			OK:      true,
		}
	}

	detail, err := functionalCheck(ctx, resolved, p.checkArgs, p.checkContains)
	if err != nil {
		// Only a definite negative answer (non-zero exit or missing required
		// output) proves lack of support; start failures and other
		// infrastructure errors leave the state unknown.
		state := StateUnknown
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) || errors.Is(err, errMissingOutput) {
			state = StateUnsupported
		}
		if ctx.Err() != nil {
			state = StateUnknown
		}
		return Result{
			State:   state,
			Path:    resolved,
			Version: version,
			Reason:  fmt.Sprintf("binary found but functional check failed: %v", err),
			Detail:  detail,
			OK:      ctx.Err() == nil,
		}
	}

	return Result{
		State:   StateAvailable,
		Path:    resolved,
		Version: version,
		Reason:  discoveryReason("binary and requested feature verified", p.note),
		Detail:  detail,
		OK:      true,
	}
}

// functionalCheck runs one fixed, bounded command and verifies required
// substrings in its combined output. It never runs a user program.
func functionalCheck(ctx context.Context, path string, args, required []string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	cmd.WaitDelay = 250 * time.Millisecond

	stdout := &boundedBuffer{limit: probeOutputLimit}
	stderr := &boundedBuffer{limit: probeOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = firstLine(stdout.String())
		}
		if detail == "" {
			return "", err
		}
		return detail, fmt.Errorf("%w: %s", err, detail)
	}

	combined := stdout.String() + "\n" + stderr.String()
	for _, expected := range required {
		if !strings.Contains(combined, expected) {
			return "missing " + expected, fmt.Errorf("%w: %q", errMissingOutput, expected)
		}
	}
	return truncate(firstLine(combined), versionMaxLength), nil
}

func discoveryReason(base, note string) string {
	if note == "" {
		return base
	}
	return base + " (" + note + ")"
}

var errMissingOutput = errors.New("output does not contain required text")

const (
	probeOutputLimit = 64 << 10
	versionMaxLength = 200
)

func binaryVersion(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	// A deterministic, minimal environment avoids locale-dependent output and
	// avoids inheriting unrelated secrets.
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	// Bound the I/O wait even if a grandchild process keeps the pipes open after
	// the probe context expires.
	cmd.WaitDelay = 250 * time.Millisecond

	stdout := &boundedBuffer{limit: probeOutputLimit}
	stderr := &boundedBuffer{limit: probeOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = firstLine(stdout.String())
		}
		if detail == "" {
			return "", err
		}
		return "", fmt.Errorf("%v: %s", err, detail)
	}

	line := firstLine(stdout.String())
	if line == "" {
		line = firstLine(stderr.String())
	}
	if line == "" {
		return "", errors.New("empty version output")
	}
	return truncate(line, versionMaxLength), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		s = s[:index]
	}
	// Tool output is untrusted text: drop terminal control characters before it
	// reaches doctor output or an MCP response.
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// boundedBuffer accepts at most limit bytes and reports successful writes so
// the child process is never blocked by an unbounded pipe.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// probeTimeout is the default bound for a single capability probe.
const probeTimeout = 5 * time.Second
