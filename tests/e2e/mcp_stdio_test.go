// Package e2e exercises the compiled binary over a real stdio transport.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/analysis"
	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
	"github.com/Morditux/traceanvil-mcp/internal/mcpserver"
)

var binaryPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcp-native-diagnostics-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create temp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binaryPath = filepath.Join(dir, "mcp-native-diagnostics")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/mcp-native-diagnostics")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build server binary: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestServeStdio(t *testing.T) {
	configPath, sentinel := writeConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}

	init := session.InitializeResult()
	if init == nil || init.Capabilities == nil || init.Capabilities.Tools == nil {
		t.Fatalf("missing tools capability: %+v", init)
	}
	if !init.Capabilities.Tools.ListChanged {
		t.Error("tools.listChanged = false, want true")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "system_capabilities",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(): %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() returned a tool error: %+v", result.Content)
	}

	rawReport, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var report mcpserver.Report
	if err := json.Unmarshal(rawReport, &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.SchemaVersion != mcpserver.ReportSchemaVersion {
		t.Errorf("schema_version = %d, want %d", report.SchemaVersion, mcpserver.ReportSchemaVersion)
	}
	if report.Server.Name != "mcp-native-diagnostics" {
		t.Errorf("server.name = %q, want %q", report.Server.Name, "mcp-native-diagnostics")
	}
	if len(report.Tools) < 5 {
		t.Errorf("tools = %+v, want system_capabilities and the evidence tools", report.Tools)
	}
	expected := make(map[string]bool)
	for _, status := range report.Tools {
		if status.Exposed {
			expected[status.Name] = true
		}
	}
	for _, required := range []string{
		"system_capabilities",
		"analysis_status",
		"analysis_cancel",
		"analysis_report",
		"analysis_artifact",
	} {
		if !expected[required] {
			t.Errorf("tools = %+v, want exposed %q", report.Tools, required)
		}
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools(): %v", err)
	}
	if len(tools.Tools) != len(expected) {
		t.Fatalf("tools/list = %v, want the exposed set %v", toolNames(tools.Tools), expected)
	}
	for _, tool := range tools.Tools {
		if !expected[tool.Name] {
			t.Errorf("tools/list exposes %q, which system_capabilities reports as hidden", tool.Name)
		}
	}

	assertTargetNotExecuted(t, sentinel)

	if err := session.Close(); err != nil {
		t.Logf("close client session: %v", err)
	}
	waitForExit(t, cmd, &stderr)

	assertProtocolOnly(t, raw.Bytes())
}

func TestDoctorDoesNotExecuteTarget(t *testing.T) {
	configPath, sentinel := writeConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "doctor", "--config", configPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("doctor failed: %v\nstderr:\n%s", err, stderr.String())
	}
	assertTargetNotExecuted(t, sentinel)

	if !strings.Contains(stdout.String(), "profile:") {
		t.Errorf("doctor output = %q, want a profile line", stdout.String())
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := config.Load("../../config.example.json"); err != nil {
		t.Fatalf("config.example.json is invalid: %v", err)
	}
}

func TestServeRejectsInvalidConfigWithoutWritingStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "serve", "--config", filepath.Join(t.TempDir(), "absent.json"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("serve exited 0, want a failure")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty (stdout is reserved for MCP)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "config:") {
		t.Errorf("stderr = %q, want a config error", stderr.String())
	}
}

