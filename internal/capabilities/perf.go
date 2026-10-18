package capabilities

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// BackendPerf is the experimental Linux perf capability. It is available only
// when perf is configured, installed, and an unprivileged permission probe
// succeeds. The server never raises perf_event_paranoid or uses sudo.
const BackendPerf = "backend.perf"

// PerfProbe checks perf discovery and the local permission ceiling. It never
// runs a declared target; the permission probe uses the fixed system /bin/true.
type PerfProbe struct {
	enabled bool
	binary  *BinaryProbe
}

// NewPerfProbe creates the experimental perf probe.
func NewPerfProbe(enabled bool) *PerfProbe {
	return &PerfProbe{
		enabled: enabled,
		binary: NewBinaryProbe(BackendPerf, "perf", "perf", enabled,
			[]string{"perf-stat", "allowlisted-events", "json-output"}).
			WithNote("perf permissions are checked without sudo or sysctl changes"),
	}
}

func (p *PerfProbe) ID() string         { return BackendPerf }
func (p *PerfProbe) Backend() string    { return "perf" }
func (p *PerfProbe) Features() []string { return p.binary.Features() }

func (p *PerfProbe) Run(ctx context.Context) Result {
	if !p.enabled {
		return Result{State: StateDisabled, Reason: "disabled by configuration", OK: true}
	}
	base := p.binary.Run(ctx)
	if base.State != StateUnknown {
		return base
	}
	if base.Path == "" {
		return base
	}
	scope, caps, err := perfPermissions()
	if err != nil {
		return Result{State: StateUnknown, Path: base.Path, Version: base.Version,
			Reason: "perf permission metadata is unavailable: " + err.Error(), OK: false}
	}
	if scope >= 3 && !caps.perfmon && !caps.sysAdmin {
		return Result{State: StateUnavailable, Path: base.Path, Version: base.Version,
			Reason: fmt.Sprintf("perf_event_paranoid=%d and no CAP_PERFMON/CAP_SYS_ADMIN; the server does not escalate", scope),
			OK:     true}
	}
	truePath := "/bin/true"
	if _, err := os.Stat(truePath); err != nil {
		truePath = "/usr/bin/true"
		if _, err := os.Stat(truePath); err != nil {
			return Result{State: StateUnknown, Path: base.Path, Version: base.Version,
				Reason: "perf permission probe has no fixed true binary", OK: false}
		}
	}
	args := []string{"stat", "-j", "-e", "task-clock,cycles,instructions", "--", truePath}
	output, err := runPerfProbe(ctx, base.Path, args)
	if err != nil {
		state := StateUnsupported
		if ctx.Err() != nil {
			state = StateUnknown
		}
		return Result{State: state, Path: base.Path, Version: base.Version,
			Reason: "perf permission/event probe failed: " + err.Error(),
			Detail: firstLine(output), OK: ctx.Err() == nil}
	}
	missing := missingPerfProbeEvents(output)
	if len(missing) > 0 {
		return Result{State: StateUnsupported, Path: base.Path, Version: base.Version,
			Reason: "perf permission succeeded but fixed counters are unavailable: " + strings.Join(missing, ", "),
			Detail: firstLine(output), OK: true}
	}
	return Result{State: StateAvailable, Path: base.Path, Version: base.Version,
		Reason: "perf permission and fixed task-clock/cycles/instructions counters verified without escalation",
		Detail: firstLine(output), OK: true}
}

func runPerfProbe(ctx context.Context, path string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LC_ALL=C", "LANG=C"}
	cmd.WaitDelay = 250 * time.Millisecond
	stdout := &boundedBuffer{limit: probeOutputLimit}
	stderr := &boundedBuffer{limit: probeOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	combined := stdout.String()
	if stderr.String() != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr.String()
	}
	if ctx.Err() != nil {
		return combined, ctx.Err()
	}
	if err != nil {
		return combined, fmt.Errorf("%w: %s", err, firstLine(combined))
	}
	return combined, nil
}

func missingPerfProbeEvents(output string) []string {
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 4096), probeOutputLimit)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var raw struct {
			Event string          `json:"event"`
			Value json.RawMessage `json:"counter-value"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil || raw.Event == "" {
			continue
		}
		text := strings.Trim(string(raw.Value), `"`)
		if strings.Contains(text, "not supported") || strings.Contains(text, "not counted") || text == "" {
			continue
		}
		seen[raw.Event] = true
	}
	var missing []string
	for _, event := range []string{"task-clock", "cycles", "instructions"} {
		if !seen[event] {
			missing = append(missing, event)
		}
	}
	return missing
}

type perfCapabilities struct {
	perfmon  bool
	sysAdmin bool
}

func perfPermissions() (int, perfCapabilities, error) {
	data, err := os.ReadFile("/proc/sys/kernel/perf_event_paranoid")
	if err != nil {
		return 0, perfCapabilities{}, err
	}
	scope, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, perfCapabilities{}, err
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, perfCapabilities{}, err
	}
	var caps perfCapabilities
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		value, err := strconv.ParseUint(fields[1], 16, 64)
		if err != nil {
			return 0, perfCapabilities{}, err
		}
		caps.perfmon = value&(1<<38) != 0
		caps.sysAdmin = value&(1<<21) != 0
	}
	return scope, caps, nil
}
