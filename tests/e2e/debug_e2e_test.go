package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/debugger"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
)

const e2eDebugSource = `#include <stdio.h>

static int add(int a, int b) {
    int sum = a + b;
    return sum;
}

int main(void) {
    int x = 1;
    int y = 2;
    printf("start\n");
    int z = add(x, y);
    printf("z=%d\n", z);
    return 0;
}
`

func TestServeGDBDebugWorkflow(t *testing.T) {
	runServeDebugWorkflow(t, "gdb")
}

func TestServeLLDBDebugWorkflow(t *testing.T) {
	runServeDebugWorkflow(t, "lldb")
}

func runServeDebugWorkflow(t *testing.T, backend string) {
	debuggerBinary := "gdb"
	if backend == "lldb" {
		debuggerBinary = "lldb-dap"
	}
	if _, err := exec.LookPath(debuggerBinary); err != nil {
		t.Skipf("%s is not installed; skipping the debug workflow", debuggerBinary)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed; skipping the debug workflow")
	}

	root := t.TempDir()
	source := filepath.Join(root, "fixture.c")
	if err := os.WriteFile(source, []byte(e2eDebugSource), 0o600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "fixture")
	build := exec.Command(cc, "-g", "-O0", "-fno-omit-frame-pointer", "-o", program, source)
	build.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, output)
	}
	configPath := writeDebugConfig(t, root, program, backend)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "serve", "--config", configPath)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr syncBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	raw := &syncBuffer{}
	transport := &mcp.IOTransport{
		Reader: &teeReadCloser{Reader: io.TeeReader(stdoutPipe, raw), Closer: stdoutPipe},
		Writer: stdinPipe,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "debug-e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}

	capResult := callTool(t, ctx, session, "system_capabilities", map[string]any{})
	var report mcpserver.Report
	decodeStructured(t, capResult, &report)
	capabilityID := "backend." + backend
	backendAvailable := false
	for _, capability := range report.Capabilities {
		if capability.ID == capabilityID && capability.State == "available" {
			backendAvailable = true
		}
	}
	if !backendAvailable {
		t.Fatalf("system_capabilities does not report %s as available: %+v", capabilityID, report.Capabilities)
	}
	startExposed := false
	for _, tool := range report.Tools {
		if tool.Name == "debug_start" {
			startExposed = tool.Exposed
		}
	}
	if !startExposed {
		t.Fatalf("system_capabilities does not expose debug_start: %+v", report.Tools)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools(): %v", err)
	}
	for _, name := range mcpserver.DebugToolNames() {
		if name == "debug_attach" {
			continue
		}
		if !containsTool(tools.Tools, name) {
			_ = session.Close()
			waitForExit(t, cmd, &stderr)
			t.Fatalf("debug tool %q is not published; tools = %v\nserver stderr:\n%s",
				name, toolNames(tools.Tools), stderr.String())
		}
	}
	for _, forbidden := range []string{"debug_evaluate", "debug_execute_command"} {
		if containsTool(tools.Tools, forbidden) {
			t.Errorf("forbidden debug tool %q is published", forbidden)
		}
	}
	if containsTool(tools.Tools, "debug_attach") {
		t.Error("debug_attach is published without an explicit attach allowlist")
	}

	startArguments := map[string]any{"target_id": "fixture"}
	if backend == "gdb" {
		// Keep the explicit GDB path covered; LLDB omits backend to exercise
		// the documented fallback when GDB is disabled.
		startArguments["backend"] = "gdb"
	}
	startResult := callTool(t, ctx, session, "debug_start", startArguments)
	var started debugger.Status
	decodeStructured(t, startResult, &started)
	if started.State != debugger.StateStopped || started.StopGeneration != 1 {
		t.Fatalf("debug_start status = %+v", started)
	}
	if started.Backend != backend {
		t.Errorf("backend = %q, want %q", started.Backend, backend)
	}

	bpResult := callTool(t, ctx, session, "debug_set_breakpoints", map[string]any{
		"session_id":  started.SessionID,
		"source_path": "fixture.c",
		"lines":       []int{11},
	})
	var breakpoints debugger.BreakpointsResult
	decodeStructured(t, bpResult, &breakpoints)
	if len(breakpoints.Breakpoints) != 1 || !breakpoints.Breakpoints[0].Verified {
		t.Fatalf("breakpoints = %+v", breakpoints)
	}
	if breakpoints.Source != "fixture.c" {
		t.Errorf("breakpoint source = %q, want fixture.c", breakpoints.Source)
	}

	callTool(t, ctx, session, "debug_continue", map[string]any{"session_id": started.SessionID})
	stopped := waitForDebugStop(t, ctx, session, started.SessionID, started.StopGeneration)
	if stopped.Stop == nil || stopped.Stop.ThreadID == 0 {
		t.Fatalf("stopped = %+v", stopped)
	}

	threadsResult := callTool(t, ctx, session, "debug_threads", map[string]any{
		"session_id": started.SessionID,
	})
	var threads debugger.ThreadsResult
	decodeStructured(t, threadsResult, &threads)
	if threads.Total == 0 {
		t.Fatalf("threads = %+v", threads)
	}

	stackResult := callTool(t, ctx, session, "debug_stack_trace", map[string]any{
		"session_id": started.SessionID,
		"thread_id":  stopped.Stop.ThreadID,
		"levels":     10,
	})
	var stack debugger.StackResult
	decodeStructured(t, stackResult, &stack)
	var mainFrame *debugger.Frame
	for i := range stack.Frames {
		if stack.Frames[i].Name == "main" {
			mainFrame = &stack.Frames[i]
		}
		if stack.Frames[i].Source != nil && strings.HasPrefix(stack.Frames[i].Source.Path, "/") {
			t.Errorf("absolute source path exposed: %+v", stack.Frames[i].Source)
		}
	}
	if mainFrame == nil {
		t.Fatalf("no main frame in %+v", stack.Frames)
	}
	if mainFrame.Source == nil || mainFrame.Source.Path != "fixture.c" {
		t.Errorf("main source = %+v", mainFrame.Source)
	}

	scopesResult := callTool(t, ctx, session, "debug_scopes", map[string]any{
		"session_id": started.SessionID,
		"frame_id":   mainFrame.ID,
	})
	var scopes debugger.ScopesResult
	decodeStructured(t, scopesResult, &scopes)
	var locals *debugger.Scope
	for i := range scopes.Scopes {
		if scopes.Scopes[i].Name == "Locals" {
			locals = &scopes.Scopes[i]
		}
	}
	if locals == nil {
		t.Fatalf("no Locals scope in %+v", scopes.Scopes)
	}
	variablesResult := callTool(t, ctx, session, "debug_variables", map[string]any{
		"session_id":          started.SessionID,
		"variables_reference": locals.VariablesReference,
		"count":               20,
	})
	var variables debugger.VariablesResult
	decodeStructured(t, variablesResult, &variables)
	values := make(map[string]string)
	for _, variable := range variables.Variables {
		values[variable.Name] = variable.Value
	}
	if values["x"] != "1" || values["y"] != "2" {
		t.Errorf("locals = %+v, want x=1 y=2", variables.Variables)
	}

	stopResult := callTool(t, ctx, session, "debug_stop", map[string]any{
		"session_id": started.SessionID,
	})
	var stoppedResult debugger.StopResult
	decodeStructured(t, stopResult, &stoppedResult)
	if !stoppedResult.Terminated || stoppedResult.Status.State != debugger.StateTerminated {
		t.Errorf("debug_stop result = %+v", stoppedResult)
	}

	statusResult := callTool(t, ctx, session, "debug_status", map[string]any{
		"session_id": started.SessionID,
	})
	var final debugger.Status
	decodeStructured(t, statusResult, &final)
	if final.State != debugger.StateTerminated {
		t.Errorf("final state = %q, want terminated", final.State)
	}

	if err := session.Close(); err != nil {
		t.Logf("close MCP session: %v", err)
	}
	waitForExit(t, cmd, &stderr)
	assertProtocolOnly(t, raw.Bytes())
	if strings.Contains(stderr.String(), "panic:") {
		t.Errorf("server stderr contains a panic:\n%s", stderr.String())
	}
}

func writeDebugConfig(t *testing.T, root, program, backend string) string {
	t.Helper()
	reportDir := filepath.Join(t.TempDir(), "reports")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [
			{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": []}
		],
		"backends": {"valgrind": false, "gdb": %t, "lldb": %t},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_sessions": 2, "max_response_items": 100}
	}`, config.SchemaVersion, root, reportDir, program, root, backend == "gdb", backend == "lldb")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForDebugStop(t *testing.T, ctx context.Context, session *mcp.ClientSession, sessionID string, previousGeneration int) debugger.Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		result := callTool(t, ctx, session, "debug_status", map[string]any{"session_id": sessionID})
		var status debugger.Status
		decodeStructured(t, result, &status)
		if status.State == debugger.StateStopped && status.StopGeneration > previousGeneration {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("debug session did not stop: %+v", status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
