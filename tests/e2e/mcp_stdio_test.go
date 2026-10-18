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

	"github.com/Morditux/traceanvil-mcp/internal/config"
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

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools(): %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "system_capabilities" {
		t.Fatalf("tools = %v, want exactly system_capabilities", toolNames(tools.Tools))
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
	if len(report.Tools) != 1 || report.Tools[0].Name != "system_capabilities" || !report.Tools[0].Exposed {
		t.Errorf("tools = %+v, want exposed system_capabilities", report.Tools)
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
