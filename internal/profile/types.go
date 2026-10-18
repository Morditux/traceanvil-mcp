// Package profile runs Callgrind and Massif and parses their native output.
// It exposes no free-form Valgrind flags: every argument is fixed by the tool.
package profile

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// APISchemaVersion versions the profile payloads.
const APISchemaVersion = 1

// Tool identifies a supported profiler.
type Tool string

const (
	ToolCallgrind Tool = "callgrind"
	ToolMassif    Tool = "massif"
)

// Valid reports whether tool is implemented.
func (t Tool) Valid() bool {
	return t == ToolCallgrind || t == ToolMassif
}

// RawArtifactName is the fixed raw output name inside a run directory.
func (t Tool) RawArtifactName() string {
	if t == ToolMassif {
		return "massif.out"
	}
	return "callgrind.out"
}

// ReportArtifactName is the normalized report name.
const ReportArtifactName = "profile.json"

// Default raw/output bounds. They are deliberately independent of the
// Valgrind XML parser and preserve the native format only.
const (
	DefaultMaxRawBytes          int64 = 64 << 20
	DefaultMaxReportBytes             = 8 << 20
	DefaultMaxReportOutputBytes       = 64 << 10
	DefaultMaxArtifactBytes           = 64 << 10
	MaxArtifactBytes                  = 1 << 20
	MaxTopFunctions                   = 32
	MaxMassifSnapshots                = 64
	MaxEvents                         = 64
	MaxFunctions                      = 100000
	MaxSnapshotCount                  = 1000000
	MaxLineBytes                      = 64 << 10
	MaxLines                          = 2000000
)

// Error codes are stable, machine-readable profile errors.
type ErrorCode string

const (
	CodeInvalidArgument      ErrorCode = "invalid_argument"
	CodeUnsupported          ErrorCode = "unsupported_capability"
	CodeBackendUnavailable   ErrorCode = "backend_unavailable"
	CodeTargetUnknown        ErrorCode = "target_unknown"
	CodeTargetDenied         ErrorCode = "target_denied"
	CodeTimeout              ErrorCode = "timeout"
	CodeCanceled             ErrorCode = "canceled"
	CodeRunFailed            ErrorCode = "run_failed"
	CodeProfileUnavailable   ErrorCode = "profile_unavailable"
	CodeProfileIncomplete    ErrorCode = "profile_incomplete"
	CodeIncompatibleProfiles ErrorCode = "incompatible_profiles"
	CodeArtifactNotFound     ErrorCode = "artifact_not_found"
	CodeQuotaExceeded        ErrorCode = "quota_exceeded"
)

