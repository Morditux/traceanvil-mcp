package debugger

import (
	"errors"
	"fmt"
)

// ErrorCode is a stable machine-readable debugger API error.
type ErrorCode string

const (
	CodeInvalidArgument    ErrorCode = "invalid_argument"
	CodeBackendDisabled    ErrorCode = "backend_disabled"
	CodeBackendUnavailable ErrorCode = "backend_unavailable"
	CodeTargetUnknown      ErrorCode = "target_unknown"
	CodePolicyDenied       ErrorCode = "policy_denied"
	CodeSourceDenied       ErrorCode = "source_denied"
	CodeQuotaExceeded      ErrorCode = "quota_exceeded"
	CodeSessionNotFound    ErrorCode = "session_not_found"
	CodeSessionBusy        ErrorCode = "session_busy"
	CodeNotStopped         ErrorCode = "not_stopped"
	CodeNotRunning         ErrorCode = "not_running"
	CodeSessionTerminated  ErrorCode = "session_terminated"
	CodeStaleReference     ErrorCode = "stale_reference"
	CodeUnsupported        ErrorCode = "unsupported_capability"
	CodeTimeout            ErrorCode = "timeout"
	CodeCanceled           ErrorCode = "canceled"
	CodeClosed             ErrorCode = "server_closed"
	CodeStartFailed        ErrorCode = "start_failed"
	CodeProtocol           ErrorCode = "protocol_error"
	CodeInternal           ErrorCode = "internal"
)

// Error is a named debugger API error. It preserves the underlying cause for
// logs while exposing a stable code in the MCP tool error text.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

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

// CodeOf returns the named code of err, or CodeInternal.
func CodeOf(err error) ErrorCode {
	var named *Error
	if errors.As(err, &named) {
		return named.Code
	}
	return CodeInternal
}