func writeConfig(t *testing.T) (configPath, sentinel string) {
	t.Helper()

	root := t.TempDir()
	reportDir := filepath.Join(t.TempDir(), "reports")
	program := filepath.Join(root, "fixture")
	sentinel = filepath.Join(root, "target-executed")

	// The target writes a sentinel if it is ever executed. doctor and
	// system_capabilities must leave it untouched.
	script := fmt.Sprintf("#!/bin/sh\nprintf executed > %q\n", sentinel)
	if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [
			{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": ["--one"]}
		],
		"backends": {"valgrind": true, "gdb": true, "lldb": false},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {}
	}`, config.SchemaVersion, root, reportDir, program, root)

	configPath = filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, sentinel
}

func assertTargetNotExecuted(t *testing.T, sentinel string) {
	t.Helper()

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("configured target was executed: sentinel %q exists", sentinel)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat sentinel %q: %v", sentinel, err)
	}
}

func waitForExit(t *testing.T, cmd *exec.Cmd, stderr *syncBuffer) {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exited with %v after a clean shutdown\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("server did not exit after stdin closed\nstderr:\n%s", stderr.String())
	}
}

// assertProtocolOnly verifies that every line written by serve on stdout is a
// JSON-RPC 2.0 message. Any human-readable log line would fail this check.
func assertProtocolOnly(t *testing.T, data []byte) {
	t.Helper()

	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatal("stdout is empty, want MCP messages")
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lines := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lines++

		var envelope struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Errorf("stdout line is not JSON: %q (%v)", line, err)
			continue
		}
		if envelope.JSONRPC != "2.0" {
			t.Errorf("stdout line is not JSON-RPC 2.0: %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Errorf("scan stdout: %v", err)
	}
	if lines == 0 {
		t.Error("no stdout line found")
	}
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

type teeReadCloser struct {
	io.Reader
	io.Closer
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeAnalysisWorkflow(t *testing.T) {
	if _, err := exec.LookPath("valgrind"); err != nil {
		t.Skip("valgrind is not installed; skipping the analysis workflow")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed; skipping the analysis workflow")
	}

	root := t.TempDir()
	program := filepath.Join(root, "invalid_read")
	source := filepath.Join("..", "..", "testdata", "fixtures", "c", "invalid_read.c")
	build := exec.Command(cc, "-g", "-O0", "-fno-omit-frame-pointer", "-o", program, source)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, output)
	}

	configPath := writeAnalysisConfig(t, root, program)

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
	client := mcp.NewClient(&mcp.Implementation{Name: "analysis-e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools(): %v", err)
	}
	if !containsTool(tools.Tools, "analysis_start") {
		_ = session.Close()
		waitForExit(t, cmd, &stderr)
		t.Skipf("backend.valgrind is not available; tools = %v", toolNames(tools.Tools))
	}

	startResult := callTool(t, ctx, session, "analysis_start", map[string]any{
		"target_id": "fixture",
		"tool":      "memcheck",
	})
	var started analysis.Status
	decodeStructured(t, startResult, &started)
	if started.RunID == "" {
		t.Fatal("analysis_start returned an empty run_id")
	}
	t.Logf("analysis run_id=%s", started.RunID)

	var status analysis.Status
	deadline := time.Now().Add(30 * time.Second)
	for {
		statusResult := callTool(t, ctx, session, "analysis_status", map[string]any{
			"run_id": started.RunID,
		})
		decodeStructured(t, statusResult, &status)
		if status.Phase == analysis.PhaseCompleted || status.Phase == analysis.PhaseFailed ||
			status.Phase == analysis.PhaseCancelled || status.Phase == analysis.PhaseTimedOut {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("analysis did not finish: %+v", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status.Phase != analysis.PhaseCompleted || status.State != analysis.StateCompleted {
		t.Fatalf("final status = %+v", status)
	}
	if status.Result != diagnostic.ResultErrorsFound || !status.AnalysisComplete {
		t.Fatalf("result = %q complete = %v (warnings: %v)", status.Result, status.AnalysisComplete, status.Warnings)
	}
	if status.Metrics.Diagnostics != 1 || status.Metrics.Occurrences != 1 {
		t.Errorf("metrics = %+v, want one diagnostic", status.Metrics)
	}

	reportResult := callTool(t, ctx, session, "analysis_report", map[string]any{
		"run_id": started.RunID,
		"limit":  10,
	})
	var page analysis.ReportPage
	decodeStructured(t, reportResult, &page)
	if page.Result != diagnostic.ResultErrorsFound || !page.AnalysisComplete {
		t.Fatalf("report = %+v", page)
	}
	if len(page.Diagnostics) != 1 || page.Diagnostics[0].Kind != "InvalidRead" {
		t.Fatalf("diagnostics = %+v, want one InvalidRead", page.Diagnostics)
	}
	if page.Page.Matched != 1 || page.Page.HasMore {
		t.Errorf("page = %+v, want one matched item and no continuation", page.Page)
	}
	t.Logf("report result=%s diagnostics=%d raw_xml_bytes=%d report_bytes=%d",
		page.Result, page.Page.Matched, status.Metrics.RawXMLBytes, status.Metrics.ReportBytes)

	artifactResult := callTool(t, ctx, session, "analysis_artifact", map[string]any{
		"run_id":        started.RunID,
		"artifact_type": "raw_xml",
		"max_bytes":     500,
	})
	var artifact analysis.ArtifactOutput
	decodeStructured(t, artifactResult, &artifact)
	if !strings.Contains(artifact.Content, "<valgrindoutput") {
		t.Errorf("raw XML excerpt does not look like Valgrind XML: %q", artifact.Content)
	}
	if !artifact.HasMore || artifact.ReturnedBytes != 500 || artifact.NextOffset != 500 {
		t.Errorf("artifact = %+v, want a 500-byte continuation", artifact)
	}

	continuation := callTool(t, ctx, session, "analysis_artifact", map[string]any{
		"run_id":        started.RunID,
		"artifact_type": "raw_xml",
		"offset":        artifact.NextOffset,
		"max_bytes":     500,
	})
	var next analysis.ArtifactOutput
	decodeStructured(t, continuation, &next)
	if next.Content == "" || next.Offset != 500 {
		t.Errorf("continuation = %+v", next)
	}

	reportArtifact := callTool(t, ctx, session, "analysis_artifact", map[string]any{
		"run_id":        started.RunID,
		"artifact_type": "analysis_report",
		"max_bytes":     4000,
	})
	if !strings.Contains(toolResultText(reportArtifact), "schema_version") {
		t.Errorf("analysis_report excerpt does not contain schema_version: %q", toolResultText(reportArtifact))
	}

	cancelResult := callTool(t, ctx, session, "analysis_cancel", map[string]any{
		"run_id": started.RunID,
	})
	var canceled analysis.CancelResult
	decodeStructured(t, cancelResult, &canceled)
	if canceled.CancelRequested {
		t.Errorf("cancel_requested = true for a finished run: %+v", canceled)
	}

	unknown := callToolExpectError(t, ctx, session, "analysis_status", map[string]any{
		"run_id": "run_ffffffffffffffffffffffffffffffff",
	})
	if !strings.Contains(unknown, "run_not_found") {
		t.Errorf("error text = %q, want run_not_found", unknown)
	}

	invalidArtifact := callToolExpectError(t, ctx, session, "analysis_artifact", map[string]any{
		"run_id":        started.RunID,
		"artifact_type": "secrets",
	})
	if !strings.Contains(invalidArtifact, "invalid_argument") {
		t.Errorf("error text = %q, want invalid_argument", invalidArtifact)
	}

	invalidField := callToolExpectError(t, ctx, session, "analysis_report", map[string]any{
		"run_id": started.RunID,
		"bogus":  true,
	})
	if !strings.Contains(invalidField, "bogus") && !strings.Contains(invalidField, "additional") {
		t.Errorf("error text = %q, want an unknown-field rejection", invalidField)
	}

	if err := session.Close(); err != nil {
		t.Logf("close client session: %v", err)
	}
	waitForExit(t, cmd, &stderr)
	assertProtocolOnly(t, raw.Bytes())
}

func writeAnalysisConfig(t *testing.T, root, program string) string {
	t.Helper()

	reportDir := filepath.Join(t.TempDir(), "reports")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [
			{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": []}
		],
		"backends": {"valgrind": true, "gdb": false, "lldb": false},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_response_items": 100}
	}`, config.SchemaVersion, root, reportDir, program, root)

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned a tool error: %s", name, toolResultText(result))
	}
	return result
}

func callToolExpectError(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, arguments map[string]any) string {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !result.IsError {
		t.Fatalf("CallTool(%s) succeeded, want a tool error: %+v", name, result.StructuredContent)
	}
	return toolResultText(result)
}

func toolResultText(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal structured content: %v (%s)", err, raw)
	}
}

func containsTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