// Error is a typed profile error.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "profile: " + string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return "profile: " + string(e.Code) + ": " + e.Message
}
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a typed error.
func Errorf(code ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a typed error with a cause.
func Wrap(code ErrorCode, err error, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the typed code, if any.
func CodeOf(err error) ErrorCode {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

// Request starts one profile run. Args are constrained to the target's
// allowed_args; timeout_ms may only shorten the configured maximum.
type Request struct {
	TargetID  string   `json:"target_id" jsonschema:"declared target id; never a filesystem path"`
	Args      []string `json:"args,omitempty" jsonschema:"program arguments; each must be declared in the target's allowed_args"`
	TimeoutMS int      `json:"timeout_ms,omitempty" jsonschema:"optional timeout in milliseconds; may only shorten limits.timeout_seconds"`
}

// ToolRequest is the common request for profile_callgrind/profile_massif.
type ToolRequest = Request

// EventMetric is one Callgrind event and its normalized total.
type EventMetric struct {
	Name  string  `json:"name"`
	Total float64 `json:"total"`
}

// FunctionCost is a Callgrind function's self cost for one event.
type FunctionCost struct {
	Name  string  `json:"name"`
	File  string  `json:"file,omitempty"`
	Event string  `json:"event"`
	Self  float64 `json:"self"`
}

// MassifSnapshot is one bounded Massif heap snapshot.
type MassifSnapshot struct {
	Index       int    `json:"index"`
	Time        uint64 `json:"time"`
	HeapBytes   uint64 `json:"heap_bytes"`
	HeapExtra   uint64 `json:"heap_extra_bytes"`
	StacksBytes uint64 `json:"stacks_bytes"`
	Peak        bool   `json:"peak,omitempty"`
}

// Artifact references one preserved profile artifact.
type Artifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// Report is the bounded normalized result of one profile run.
type Report struct {
	SchemaVersion int      `json:"schema_version"`
	RunID         string   `json:"run_id"`
	Tool          Tool     `json:"tool"`
	TargetID      string   `json:"target_id"`
	Program       string   `json:"program"`
	ProgramSHA256 string   `json:"program_sha256,omitempty"`
	Args          []string `json:"args,omitempty"`
	ToolPath      string   `json:"tool_path"`
	ToolVersion   string   `json:"tool_version,omitempty"`
	Complete      bool     `json:"complete"`
	ExitCode      int      `json:"exit_code"`
	Signal        int      `json:"signal,omitempty"`
	TimedOut      bool     `json:"timed_out"`
	Canceled      bool     `json:"canceled"`
	DurationMS    int64    `json:"duration_ms"`

	MetricUnit         string             `json:"metric_unit"`
	PrimaryMetric      string             `json:"primary_metric,omitempty"`
	Metrics            map[string]float64 `json:"metrics"`
	Events             []EventMetric      `json:"events,omitempty"`
	TopFunctions       []FunctionCost     `json:"top_functions,omitempty"`
	PeakHeapBytes      uint64             `json:"peak_heap_bytes,omitempty"`
	PeakHeapExtraBytes uint64             `json:"peak_heap_extra_bytes,omitempty"`
	PeakStacksBytes    uint64             `json:"peak_stacks_bytes,omitempty"`
	Snapshots          int                `json:"snapshots,omitempty"`
	TimeUnit           string             `json:"time_unit,omitempty"`
	SnapshotSamples    []MassifSnapshot   `json:"snapshot_samples,omitempty"`

	Artifacts []Artifact `json:"artifacts"`
	Warnings  []string   `json:"warnings"`

	// ReportArtifact is the normalized profile.json reference.
	ReportArtifact string `json:"report_artifact"`
}

// Comparison is a cautious, descriptive comparison of two persisted profiles.
type Comparison struct {
	SchemaVersion int                `json:"schema_version"`
	BaseRunID     string             `json:"base_run_id"`
	TargetRunID   string             `json:"target_run_id"`
	Tool          Tool               `json:"tool"`
	MetricUnit    string             `json:"metric_unit"`
	Compatible    bool               `json:"compatible"`
	Reasons       []string           `json:"reasons,omitempty"`
	BaseMetrics   map[string]float64 `json:"base_metrics"`
	TargetMetrics map[string]float64 `json:"target_metrics"`
	Deltas        map[string]float64 `json:"deltas"`
	Percent       map[string]float64 `json:"percent,omitempty"`
	Warnings      []string           `json:"warnings"`
}

// CompareRequest selects two persisted profile runs.
type CompareRequest struct {
	BaseRunID   string `json:"base_run_id" jsonschema:"run id from profile_callgrind/profile_massif"`
	TargetRunID string `json:"target_run_id" jsonschema:"run id from the same profiler"`
}

// ArtifactRequest selects preserved profile evidence.
type ArtifactRequest struct {
	RunID    string `json:"run_id" jsonschema:"run id returned by a profile tool"`
	FileName string `json:"file_name" jsonschema:"profile.json, callgrind.out or massif.out"`
	Offset   int64  `json:"offset,omitempty" jsonschema:"byte offset"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum excerpt bytes (default 64 KiB, max 1 MiB)"`
}

// ArtifactOutput is a bounded UTF-8 excerpt of profile evidence.
type ArtifactOutput struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	FileName      string `json:"file_name"`
	Bytes         int64  `json:"bytes"`
	Offset        int64  `json:"offset"`
	ReturnedBytes int    `json:"returned_bytes"`
	MaxBytes      int    `json:"max_bytes"`
	HasMore       bool   `json:"has_more"`
	NextOffset    int64  `json:"next_offset"`
	Content       string `json:"content"`
	TruncatedUTF8 bool   `json:"truncated_utf8,omitempty"`
}

// runOptions are the fixed options recorded in a report.
func (t Tool) metricsUnit() string {
	if t == ToolMassif {
		return "bytes"
	}
	return "events"
}

// ContextError maps a context cancellation to a typed profile error.
func ContextError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "profile run was canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "profile run timed out")
	}
	return Wrap(CodeRunFailed, err, "profile run failed")
}

// ProfileDuration is a testable time helper; it is not part of the wire API.
func ProfileDuration(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}
