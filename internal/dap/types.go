package dap

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// Event is one DAP event delivered to the single Events consumer.
type Event struct {
	// Seq is the adapter-assigned sequence number.
	Seq int
	// Name is the DAP event name (stopped, continued, output, …).
	Name string
	// Body is the raw event body; missing bodies are nil.
	Body json.RawMessage
	// ReceivedAt is the local reception time.
	ReceivedAt time.Time
}

// DecodeBody decodes the event body into v. A missing or null body is a no-op.
func (e Event) DecodeBody(v any) error {
	return decodeRaw(e.Body, v)
}

// Response is one successful or failed DAP response.
type Response struct {
	// Seq is the adapter-assigned sequence number.
	Seq int
	// RequestSeq is the client request sequence this response answers.
	RequestSeq int
	// Command is the DAP command name.
	Command string
	// Success reports the adapter's outcome.
	Success bool
	// Message is the adapter error message when Success is false.
	Message string
	// Body is the raw response body; missing bodies are nil.
	Body json.RawMessage
}

// DecodeBody decodes the response body into v. A missing or null body is a
// no-op.
func (r Response) DecodeBody(v any) error {
	return decodeRaw(r.Body, v)
}

// ReverseRequest is an adapter-initiated request, for example runInTerminal.
type ReverseRequest struct {
	// Seq is the adapter request sequence.
	Seq int
	// Command is the reverse command name.
	Command string
	// Arguments is the raw arguments object, if any.
	Arguments json.RawMessage
}

// DecodeArguments decodes the reverse request arguments into v.
func (r ReverseRequest) DecodeArguments(v any) error {
	return decodeRaw(r.Arguments, v)
}

// InitializeRequestArguments is the client side of the DAP initialize request.
// Fields with pointer types are omitted when nil; Initialize fills the standard
// defaults for linesStartAt1, columnsStartAt1 and pathFormat.
type InitializeRequestArguments struct {
	ClientID        string `json:"clientID,omitempty"`
	ClientName      string `json:"clientName,omitempty"`
	AdapterID       string `json:"adapterID"`
	Locale          string `json:"locale,omitempty"`
	LinesStartAt1   *bool  `json:"linesStartAt1,omitempty"`
	ColumnsStartAt1 *bool  `json:"columnsStartAt1,omitempty"`
	PathFormat      string `json:"pathFormat,omitempty"`

	SupportsVariableType                *bool `json:"supportsVariableType,omitempty"`
	SupportsVariablePaging              *bool `json:"supportsVariablePaging,omitempty"`
	SupportsRunInTerminalRequest        *bool `json:"supportsRunInTerminalRequest,omitempty"`
	SupportsMemoryReferences            *bool `json:"supportsMemoryReferences,omitempty"`
	SupportsProgressReporting           *bool `json:"supportsProgressReporting,omitempty"`
	SupportsInvalidatedEvent            *bool `json:"supportsInvalidatedEvent,omitempty"`
	SupportsMemoryEvent                 *bool `json:"supportsMemoryEvent,omitempty"`
	SupportsArgsCanBeInterpretedByShell *bool `json:"supportsArgsCanBeInterpretedByShell,omitempty"`
	SupportsStartDebuggingRequest       *bool `json:"supportsStartDebuggingRequest,omitempty"`
	SupportsANSIStyling                 *bool `json:"supportsANSIStyling,omitempty"`
}

// Options configures a Client. Zero values select the documented defaults.
type Options struct {
	// ClientID and ClientName identify the MCP server to the adapter.
	ClientID   string
	ClientName string

	// MaxFrameBytes bounds one Content-Length payload in both directions.
	MaxFrameBytes int64
	// MaxHeaderBytes bounds the header block of one incoming frame.
	MaxHeaderBytes int64

	// WriteQueue bounds frames waiting for the single writer.
	WriteQueue int
	// EventBuffer bounds events waiting for the consumer. A full buffer
	// applies backpressure to the read loop.
	EventBuffer int
	// MaxPendingRequests bounds unanswered request futures.
	MaxPendingRequests int
	// ReverseRequestConcurrency bounds adapter-initiated handlers.
	ReverseRequestConcurrency int

	// WriteTimeout, when positive and supported by the transport, bounds one
	// frame write. It has no effect on transports without write deadlines.
	WriteTimeout time.Duration

	// ReverseRequestHandler handles adapter-initiated requests. A nil handler
	// rejects every reverse request with success=false.
	ReverseRequestHandler func(context.Context, ReverseRequest) (any, error)
}

func (o Options) withDefaults() Options {
	if o.ClientID == "" {
		o.ClientID = "mcp-native-diagnostics"
	}
	if o.ClientName == "" {
		o.ClientName = "MCP Native Diagnostics"
	}
	if o.MaxFrameBytes <= 0 {
		o.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if o.MaxHeaderBytes <= 0 {
		o.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if o.WriteQueue <= 0 {
		o.WriteQueue = 32
	}
	if o.EventBuffer <= 0 {
		o.EventBuffer = 128
	}
	if o.MaxPendingRequests <= 0 {
		o.MaxPendingRequests = 128
	}
	if o.ReverseRequestConcurrency <= 0 {
		o.ReverseRequestConcurrency = 8
	}
	if o.WriteTimeout < 0 {
		o.WriteTimeout = 0
	}
	return o
}

func decodeRaw(raw json.RawMessage, v any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return json.Unmarshal(raw, v)
}
