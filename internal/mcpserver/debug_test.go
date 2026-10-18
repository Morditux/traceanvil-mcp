package mcpserver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/capabilities"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
	"github.com/Morditux/traceanvil-mcp/internal/version"
)

type fakeDebugService struct {
	mu        sync.Mutex
	sessions  bool
	targets   bool
	startErr  error
	status    debugger.Status
	lastStart debugger.StartRequest
	lastStop  debugger.StopRequest
}

func (f *fakeDebugService) Start(_ context.Context, req debugger.StartRequest) (debugger.Status, error) {
	f.mu.Lock()
	f.lastStart = req
	f.mu.Unlock()
	if f.startErr != nil {
		return debugger.Status{}, f.startErr
	}
	return f.status, nil
}

func (f *fakeDebugService) Status(context.Context, debugger.StatusRequest) (debugger.Status, error) {
	return f.status, nil
}

func (f *fakeDebugService) SetBreakpoints(_ context.Context, req debugger.BreakpointsRequest) (debugger.BreakpointsResult, error) {
	return debugger.BreakpointsResult{SessionID: req.SessionID, Source: req.SourcePath}, nil
}

func (f *fakeDebugService) Continue(_ context.Context, req debugger.ContinueRequest) (debugger.ActionResult, error) {
	return debugger.ActionResult{SessionID: req.SessionID, Action: "continue", Accepted: true}, nil
}

func (f *fakeDebugService) Step(_ context.Context, req debugger.StepRequest) (debugger.ActionResult, error) {
	return debugger.ActionResult{SessionID: req.SessionID, Action: "step", Accepted: true}, nil
}

func (f *fakeDebugService) Pause(_ context.Context, req debugger.PauseRequest) (debugger.ActionResult, error) {
	return debugger.ActionResult{SessionID: req.SessionID, Action: "pause", Accepted: true}, nil
}

func (f *fakeDebugService) Threads(context.Context, debugger.ThreadsRequest) (debugger.ThreadsResult, error) {
	return debugger.ThreadsResult{}, nil
}

func (f *fakeDebugService) StackTrace(context.Context, debugger.StackRequest) (debugger.StackResult, error) {
	return debugger.StackResult{}, nil
}

func (f *fakeDebugService) Scopes(context.Context, debugger.ScopesRequest) (debugger.ScopesResult, error) {
	return debugger.ScopesResult{}, nil
}

func (f *fakeDebugService) Variables(context.Context, debugger.VariablesRequest) (debugger.VariablesResult, error) {
	return debugger.VariablesResult{}, nil
}

func (f *fakeDebugService) Stop(_ context.Context, req debugger.StopRequest) (debugger.StopResult, error) {
	f.mu.Lock()
	f.lastStop = req
	f.mu.Unlock()
	return debugger.StopResult{SessionID: req.SessionID, Terminated: true, Status: f.status}, nil
}

func (f *fakeDebugService) HasSessions() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions
}

func (f *fakeDebugService) HasTargets() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.targets
}

func availableGDB(t *testing.T, registry *capabilities.Registry) {
	t.Helper()
	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendGDB,
		Kind:    "backend",
		Backend: "gdb",
		State:   capabilities.StateAvailable,
		Path:    "/usr/bin/gdb",
		Version: "GNU gdb test",
	}); err != nil {
		t.Fatal(err)
	}
}

func newDebugServer(t *testing.T) (*Server, *fakeDebugService, *capabilities.Registry, *mcp.ClientSession, context.Context) {
	t.Helper()
	registry := capabilities.NewRegistry(nil, time.Second)
	availableGDB(t, registry)
	service := &fakeDebugService{targets: true, status: debugger.Status{
		SchemaVersion:  debugger.APISchemaVersion,
		SessionID:      "dbg_000000000000000000000000000000aa",
		Backend:        debugger.BackendGDB,
		TargetID:       "fixture",
		State:          debugger.StateStopped,
		StopGeneration: 1,
	}}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range DebugDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatalf("Register(%s) error = %v", definition.Name, err)
		}
	}
	server.SyncTools()
	session, ctx := connect(t, server, nil)
	return server, service, registry, session, ctx
}

