// Package diagnostic defines the canonical, versioned diagnostic model shared
// by analysis backends. It is deliberately free of tool-specific parsing: a
// backend preserves what the tool reported, while this package owns the stable
// JSON shape, normalization helpers and fingerprints.
package diagnostic

import (
	"errors"
	"fmt"
	"strings"
)

// SchemaVersion versions the canonical report and diagnostic payloads.
const SchemaVersion = 1

// FingerprintAlgorithmV1 identifies the fingerprint recipe implemented by this
// package. Changing the recipe requires a new identifier.
const FingerprintAlgorithmV1 = "sha256-v1"

// Result classifies the analysis outcome independently of the job state.
type Result string

const (
	// ResultErrorsFound means a complete analysis reported at least one error.
	ResultErrorsFound Result = "errors_found"
	// ResultNoErrorsObserved means a complete analysis reported no unsuppressed
	// errors. It is never used for partial data.
	ResultNoErrorsObserved Result = "no_errors_observed"
	// ResultIncomplete means evidence is partial (truncation, timeout, limits).
	ResultIncomplete Result = "incomplete"
	// ResultToolError means the analysis tool failed before producing usable
	// evidence.
	ResultToolError Result = "tool_error"
	// ResultPolicyDenied means the request was denied before execution.
	ResultPolicyDenied Result = "policy_denied"
)

// Valid reports whether r is a known result.
func (r Result) Valid() bool {
	switch r {
	case ResultErrorsFound, ResultNoErrorsObserved, ResultIncomplete, ResultToolError, ResultPolicyDenied:
		return true
	default:
		return false
	}
}

// Severity is the server's documented policy classification, not a claim made
// by the analysis tool.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityError, SeverityWarning, SeverityInfo:
		return true
	default:
		return false
	}
}

// Confidence distinguishes observed tool output from server inference. J3 only
// emits tool_reported values; the field exists so later milestones cannot blur
// the distinction.
type Confidence string

const (
	ConfidenceToolReported Confidence = "tool_reported"
	ConfidenceInference    Confidence = "inference"
)

// Report is the canonical analysis document stored as analysis.json.
type Report struct {
	SchemaVersion    int            `json:"schema_version"`
	RunID            string         `json:"run_id"`
	Tool             Tool           `json:"tool"`
	Result           Result         `json:"result"`
	AnalysisComplete bool           `json:"analysis_complete"`
	Program          Program        `json:"program"`
	Metadata         Metadata       `json:"metadata"`
	Options          map[string]any `json:"options,omitempty"`
	Summary          Summary        `json:"summary"`
	Diagnostics      []Diagnostic   `json:"diagnostics"`
	Suppressions     []Suppression  `json:"suppressions,omitempty"`
	Artifacts        []Artifact     `json:"artifacts"`
	Warnings         []string       `json:"warnings"`
}

// Tool identifies the analysis engine, its resolved path and version.
type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// Program records the instrumented target and its observed termination.
type Program struct {
	TargetID   string   `json:"target_id"`
	Path       string   `json:"path"`
	SHA256     string   `json:"sha256,omitempty"`
	Args       []string `json:"args"`
	ExitCode   int      `json:"exit_code"`
	Signal     int      `json:"signal,omitempty"`
	TimedOut   bool     `json:"timed_out"`
	Canceled   bool     `json:"canceled"`
	DurationMS int64    `json:"duration_ms"`
}

// Metadata records provenance for reproducibility.
type Metadata struct {
	StartTime            string   `json:"start_time,omitempty"`
	EndTime              string   `json:"end_time,omitempty"`
	DurationMS           int64    `json:"duration_ms"`
	OS                   string   `json:"os"`
	Arch                 string   `json:"arch"`
	WorkspaceRoots       []string `json:"workspace_roots,omitempty"`
	FingerprintAlgorithm string   `json:"fingerprint_algorithm"`
	ParserVersion        string   `json:"parser_version"`
}

// Summary aggregates tool-reported occurrences. Total counts occurrences while
// DiagnosticRecords counts distinct records, so a repeated error counted three
// times by Valgrind yields Total=3 and DiagnosticRecords=1.
type Summary struct {
	Total             int            `json:"total"`
	DiagnosticRecords int            `json:"diagnostic_records"`
	ByKind            map[string]int `json:"by_kind"`
	BySeverity        map[string]int `json:"by_severity"`
	LeakedBytes       int64          `json:"leaked_bytes,omitempty"`
	LeakedBlocks      int64          `json:"leaked_blocks,omitempty"`
	Suppressed        int            `json:"suppressed"`
	Truncated         bool           `json:"truncated"`
}

