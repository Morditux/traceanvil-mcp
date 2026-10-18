package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
)

// DebugService is the debugger business API consumed by the MCP handlers.
type DebugService interface {
	Start(context.Context, debugger.StartRequest) (debugger.Status, error)
	Status(context.Context, debugger.StatusRequest) (debugger.Status, error)
	SetBreakpoints(context.Context, debugger.BreakpointsRequest) (debugger.BreakpointsResult, error)
	Continue(context.Context, debugger.ContinueRequest) (debugger.ActionResult, error)
	Step(context.Context, debugger.StepRequest) (debugger.ActionResult, error)
	Pause(context.Context, debugger.PauseRequest) (debugger.ActionResult, error)
	Threads(context.Context, debugger.ThreadsRequest) (debugger.ThreadsResult, error)
	StackTrace(context.Context, debugger.StackRequest) (debugger.StackResult, error)
	Scopes(context.Context, debugger.ScopesRequest) (debugger.ScopesResult, error)
	Variables(context.Context, debugger.VariablesRequest) (debugger.VariablesResult, error)
	Stop(context.Context, debugger.StopRequest) (debugger.StopResult, error)
	HasSessions() bool
	HasTargets() bool
}

// DebugToolNames returns the tools installed by DebugDefinitions.
func DebugToolNames() []string {
	return []string{
		"debug_start",
		"debug_status",
		"debug_set_breakpoints",
		"debug_continue",
		"debug_pause",
		"debug_step",
		"debug_threads",
		"debug_stack_trace",
		"debug_scopes",
		"debug_variables",
		"debug_stop",
	}
}