func TestDebugToolPublicationFollowsCapabilityAndSessions(t *testing.T) {
	server, service, registry, session, ctx := newDebugServer(t)
	names := listToolNames(t, session, ctx)
	for _, want := range DebugToolNames() {
		if !contains(names, want) {
			t.Errorf("tools = %v, missing %q", names, want)
		}
	}
	if contains(names, "debug_evaluate") || contains(names, "debug_attach") || contains(names, "debug_execute_command") {
		t.Errorf("tools = %v contain a forbidden debug tool", names)
	}

	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendGDB,
		Kind:    "backend",
		Backend: "gdb",
		State:   capabilities.StateUnavailable,
		Reason:  "gdb was removed",
	}); err != nil {
		t.Fatal(err)
	}
	added, removed := server.SyncTools()
	if len(added) != 0 {
		t.Errorf("SyncTools() added = %v", added)
	}
	if len(removed) == 0 {
		t.Error("SyncTools() removed nothing after gdb disappeared")
	}
	names = listToolNames(t, session, ctx)
	if contains(names, "debug_start") || contains(names, "debug_continue") {
		t.Errorf("tools = %v still expose execution actions without gdb", names)
	}
	if contains(names, "debug_status") || contains(names, "debug_stop") {
		t.Errorf("tools = %v expose session management with no session", names)
	}

	service.mu.Lock()
	service.sessions = true
	service.targets = true
	service.mu.Unlock()
	server.SyncTools()
	names = listToolNames(t, session, ctx)
	for _, want := range []string{"debug_status", "debug_stop"} {
		if !contains(names, want) {
			t.Errorf("tools = %v, want %q for an existing session", names, want)
		}
	}
	for _, forbidden := range []string{"debug_start", "debug_set_breakpoints", "debug_continue", "debug_variables"} {
		if contains(names, forbidden) {
			t.Errorf("tools = %v expose %q without gdb", names, forbidden)
		}
	}
	if status := toolStatus(server.ToolStatuses(), "debug_status"); !status.Exposed {
		t.Errorf("debug_status status = %+v, want exposed", status)
	}

	availableGDB(t, registry)
	server.SyncTools()
	names = listToolNames(t, session, ctx)
	for _, want := range DebugToolNames() {
		if !contains(names, want) {
			t.Errorf("tools = %v, missing %q after gdb returned", names, want)
		}
	}

	service.mu.Lock()
	service.targets = false
	service.mu.Unlock()
	server.SyncTools()
	names = listToolNames(t, session, ctx)
	if contains(names, "debug_start") {
		t.Errorf("tools = %v expose debug_start with no usable target", names)
	}
	for _, want := range []string{"debug_status", "debug_stop", "debug_continue", "debug_variables"} {
		if !contains(names, want) {
			t.Errorf("tools = %v, missing %q with GDB available", names, want)
		}
	}
}

func TestDebugToolSchemasAndAnnotations(t *testing.T) {
	_, _, _, session, ctx := newDebugServer(t)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]*mcp.Tool)
	for _, tool := range tools.Tools {
		found[tool.Name] = tool
	}

	start := found["debug_start"]
	if start == nil {
		t.Fatal("debug_start is not published")
	}
	if start.Annotations == nil || start.Annotations.ReadOnlyHint {
		t.Error("debug_start must not be read-only")
	}
	schema, ok := start.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("debug_start schema type = %T", start.InputSchema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("debug_start properties = %T", schema["properties"])
	}
	for _, field := range []string{"target_id", "backend", "args", "stop_on_entry"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("debug_start schema is missing %q", field)
		}
	}
	if _, ok := properties["path"]; ok {
		t.Error("debug_start schema exposes a free path field")
	}

	status := found["debug_status"]
	if status == nil || status.Annotations == nil || !status.Annotations.ReadOnlyHint {
		t.Error("debug_status must be published and read-only")
	}
	if status.OutputSchema == nil {
		t.Error("debug_status has no output schema")
	}

	breakpoints := found["debug_set_breakpoints"]
	if breakpoints == nil {
		t.Fatal("debug_set_breakpoints is not published")
	}
	bpSchema, ok := breakpoints.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("debug_set_breakpoints schema type = %T", breakpoints.InputSchema)
	}
	bpProperties, ok := bpSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("debug_set_breakpoints properties = %T", bpSchema["properties"])
	}
	for _, field := range []string{"session_id", "source_path", "lines"} {
		if _, ok := bpProperties[field]; !ok {
			t.Errorf("debug_set_breakpoints schema is missing %q", field)
		}
	}
	if _, ok := bpProperties["source_display"]; ok {
		t.Error("debug_set_breakpoints schema exposes the internal source_display field")
	}

	for _, name := range []string{"debug_stack_trace", "debug_scopes", "debug_variables"} {
		tool := found[name]
		if tool == nil {
			t.Fatalf("%s is not published", name)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s schema type = %T", name, tool.InputSchema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %T", name, schema["properties"])
		}
		if _, ok := properties["stop_generation"]; !ok {
			t.Errorf("%s schema is missing stop_generation", name)
		}
	}
	stop := found["debug_stop"]
	if stop == nil || stop.Annotations == nil {
		t.Fatal("debug_stop is not published with annotations")
	}
	if stop.Annotations.ReadOnlyHint {
		t.Error("debug_stop must not be read-only")
	}
	if stop.Annotations.DestructiveHint == nil || !*stop.Annotations.DestructiveHint {
		t.Error("debug_stop should be marked destructive")
	}
}