// Diagnostic is one normalized tool-reported observation.
type Diagnostic struct {
	ID                   string         `json:"id"`
	Fingerprint          string         `json:"fingerprint"`
	FingerprintAlgorithm string         `json:"fingerprint_algorithm"`
	Origin               string         `json:"origin"`
	Kind                 string         `json:"kind"`
	Severity             Severity       `json:"severity"`
	Message              string         `json:"message"`
	Location             *Location      `json:"location,omitempty"`
	Stack                []Frame        `json:"stack"`
	ContextStacks        []ContextStack `json:"context_stacks,omitempty"`
	OriginStack          []Frame        `json:"origin_stack,omitempty"`
	Occurrences          int            `json:"occurrences"`
	LeakedBytes          int64          `json:"leaked_bytes,omitempty"`
	LeakedBlocks         int64          `json:"leaked_blocks,omitempty"`
	Evidence             Evidence       `json:"evidence"`
	Confidence           Confidence     `json:"confidence"`
}

// ContextStack is an additional stack reported by Valgrind (for example the
// allocation site after an auxwhat element).
type ContextStack struct {
	Label  string  `json:"label,omitempty"`
	Frames []Frame `json:"frames"`
}

// Frame is one normalized stack frame. Object is the tool-reported binary or
// shared object; Path is only set when the tool reported a file.
type Frame struct {
	Function string `json:"function,omitempty"`
	Path     string `json:"path,omitempty"`
	Object   string `json:"object,omitempty"`
	Line     int    `json:"line,omitempty"`
	Runtime  bool   `json:"runtime,omitempty"`
}

// Location is the primary source location selected from the stack.
type Location struct {
	Function string `json:"function,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// Evidence links a diagnostic to the preserved raw document.
type Evidence struct {
	Artifact    string `json:"artifact"`
	RecordID    string `json:"record_id,omitempty"`
	RecordIndex int    `json:"record_index"`
	RawKind     string `json:"raw_kind,omitempty"`
}

// Suppression is a tool-reported suppression count. It is never a resolved bug.
type Suppression struct {
	Name   string `json:"name"`
	Count  int    `json:"count"`
	Unique string `json:"unique,omitempty"`
}

// Artifact describes one preserved file.
type Artifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
	Truncated bool   `json:"truncated"`
}

// Validate checks the invariants that must hold before a report is persisted or
// returned. In particular, partial data can never claim an absence of errors.
func (r Report) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("diagnostic: schema_version must be %d", SchemaVersion)
	}
	if r.RunID == "" {
		return errors.New("diagnostic: run_id must not be empty")
	}
	if r.Tool.Name == "" {
		return errors.New("diagnostic: tool name must not be empty")
	}
	if !r.Result.Valid() {
		return fmt.Errorf("diagnostic: invalid result %q", r.Result)
	}
	if r.Result == ResultNoErrorsObserved && !r.AnalysisComplete {
		return errors.New("diagnostic: no_errors_observed requires analysis_complete")
	}
	if r.Result == ResultErrorsFound && r.Summary.Total == 0 {
		return errors.New("diagnostic: errors_found requires a positive total")
	}
	if r.AnalysisComplete && r.Summary.Total < len(r.Diagnostics) {
		return errors.New("diagnostic: complete report total cannot be lower than the record count")
	}

	seenIDs := make(map[string]struct{}, len(r.Diagnostics))
	for i, d := range r.Diagnostics {
		if d.ID == "" {
			return fmt.Errorf("diagnostic: diagnostics[%d].id must not be empty", i)
		}
		if _, duplicate := seenIDs[d.ID]; duplicate {
			return fmt.Errorf("diagnostic: duplicate diagnostic id %q", d.ID)
		}
		seenIDs[d.ID] = struct{}{}
		if d.Kind == "" {
			return fmt.Errorf("diagnostic: diagnostics[%d].kind must not be empty", i)
		}
		if !d.Severity.Valid() {
			return fmt.Errorf("diagnostic: diagnostics[%d].severity %q is invalid", i, d.Severity)
		}
		if d.Confidence != ConfidenceToolReported && d.Confidence != ConfidenceInference {
			return fmt.Errorf("diagnostic: diagnostics[%d].confidence %q is invalid", i, d.Confidence)
		}
		if !strings.HasPrefix(d.Fingerprint, "sha256:") {
			return fmt.Errorf("diagnostic: diagnostics[%d].fingerprint must be a sha256 digest", i)
		}
		if d.Occurrences < 1 {
			return fmt.Errorf("diagnostic: diagnostics[%d].occurrences must be positive", i)
		}
	}
	return nil
}
