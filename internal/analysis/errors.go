package analysis

import (
	"errors"
	"fmt"
)

// ErrorCode is the stable, documented error code returned by the analysis API.
// It is part of the MCP error contract: the text of a tool error starts with the
// code followed by ": ".
type ErrorCode string

const (
	// CodeInvalidArgument is a malformed or out-of-range request field.
	CodeInvalidArgument ErrorCode = "invalid_argument"
	// CodeUnknownTool is a tool that this build does not implement.
	CodeUnknownTool ErrorCode = "unknown_tool"
	// CodeToolDisabled is a tool disabled by configuration.
	CodeToolDisabled ErrorCode = "tool_disabled"
	// CodeBackendUnavailable is a missing, unusable or unverified backend.
	CodeBackendUnavailable ErrorCode = "backend_unavailable"
	// CodeTargetUnknown is a target id that is not declared in configuration.
	CodeTargetUnknown ErrorCode = "target_unknown"
	// CodePolicyDenied is a target or argument set rejected by local policy.
	CodePolicyDenied ErrorCode = "policy_denied"
	// CodeQuotaExceeded is an exhausted concurrency, artifact or collection
	// quota.
	CodeQuotaExceeded ErrorCode = "quota_exceeded"
	// CodeRunNotFound is an unknown run id or a missing run directory.
	CodeRunNotFound ErrorCode = "run_not_found"
	// CodeInvalidCursor is a malformed cursor or one reused with other filters
	// or another run.
	CodeInvalidCursor ErrorCode = "invalid_cursor"
	// CodeReportUnavailable means the run exists but its normalized report is
	// missing or not yet written.
	CodeReportUnavailable ErrorCode = "report_unavailable"
	// CodeReportInvalid means the stored report violates its schema or its
	// internal invariants. It is never downgraded to an empty result.
	CodeReportInvalid ErrorCode = "report_invalid"
	// CodeReportTooLarge means the stored report exceeds the API read bound.
	CodeReportTooLarge ErrorCode = "report_too_large"
	// CodeIncompatibleRuns means two reports differ in a way that makes an
	// honest diagnostic comparison impossible.
	CodeIncompatibleRuns ErrorCode = "incompatible_runs"
	// CodeDiffTooLarge means one diff item exceeds the response byte bound.
	CodeDiffTooLarge ErrorCode = "diff_too_large"
	// CodeArtifactUnavailable means the requested artifact is missing or was
	// rejected by the artifact store.
	CodeArtifactUnavailable ErrorCode = "artifact_unavailable"
	// CodeCanceled means the caller's context was canceled.
	CodeCanceled ErrorCode = "canceled"
	// CodeTimeout means the caller's context deadline expired.
	CodeTimeout ErrorCode = "timeout"
	// CodeClosed means the analysis service has been closed.
	CodeClosed ErrorCode = "server_closed"
	// CodeInternal is an unexpected server failure. Details are not a
	// diagnostic: callers must not infer the program is clean.
	CodeInternal ErrorCode = "internal"
)

// Error is a named API error. It preserves the underlying cause for logs while
// exposing a stable code to MCP clients.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

// Error implements error. The text is "code: message" so a plain MCP tool error
// remains machine-readable.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap exposes the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a named error.
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a named code and message to an underlying error.
func Wrap(code ErrorCode, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the named code of err, or CodeInternal when err is not a
// named analysis error.
func CodeOf(err error) ErrorCode {
	var named *Error
	if errors.As(err, &named) {
		return named.Code
	}
	return CodeInternal
}

// HasCode reports whether err carries the given named code.
func HasCode(err error, code ErrorCode) bool {
	var named *Error
	return errors.As(err, &named) && named.Code == code
}