func TestDebugToolsCallAndNamedErrors(t *testing.T) {
	_, service, _, session, ctx := newDebugServer(t)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "debug_start",
		Arguments: map[string]any{"target_id": "fixture", "stop_on_entry": false},
	})
	if err != nil {
		t.Fatalf("CallTool(debug_start) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("debug_start returned an error: %s", toolText(result))
	}
	var started debugger.Status
	decodeCall(t, result, &started)
	if started.SessionID != service.status.SessionID {
		t.Errorf("session_id = %q", started.SessionID)
	}
	service.mu.Lock()
	stopOnEntry := service.lastStart.StopOnEntry
	service.mu.Unlock()
	if stopOnEntry == nil || *stopOnEntry {
		t.Errorf("debug_start stop_on_entry = %v, want explicit false", stopOnEntry)
	}

	stopResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "debug_stop",
		Arguments: map[string]any{"session_id": started.SessionID, "terminate": false},
	})
	if err != nil || stopResult.IsError {
		t.Fatalf("CallTool(debug_stop) = %+v, %v", stopResult, err)
	}
	var stopped debugger.StopResult
	decodeCall(t, stopResult, &stopped)
	if !stopped.Terminated {
		t.Errorf("stop result = %+v", stopped)
	}
	service.mu.Lock()
	terminate := service.lastStop.Terminate
	service.mu.Unlock()
	if terminate == nil || *terminate {
		t.Errorf("debug_stop terminate = %v, want explicit false", terminate)
	}

	service.mu.Lock()
	service.startErr = debugger.Errorf(debugger.CodeBackendUnavailable, "gdb disappeared")
	service.mu.Unlock()
	failed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "debug_start",
		Arguments: map[string]any{"target_id": "fixture"},
	})
	if err != nil {
		t.Fatalf("CallTool(debug_start failing) transport error = %v", err)
	}
	if !failed.IsError {
		t.Fatal("debug_start did not surface the named error as a tool error")
	}
	if text := toolText(failed); !strings.Contains(text, "backend_unavailable") {
		t.Errorf("tool error text = %q, want backend_unavailable", text)
	}
}

func availableLLDB(t *testing.T, registry *capabilities.Registry) {
	t.Helper()
	if err := registry.Set(capabilities.Capability{
		ID:      capabilities.BackendLLDB,
		Kind:    "backend",
		Backend: "lldb",
		State:   capabilities.StateAvailable,
		Path:    "/usr/bin/lldb-dap",
		Version: "lldb-dap test",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDebugToolsPublishForLLDBOnly(t *testing.T) {
	registry := capabilities.NewRegistry(nil, time.Second)
	if err := registry.Set(capabilities.Capability{
		ID: capabilities.BackendGDB, Kind: "backend", Backend: "gdb",
		State: capabilities.StateUnavailable, Reason: "gdb not installed",
	}); err != nil {
		t.Fatal(err)
	}
	availableLLDB(t, registry)
	service := &fakeDebugService{
		targets: true,
		status: debugger.Status{
			SchemaVersion: debugger.APISchemaVersion,
			SessionID:     "dbg_000000000000000000000000000000bb",
			Backend:       debugger.BackendLLDB,
			TargetID:      "fixture",
			State:         debugger.StateStopped,
		},
	}
	server := New(testConfig(), registry, version.Current(), nil)
	for _, definition := range DebugDefinitions(service, registry) {
		if err := server.Register(definition); err != nil {
			t.Fatal(err)
		}
	}
	server.SyncTools()
	session, ctx := connect(t, server, nil)
	names := listToolNames(t, session, ctx)
	for _, want := range DebugToolNames() {
		if !contains(names, want) {
			t.Errorf("tools = %v, missing %q with LLDB available", names, want)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "debug_start", Arguments: map[string]any{"target_id": "fixture", "backend": "lldb"},
	})
	if err != nil || result.IsError {
		t.Fatalf("debug_start(lldb) = %+v, %v", result, err)
	}
	service.mu.Lock()
	backend := service.lastStart.Backend
	service.mu.Unlock()
	if backend != "lldb" {
		t.Errorf("debug_start backend = %q", backend)
	}
	for _, status := range server.ToolStatuses() {
		if status.Name == "debug_start" {
			if len(status.RequiredCapabilities) != 2 ||
				status.RequiredCapabilities[0] != capabilities.BackendGDB ||
				status.RequiredCapabilities[1] != capabilities.BackendLLDB {
				t.Errorf("debug_start required_capabilities = %v", status.RequiredCapabilities)
			}
		}
	}
}
