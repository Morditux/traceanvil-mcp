package capabilities

import (
	"context"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/policy"
)

// SanitizerProbe reports whether sanitizer runs are configured. It cannot
// prove that a target binary is instrumented without running it, so it trusts
// the target's explicit sanitizer declarations and records that limitation.
type SanitizerProbe struct {
	enabled  bool
	resolver *policy.Resolver
	tool     string
}

// NewSanitizerProbe returns a per-runtime sanitizer capability probe backed by
// the same policy resolver used at invocation. It resolves targets read-only
// and never executes them.
func NewSanitizerProbe(enabled bool, resolver *policy.Resolver, tool string) *SanitizerProbe {
	return &SanitizerProbe{enabled: enabled, resolver: resolver, tool: tool}
}

// ID implements Probe.
func (p *SanitizerProbe) ID() string { return "backend.sanitizer." + p.tool }

// Backend implements Probe.
func (p *SanitizerProbe) Backend() string { return "sanitizer" }

// Features implements Probe.
func (p *SanitizerProbe) Features() []string {
	return []string{p.tool, "log-parsing", "instrumented-target-declaration"}
}

// Run implements Probe. No user program is executed.
func (p *SanitizerProbe) Run(context.Context) Result {
	if !p.enabled {
		return Result{State: StateDisabled, Reason: "disabled by configuration", OK: true}
	}
	declared := make(map[string]bool)
	if p.resolver != nil {
		for _, targetID := range p.resolver.TargetIDs() {
			resolved, err := p.resolver.Resolve(targetID, nil)
			if err != nil {
				continue
			}
			for _, name := range resolved.Sanitizers {
				if name == p.tool && config.ValidSanitizer(name) {
					declared[name] = true
				}
			}
		}
	}
	if len(declared) == 0 {
		return Result{
			State:  StateUnavailable,
			Reason: "no usable target declares a sanitizer build",
			OK:     true,
		}
	}
	if !declared[p.tool] {
		return Result{
			State:  StateUnavailable,
			Reason: "no usable target declares sanitizer " + p.tool,
			OK:     true,
		}
	}
	return Result{
		State:  StateAvailable,
		Reason: "at least one usable target declares sanitizer " + p.tool,
		Detail: "runtime instrumentation is trusted from target declarations; no user program was run",
		OK:     true,
	}
}
