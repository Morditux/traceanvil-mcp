package ctest

import (
	"errors"
	"fmt"
)

// Code is a stable CTest API error code.
type Code string

const (
	CodeInvalidArgument     Code = "invalid_argument"
	CodeProjectUnknown      Code = "project_unknown"
	CodeProjectUnavailable  Code = "project_unavailable"
	CodeBackendUnavailable  Code = "backend_unavailable"
	CodeToolDisabled        Code = "tool_disabled"
	CodeClosed              Code = "closed"
	CodeRunNotFound         Code = "run_not_found"
	CodeArtifactUnavailable Code = "artifact_unavailable"
	CodeReportUnavailable   Code = "report_unavailable"
	CodeReportInvalid       Code = "report_invalid"
	CodeDiscoveryFailed     Code = "discovery_failed"
	CodeCanceled            Code = "canceled"
	CodeTimeout             Code = "timeout"
	CodeQuotaExceeded       Code = "quota_exceeded"
	CodeInternal            Code = "internal"
)

// Error is a typed CTest API error.
type Error struct {
	Code    Code
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return "ctest: " + e.Message + ": " + e.Err.Error()
	}
	return "ctest: " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Errorf builds a typed error without an underlying cause.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds a typed error around a cause.
func Wrap(code Code, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// CodeOf returns the stable code of err, or CodeInternal.
func CodeOf(err error) Code {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}
