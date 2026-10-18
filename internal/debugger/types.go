// Package debugger owns interactive debugger sessions. J6 implements the GDB
// DAP backend and J7 adds LLDB through the same backend-neutral session manager
// and MCP contract.
package debugger

// APISchemaVersion versions the debug_* payloads.
const APISchemaVersion = 1

// Supported debugger backends. Both use the generic DAP session.
const (
	BackendGDB  = "gdb"
	BackendLLDB = "lldb"
)

// State is the session lifecycle exposed by the debugger API. It is separate
// from a process exit status and from any diagnostic conclusion.
type State string

const (
	// StateStarting means the adapter is being launched and initialized.
	StateStarting State = "starting"
	// StateRunning means a resume request was accepted by the adapter.
	StateRunning State = "running"
	// StateStopped means the adapter reported a stopped event.
	StateStopped State = "stopped"
	// StateTerminated means the debuggee ended.
	StateTerminated State = "terminated"
	// StateDetached means the server released a still-alive debuggee.
	StateDetached State = "detached"
	// StateFailed means the session ended on a transport or protocol failure.
	StateFailed State = "failed"
)

// Terminal reports whether no further execution progress is expected.
func (s State) Terminal() bool {
	switch s {
	case StateTerminated, StateDetached, StateFailed:
		return true
	default:
		return false
	}
}

// Capabilities is the session's negotiated subset relevant to the debug API.
// A missing capability is reported as false, never as silently supported.
type Capabilities struct {
	ConfigurationDone bool `json:"configuration_done"`
	TerminateRequest  bool `json:"terminate_request"`
	TerminateDebuggee bool `json:"terminate_debuggee"`
	DelayedStackTrace bool `json:"delayed_stack_trace"`
}

// Stop describes the most recent stop event.
type Stop struct {
	Reason            string `json:"reason,omitempty"`
	ThreadID          int    `json:"thread_id,omitempty"`
	AllThreadsStopped bool   `json:"all_threads_stopped"`
	Description       string `json:"description,omitempty"`
	HitBreakpoints    []int  `json:"hit_breakpoints,omitempty"`
}

// Exit describes the debuggee exit status when the adapter reported one.
type Exit struct {
	ExitCode int `json:"exit_code"`
}

// Status is returned by debug_start and debug_status.
type Status struct {
	SchemaVersion  int          `json:"schema_version"`
	SessionID      string       `json:"session_id"`
	Backend        string       `json:"backend"`
	BackendVersion string       `json:"backend_version,omitempty"`
	TargetID       string       `json:"target_id,omitempty"`
	Attached       bool         `json:"attached,omitempty"`
	State          State        `json:"state"`
	StopGeneration int          `json:"stop_generation"`
	Stop           *Stop        `json:"stop,omitempty"`
	Exit           *Exit        `json:"exit,omitempty"`
	LastEvent      string       `json:"last_event,omitempty"`
	LastEventAt    string       `json:"last_event_at,omitempty"`
	StartedAt      string       `json:"started_at,omitempty"`
	UpdatedAt      string       `json:"updated_at,omitempty"`
	Capabilities   Capabilities `json:"capabilities"`
	Warnings       []string     `json:"warnings,omitempty"`
	Error          string       `json:"error,omitempty"`
}

// StartRequest is the input of debug_start. Target arguments are a multiset
// subset of the configured target's allowed_args.
type StartRequest struct {
	TargetID    string   `json:"target_id" jsonschema:"declared target id; never a filesystem path"`
	Backend     string   `json:"backend,omitempty" jsonschema:"debugger backend: gdb (default when usable) or lldb"`
	Args        []string `json:"args,omitempty" jsonschema:"program arguments; each must be declared in the target's allowed_args"`
	StopOnEntry *bool    `json:"stop_on_entry,omitempty" jsonschema:"stop at the process entry point before running (default true)"`
}

// AttachRequest is the input of debug_attach. The process must be in the
// administrator's explicit attach allowlist and satisfy local ownership/Yama
// checks.
type AttachRequest struct {
	PID         int    `json:"pid" jsonschema:"explicitly allowed process id"`
	Backend     string `json:"backend,omitempty" jsonschema:"debugger backend: gdb (default when usable) or lldb"`
	StopOnEntry *bool  `json:"stop_on_entry,omitempty" jsonschema:"request a stop before inspecting; attach defaults to false and may be paused explicitly"`
}

