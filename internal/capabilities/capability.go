// Package capabilities records what the server can actually do on this
// machine. A capability is exposed to clients only when it is implemented,
// enabled by configuration, permitted by policy and ready at runtime.
package capabilities

import "fmt"

// State is the availability of one capability.
type State string

const (
	// StateAvailable means the capability has been functionally verified.
	StateAvailable State = "available"
	// StateUnavailable means a required executable or resource is missing.
	StateUnavailable State = "unavailable"
	// StateDisabled means configuration explicitly turned the capability off.
	StateDisabled State = "disabled"
	// StateUnsupported means the backend exists but does not support the feature.
	StateUnsupported State = "unsupported"
	// StateUnknown means the capability could not be verified.
	StateUnknown State = "unknown"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StateAvailable, StateUnavailable, StateDisabled, StateUnsupported, StateUnknown:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (s State) String() string { return string(s) }

// ProbeResult describes how a capability was inspected.
type ProbeResult struct {
	Attempted  bool   `json:"attempted"`
	Success    bool   `json:"success"`
	DurationMS int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

// Capability is one auditable availability record.
type Capability struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	Backend     string      `json:"backend,omitempty"`
	State       State       `json:"state"`
	Path        string      `json:"path,omitempty"`
	Version     string      `json:"version,omitempty"`
	Features    []string    `json:"features,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	LastChecked string      `json:"last_checked"`
	Probe       ProbeResult `json:"probe"`
}

// Validate checks the record's invariants.
func (c Capability) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("capability: id must not be empty")
	}
	if !c.State.Valid() {
		return fmt.Errorf("capability %q: invalid state %q", c.ID, c.State)
	}
	if c.Kind == "" {
		return fmt.Errorf("capability %q: kind must not be empty", c.ID)
	}
	return nil
}
