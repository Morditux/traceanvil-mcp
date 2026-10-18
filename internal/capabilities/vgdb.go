package capabilities

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

// VgdbProbe discovers vgdb without probing --version: vgdb intentionally
// rejects --version and writes its help text to stderr. The fixed --help
// invocation is the only process executed by this probe.
type VgdbProbe struct {
	enabled bool
}

// NewVgdbProbe returns the experimental vgdb probe.
func NewVgdbProbe(enabled bool) *VgdbProbe { return &VgdbProbe{enabled: enabled} }

func (p *VgdbProbe) ID() string      { return BackendVgdb }
func (p *VgdbProbe) Backend() string { return "vgdb" }
func (p *VgdbProbe) Features() []string {
	return []string{"vgdb-monitor", "fixed-monitor-commands", "no-free-monitor"}
}

func (p *VgdbProbe) Run(ctx context.Context) Result {
	if !p.enabled {
		return Result{State: StateDisabled, Reason: "disabled by configuration", OK: true}
	}
	resolved, err := exec.LookPath("vgdb")
	if err != nil {
		if errors.Is(err, exec.ErrDot) {
			return Result{State: StateUnavailable, Reason: "vgdb resolved through a relative PATH entry", OK: true}
		}
		return Result{State: StateUnavailable, Reason: "vgdb not found on PATH", OK: true}
	}
	if !filepath.IsAbs(resolved) {
		return Result{State: StateUnavailable, Reason: "vgdb resolved through a relative PATH entry", OK: true}
	}
	detail, err := functionalCheck(ctx, resolved, []string{"--help"},
		[]string{"--vgdb-prefix", "--pid", "Usage:"})
	if err != nil {
		state := StateUnsupported
		if ctx.Err() != nil {
			state = StateUnknown
		}
		return Result{State: state, Path: resolved,
			Reason: fmt.Sprintf("vgdb help probe failed: %v", err), Detail: detail,
			OK: ctx.Err() == nil}
	}
	return Result{State: StateAvailable, Path: resolved,
		Reason: "vgdb help/options verified; fixed read-only monitor commands only", Detail: detail, OK: true}
}
