// Package vgdb exposes a deliberately narrow experimental inspection of a
// Valgrind gdbserver. It sends only fixed read-only monitor commands and never
// accepts a client command string.
package vgdb

import (
	"context"
	"errors"
	"fmt"
)

const (
	// APISchemaVersion versions vgdb payloads.
	APISchemaVersion = 1
	// DefaultMaxOutputBytes bounds one vgdb command.
	DefaultMaxOutputBytes int64 = 64 << 10
	// DefaultMaxReportBytes bounds the normalized report.
	DefaultMaxReportBytes = 1 << 20
	// DefaultMaxArtifactBytes bounds an artifact excerpt.
	DefaultMaxArtifactBytes = 64 << 10
	// MaxArtifactBytes bounds an explicit artifact request.
	MaxArtifactBytes = 1 << 20
)

// ErrorCode is a stable vgdb error code.
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

// Error is a typed vgdb error.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "vgdb: " + string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return "vgdb: " + string(e.Code) + ": " + e.Message
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

// Request selects a declared target for the fixed vgdb inspection.
type Request struct {
	TargetID  string   `json:"target_id" jsonschema:"declared target id; never a filesystem path"`
	Args      []string `json:"args,omitempty" jsonschema:"program arguments; each must be declared in the target's allowed_args"`
	TimeoutMS int      `json:"timeout_ms,omitempty" jsonschema:"optional timeout in milliseconds; may only shorten limits.timeout_seconds"`
}

// CommandResult is one fixed monitor-command result.
type CommandResult struct {
	Command   string `json:"command"`
	Complete  bool   `json:"complete"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Artifact references preserved vgdb/Valgrind evidence.
type Artifact struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Report is the experimental vgdb inspection result.
type Report struct {
	SchemaVersion  int             `json:"schema_version"`
	RunID          string          `json:"run_id"`
	TargetID       string          `json:"target_id"`
	Program        string          `json:"program"`
	Args           []string        `json:"args,omitempty"`
	ValgrindPath   string          `json:"valgrind_path"`
	VgdbPath       string          `json:"vgdb_path"`
	Experimental   bool            `json:"experimental"`
	Complete       bool            `json:"complete"`
	ExitCode       int             `json:"exit_code"`
	TimedOut       bool            `json:"timed_out"`
	Canceled       bool            `json:"canceled"`
	DurationMS     int64           `json:"duration_ms"`
	CleanupMethod  string          `json:"cleanup_method,omitempty"`
	Commands       []CommandResult `json:"commands"`
	Artifacts      []Artifact      `json:"artifacts"`
	Warnings       []string        `json:"warnings"`
	ReportArtifact string          `json:"report_artifact"`
}

// ArtifactRequest selects preserved vgdb evidence.
type ArtifactRequest struct {
	RunID    string `json:"run_id" jsonschema:"run id returned by vgdb_inspect"`
	FileName string `json:"file_name" jsonschema:"vgdb.json, stdout.log or stderr.log"`
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

// ContextError maps context failures.
func ContextError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return Wrap(CodeCanceled, err, "vgdb run was canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeTimeout, err, "vgdb run timed out")
	}
	return Wrap(CodeRunFailed, err, "vgdb run failed")
}
