// Package perf runs the experimental Linux perf stat tool. It is not a
// general perf command surface: events and arguments are fixed by the server.
package perf

import (
	"context"
	"errors"
	"fmt"
)

const (
	// APISchemaVersion versions perf payloads.
	APISchemaVersion = 1
	// DefaultMaxOutputBytes bounds captured perf output.
	DefaultMaxOutputBytes int64 = 4 << 20
	// DefaultMaxReportBytes bounds the normalized report.
	DefaultMaxReportBytes = 1 << 20
	// DefaultMaxArtifactBytes bounds one artifact excerpt.
	DefaultMaxArtifactBytes = 64 << 10
	// MaxArtifactBytes bounds an explicit artifact request.
	MaxArtifactBytes = 1 << 20
)

// ErrorCode is a stable perf error code.
type ErrorCode string

const (
	CodeInvalidArgument    ErrorCode = "invalid_argument"
	CodeUnsupported        ErrorCode = "unsupported_capability"
	CodeBackendUnavailable ErrorCode = "backend_unavailable"
	CodeTargetUnknown      ErrorCode = "target_unknown"
	CodeTargetDenied       ErrorCode = "target_denied"
	CodeTimeout            ErrorCode = "timeout"
	CodeCanceled           ErrorCode = "canceled"
	CodeRunFailed          ErrorCode = "run_failed"
	CodeProfileIncomplete  ErrorCode = "profile_incomplete"
	CodeArtifactNotFound   ErrorCode = "artifact_not_found"
)

// Error is a typed perf error.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "perf: " + string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return "perf: " + string(e.Code) + ": " + e.Message
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

// Request is the fixed perf-stat request. The server supplies the event list;
// the client cannot add perf flags or event names.
type Request struct {
	TargetID  string   `json:"target_id" jsonschema:"declared target id; never a filesystem path"`
	Args      []string `json:"args,omitempty" jsonschema:"program arguments; each must be declared in the target's allowed_args"`
	TimeoutMS int      `json:"timeout_ms,omitempty" jsonschema:"optional timeout in milliseconds; may only shorten limits.timeout_seconds"`
}

// EventMetric is one normalized perf counter.
type EventMetric struct {
	Name           string  `json:"name"`
	Value          float64 `json:"value"`
	Unit           string  `json:"unit,omitempty"`
	RunningPercent float64 `json:"running_percent,omitempty"`
}

// Artifact references preserved perf evidence.
type Artifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Report is the bounded normalized perf result.
type Report struct {
	SchemaVersion  int                `json:"schema_version"`
	RunID          string             `json:"run_id"`
	TargetID       string             `json:"target_id"`
	Program        string             `json:"program"`
	Args           []string           `json:"args,omitempty"`
	ToolPath       string             `json:"tool_path"`
	ToolVersion    string             `json:"tool_version,omitempty"`
	Experimental   bool               `json:"experimental"`
	Complete       bool               `json:"complete"`
	ExitCode       int                `json:"exit_code"`
	TimedOut       bool               `json:"timed_out"`
	Canceled       bool               `json:"canceled"`
	DurationMS     int64              `json:"duration_ms"`
	Events         []EventMetric      `json:"events,omitempty"`
	Metrics        map[string]float64 `json:"metrics"`
	Artifacts      []Artifact         `json:"artifacts"`
	Warnings       []string           `json:"warnings"`
	ReportArtifact string             `json:"report_artifact"`
}

// ArtifactRequest selects a bounded perf artifact excerpt.
type ArtifactRequest struct {
	RunID    string `json:"run_id" jsonschema:"run id returned by profile_perf"`
	FileName string `json:"file_name" jsonschema:"perf.json, stdout.log or stderr.log"`
	Offset   int64  `json:"offset,omitempty" jsonschema:"byte offset"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum excerpt bytes"`
}

// ArtifactOutput is a bounded UTF-8 excerpt.
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
}

// ContextError maps context failures to typed perf errors.
func ContextError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "perf run was canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "perf run timed out")
	}
	return Wrap(CodeRunFailed, err, "perf run failed")
}