// StatusRequest is the input of debug_status.
type StatusRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
}

// BreakpointsRequest is the input of debug_set_breakpoints.
type BreakpointsRequest struct {
	SessionID     string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	SourcePath    string `json:"source_path" jsonschema:"source file inside a workspace root, absolute or relative to the target cwd"`
	SourceDisplay string `json:"-" jsonschema:"-"`
	Lines         []int  `json:"lines" jsonschema:"1-based source line numbers"`
}

// Breakpoint is one adapter-reported breakpoint state.
type Breakpoint struct {
	ID       int    `json:"id,omitempty"`
	Verified bool   `json:"verified"`
	Line     int    `json:"line"`
	Message  string `json:"message,omitempty"`
	Source   string `json:"source,omitempty"`
}

// BreakpointsResult is the output of debug_set_breakpoints.
type BreakpointsResult struct {
	SchemaVersion  int          `json:"schema_version"`
	SessionID      string       `json:"session_id"`
	Source         string       `json:"source,omitempty"`
	StopGeneration int          `json:"stop_generation"`
	Breakpoints    []Breakpoint `json:"breakpoints"`
	Warnings       []string     `json:"warnings,omitempty"`
}

// ContinueRequest is the input of debug_continue.
type ContinueRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	ThreadID  int    `json:"thread_id,omitempty" jsonschema:"thread to continue; defaults to the stopped thread"`
}

// StepRequest is the input of debug_step.
type StepRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	Kind      string `json:"kind" jsonschema:"step kind: next, in or out"`
	ThreadID  int    `json:"thread_id,omitempty" jsonschema:"thread to step; defaults to the stopped thread"`
}

// PauseRequest is the input of debug_pause.
type PauseRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	ThreadID  int    `json:"thread_id,omitempty" jsonschema:"thread to pause; defaults to the adapter-reported thread or process id"`
}

// ActionResult acknowledges a resume or pause request. debug_continue is
// always asynchronous; debug_step and debug_pause wait briefly for the next
// stop. If the debuggee is still running, poll debug_status until a stopped
// event with a newer stop_generation arrives.
type ActionResult struct {
	SchemaVersion  int      `json:"schema_version"`
	SessionID      string   `json:"session_id"`
	Action         string   `json:"action"`
	Accepted       bool     `json:"accepted"`
	State          State    `json:"state"`
	StopGeneration int      `json:"stop_generation"`
	ThreadID       int      `json:"thread_id,omitempty"`
	Message        string   `json:"message,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// ThreadsRequest is the input of debug_threads.
type ThreadsRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	Start     int    `json:"start,omitempty" jsonschema:"zero-based first thread index"`
	Count     int    `json:"count,omitempty" jsonschema:"maximum threads to return"`
}

// Thread is one adapter-reported thread.
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
}

// ThreadsResult is the output of debug_threads.
type ThreadsResult struct {
	SchemaVersion  int      `json:"schema_version"`
	SessionID      string   `json:"session_id"`
	StopGeneration int      `json:"stop_generation"`
	Start          int      `json:"start"`
	Returned       int      `json:"returned"`
	Total          int      `json:"total"`
	HasMore        bool     `json:"has_more"`
	NextStart      int      `json:"next_start,omitempty"`
	Threads        []Thread `json:"threads"`
	Warnings       []string `json:"warnings,omitempty"`
}

// StackRequest is the input of debug_stack_trace.
type StackRequest struct {
	SessionID      string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	ThreadID       int    `json:"thread_id,omitempty" jsonschema:"thread whose stack is read; defaults to the stopped thread"`
	StartFrame     int    `json:"start_frame,omitempty" jsonschema:"zero-based first frame"`
	Levels         int    `json:"levels,omitempty" jsonschema:"maximum frames to return"`
	StopGeneration int    `json:"stop_generation,omitempty" jsonschema:"optional expected stop_generation; a stale value is rejected"`
}

// Source is a source location normalized to the workspace. External sources
// expose only their display name: absolute system paths are not returned.
type Source struct {
	Name     string `json:"name,omitempty"`
	Path     string `json:"path,omitempty"`
	External bool   `json:"external,omitempty"`
}

// Frame is one debugger stack frame. Instruction pointers and raw memory
// references are intentionally not exposed at J6/J7.
type Frame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name,omitempty"`
	Source *Source `json:"source,omitempty"`
	Line   int     `json:"line,omitempty"`
	Column int     `json:"column,omitempty"`
}