// DebugDefinitions returns the MCP tool definitions for the J6 GDB debug API.
// Start and execution actions are published only while backend.gdb is
// available. Status and stop remain available for an existing session even if
// the backend capability disappears, so resources can always be released.
func DebugDefinitions(service DebugService, caps *capabilities.Registry) []ToolDefinition {
	if service == nil {
		return nil
	}

	backendState := func() (capabilities.State, string) {
		state := capabilities.StateUnknown
		reason := "gdb capability is not registered"
		if caps != nil {
			if record, ok := caps.Get(capabilities.BackendGDB); ok {
				state = record.State
				reason = record.Reason
			}
		}
		if reason == "" {
			reason = "gdb is unavailable"
		}
		return state, reason
	}

	manageAvailability := func() (bool, capabilities.State, string) {
		state, reason := backendState()
		if state == capabilities.StateAvailable {
			return true, state, reason
		}
		// An existing session must remain manageable even after the backend
		// capability disappears; this exemption applies only to status/stop.
		if service.HasSessions() {
			return true, state, "an existing debug session remains manageable"
		}
		return false, state, reason
	}

	startAvailability := func() (bool, capabilities.State, string) {
		state, reason := backendState()
		if state != capabilities.StateAvailable {
			return false, state, reason
		}
		if !service.HasTargets() {
			return false, state, "no declared target currently resolves under policy"
		}
		return true, state, reason
	}

	return []ToolDefinition{
		{
			Name:               "debug_start",
			RequiredCapability: capabilities.BackendGDB,
			Available:          startAvailability,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_start",
					Description: "Launch one declared target under GDB over DAP. Only the gdb backend is " +
						"implemented. The target id is resolved from configuration; this tool accepts no " +
						"path, GDB flag, expression or shell command. stop_on_entry defaults to true. " +
						"Returns a session_id to use with the other debug_* tools.",
					Annotations: mutationAnnotations("Start debug session", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.StartRequest) (*mcp.CallToolResult, debugger.Status, error) {
					status, err := service.Start(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name:      "debug_status",
			Available: manageAvailability,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_status",
					Description: "Report session state, the latest stop event and stop_generation without " +
						"contacting GDB. Poll after a resume or pause; frame and variable references belong " +
						"to one stop_generation.",
					Annotations: readOnlyAnnotations("Debug status"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.StatusRequest) (*mcp.CallToolResult, debugger.Status, error) {
					status, err := service.Status(ctx, input)
					return nil, status, err
				})
			},
		},
		{
			Name:               "debug_set_breakpoints",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_set_breakpoints",
					Description: "Set line breakpoints in a source file inside a workspace root. Relative " +
						"paths resolve against the target cwd; symlink escapes are rejected. The session " +
						"must be stopped. The adapter-reported verified state is returned; unverified " +
						"breakpoints are not treated as set.",
					Annotations: mutationAnnotations("Set breakpoints", false),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.BreakpointsRequest) (*mcp.CallToolResult, debugger.BreakpointsResult, error) {
					result, err := service.SetBreakpoints(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_continue",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_continue",
					Description: "Continue a stopped session. The request is acknowledged asynchronously; " +
						"poll debug_status until stop_generation changes. The stopped thread is used when " +
						"thread_id is omitted.",
					Annotations: mutationAnnotations("Continue debuggee", false),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.ContinueRequest) (*mcp.CallToolResult, debugger.ActionResult, error) {
					result, err := service.Continue(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_pause",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_pause",
					Description: "Request a pause of a running session. Asynchronous: poll debug_status " +
						"until a stopped event with a new stop_generation arrives.",
					Annotations: mutationAnnotations("Pause debuggee", false),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.PauseRequest) (*mcp.CallToolResult, debugger.ActionResult, error) {
					result, err := service.Pause(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_step",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_step",
					Description: "Step one source-level next, in or out from a stopped session. " +
						"Asynchronous: poll debug_status until stop_generation changes.",
					Annotations: mutationAnnotations("Step debuggee", false),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.StepRequest) (*mcp.CallToolResult, debugger.ActionResult, error) {
					result, err := service.Step(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_threads",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_threads",
					Description: "Return one bounded page of adapter-reported threads. Thread ids are " +
						"valid for the current session; a running target may report a changing set.",
					Annotations: readOnlyAnnotations("Debug threads"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.ThreadsRequest) (*mcp.CallToolResult, debugger.ThreadsResult, error) {
					result, err := service.Threads(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_stack_trace",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_stack_trace",
					Description: "Return one bounded page of stack frames for a stopped thread. Source " +
						"paths are workspace-relative; external paths and instruction pointers are omitted. " +
						"Frames without symbols are reported as such, never invented.",
					Annotations: readOnlyAnnotations("Debug stack trace"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.StackRequest) (*mcp.CallToolResult, debugger.StackResult, error) {
					result, err := service.StackTrace(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_scopes",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_scopes",
					Description: "Return the variable scopes of a frame. Scopes and their references are " +
						"valid only for the stop_generation that produced the frame id.",
					Annotations: readOnlyAnnotations("Debug scopes"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.ScopesRequest) (*mcp.CallToolResult, debugger.ScopesResult, error) {
					result, err := service.Scopes(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:               "debug_variables",
			RequiredCapability: capabilities.BackendGDB,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_variables",
					Description: "Return one bounded page of variable children. Child expansion is a " +
						"separate request; values are truncated explicitly. Expression evaluation, memory " +
						"reads and arbitrary writes are not exposed.",
					Annotations: readOnlyAnnotations("Debug variables"),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.VariablesRequest) (*mcp.CallToolResult, debugger.VariablesResult, error) {
					result, err := service.Variables(ctx, input)
					return nil, result, err
				})
			},
		},
		{
			Name:      "debug_stop",
			Available: manageAvailability,
			Add: func(server *mcp.Server) {
				mcp.AddTool(server, &mcp.Tool{
					Name: "debug_stop",
					Description: "Terminate a debug session and release its GDB process. Detach is not " +
						"implemented at J6: terminate=false is refused with unsupported_capability. Always " +
						"available for an existing session, even if GDB later disappears.",
					Annotations: mutationAnnotations("Stop debug session", true),
				}, func(ctx context.Context, _ *mcp.CallToolRequest, input debugger.StopRequest) (*mcp.CallToolResult, debugger.StopResult, error) {
					result, err := service.Stop(ctx, input)
					return nil, result, err
				})
			},
		},
	}
}
