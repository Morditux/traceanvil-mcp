// Package dap implements a generic Debug Adapter Protocol client shared by the
// GDB and LLDB backends. It owns the Content-Length framing, request/response
// correlation, event delivery, capability negotiation, timeouts and shutdown.
// It speaks no debugger-specific command: backends build on Client.Request.
package dap

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Sentinel errors. Use errors.Is to classify a failure; protocol details are
// wrapped in ProtocolError.
var (
	// ErrClosed means the client was closed or the transport terminated.
	ErrClosed = errors.New("dap: client is closed")
	// ErrNotInitialized means a command was sent before initialize completed.
	ErrNotInitialized = errors.New("dap: client is not initialized")
	// ErrInvalidState means the requested transition is not allowed.
	ErrInvalidState = errors.New("dap: invalid client state")
	// ErrInvalidArgument means a caller-provided value is invalid.
	ErrInvalidArgument = errors.New("dap: invalid argument")
	// ErrNilContext means a nil context was passed.
	ErrNilContext = errors.New("dap: nil context")
	// ErrEOF means the adapter closed the connection while the client was open.
	ErrEOF = errors.New("dap: adapter closed the connection")
	// ErrFrameTooLarge means Content-Length or a payload exceeds the limit.
	ErrFrameTooLarge = errors.New("dap: frame exceeds the maximum size")
	// ErrHeaderTooLarge means the header block exceeds the limit.
	ErrHeaderTooLarge = errors.New("dap: header exceeds the maximum size")
	// ErrMalformedFrame means the framing or JSON envelope is invalid.
	ErrMalformedFrame = errors.New("dap: malformed frame")
	// ErrPendingLimit means too many requests are waiting for responses.
	ErrPendingLimit = errors.New("dap: pending request limit reached")
)

// ProtocolError describes a framing, encoding or protocol violation. The
// underlying error remains available through errors.Is/errors.As.
type ProtocolError struct {
	Reason string
	Err    error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return "dap: " + e.Reason + ": " + e.Err.Error()
	}
	return "dap: " + e.Reason
}

// Unwrap exposes the underlying cause.
func (e *ProtocolError) Unwrap() error { return e.Err }

func protocolError(reason string, err error) *ProtocolError {
	return &ProtocolError{Reason: reason, Err: err}
}

// ResponseError reports a well-formed DAP response with success=false. It is a
// request-level failure, not a transport failure: the session can continue.
type ResponseError struct {
	// Response is the full failed response, including a raw body.
	Response *Response
	// Command is the request command that failed.
	Command string
	// Message is the adapter-provided error message.
	Message string
	// Body is the adapter-provided structured error body, if any.
	Body json.RawMessage
}

func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}
	command := e.Command
	if command == "" {
		command = "request"
	}
	if e.Message != "" {
		return fmt.Sprintf("dap: %s failed: %s", command, e.Message)
	}
	return fmt.Sprintf("dap: %s failed", command)
}

func newResponseError(resp *Response) *ResponseError {
	if resp == nil {
		return &ResponseError{}
	}
	return &ResponseError{
		Response: resp,
		Command:  resp.Command,
		Message:  resp.Message,
		Body:     resp.Body,
	}
}