// StackResult is the output of debug_stack_trace.
type StackResult struct {
	SchemaVersion  int      `json:"schema_version"`
	SessionID      string   `json:"session_id"`
	StopGeneration int      `json:"stop_generation"`
	StartFrame     int      `json:"start_frame"`
	Returned       int      `json:"returned"`
	HasMore        bool     `json:"has_more"`
	NextFrame      int      `json:"next_frame,omitempty"`
	Frames         []Frame  `json:"frames"`
	Warnings       []string `json:"warnings,omitempty"`
}

// ScopesRequest is the input of debug_scopes.
type ScopesRequest struct {
	SessionID      string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	FrameID        int    `json:"frame_id" jsonschema:"frame id from debug_stack_trace for the current stop_generation"`
	StopGeneration int    `json:"stop_generation,omitempty" jsonschema:"optional expected stop_generation; a stale value is rejected"`
}

// Scope is one variable scope of a frame.
type Scope struct {
	Name               string  `json:"name"`
	VariablesReference int     `json:"variables_reference"`
	Expensive          bool    `json:"expensive"`
	NamedVariables     int     `json:"named_variables,omitempty"`
	IndexedVariables   int     `json:"indexed_variables,omitempty"`
	Source             *Source `json:"source,omitempty"`
	Line               int     `json:"line,omitempty"`
}

// ScopesResult is the output of debug_scopes.
type ScopesResult struct {
	SchemaVersion  int      `json:"schema_version"`
	SessionID      string   `json:"session_id"`
	StopGeneration int      `json:"stop_generation"`
	Scopes         []Scope  `json:"scopes"`
	Warnings       []string `json:"warnings,omitempty"`
}

// VariablesRequest is the input of debug_variables.
type VariablesRequest struct {
	SessionID          string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	VariablesReference int    `json:"variables_reference" jsonschema:"reference from debug_scopes or a parent variable"`
	Start              int    `json:"start,omitempty" jsonschema:"zero-based first child"`
	Count              int    `json:"count,omitempty" jsonschema:"maximum children to return"`
	StopGeneration     int    `json:"stop_generation,omitempty" jsonschema:"optional expected stop_generation; a stale value is rejected"`
}

// Variable is one adapter-reported variable. Values are bounded and may be
// truncated explicitly; child expansion is always a separate request.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variables_reference,omitempty"`
	NamedVariables     int    `json:"named_variables,omitempty"`
	IndexedVariables   int    `json:"indexed_variables,omitempty"`
	ValueTruncated     bool   `json:"value_truncated,omitempty"`
}

// VariablesResult is the output of debug_variables.
type VariablesResult struct {
	SchemaVersion  int        `json:"schema_version"`
	SessionID      string     `json:"session_id"`
	StopGeneration int        `json:"stop_generation"`
	Start          int        `json:"start"`
	Returned       int        `json:"returned"`
	Total          int        `json:"total"`
	HasMore        bool       `json:"has_more"`
	NextStart      int        `json:"next_start,omitempty"`
	Variables      []Variable `json:"variables"`
	Warnings       []string   `json:"warnings,omitempty"`
}

// StopRequest is the input of debug_stop. Terminate defaults to true: the
// server terminates the debuggee it started. Detach is not implemented in J6/J7;
// setting it to false is refused rather than reported as successful.
type StopRequest struct {
	SessionID string `json:"session_id" jsonschema:"opaque session id returned by debug_start"`
	Terminate *bool  `json:"terminate,omitempty" jsonschema:"terminate the debuggee; default true for launched sessions, false (detach) for attached sessions; false is refused for launched sessions"`
}

// StopResult is the output of debug_stop.
type StopResult struct {
	SchemaVersion  int      `json:"schema_version"`
	SessionID      string   `json:"session_id"`
	Terminated     bool     `json:"terminated"`
	Detached       bool     `json:"detached"`
	AlreadyStopped bool     `json:"already_stopped"`
	Status         Status   `json:"status"`
	Warnings       []string `json:"warnings,omitempty"`
}
