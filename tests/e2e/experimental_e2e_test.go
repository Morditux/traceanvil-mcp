package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/vgdb"
)

func TestServeVgdbExperimentalWorkflow(t *testing.T) {
	if _, err := exec.LookPath("vgdb"); err != nil {
		t.Skip("vgdb is not installed")
	}
	if _, err := exec.LookPath("valgrind"); err != nil {
		t.Skip("valgrind is not installed")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "fixture.c")
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(source, []byte("#include <stdio.h>\nint main(void){ puts(\"ok\"); return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(cc, "-g", "-O0", "-o", program, source).CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, output)
	}
	configPath, cleanup := writeExperimentalConfig(t, root, program, true, false)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, stderr := startExperimentalServer(t, ctx, configPath)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsTool(tools.Tools, "vgdb_inspect") {
		t.Fatalf("tools = %v, missing vgdb_inspect\nserver stderr:\n%s", toolNames(tools.Tools), stderr.String())
	}
	if containsTool(tools.Tools, "profile_perf") {
		t.Error("profile_perf is exposed without a successful permission probe")
	}
	result := callTool(t, ctx, session, "vgdb_inspect", map[string]any{"target_id": "fixture"})
	var report vgdb.Report
	decodeStructured(t, result, &report)
	if !report.Experimental || !report.Complete || len(report.Commands) != 3 {
		t.Fatalf("vgdb report = %+v", report)
	}
	if report.Commands[0].Command != "v.info gdbserver_status" {
		t.Fatalf("fixed command = %+v", report.Commands[0])
	}
}

func TestServePerfRestrictionIsVisible(t *testing.T) {
	if _, err := exec.LookPath("perf"); err != nil {
		t.Skip("perf is not installed")
	}
	root := t.TempDir()
	program := filepath.Join(root, "fixture")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath, cleanup := writeExperimentalConfig(t, root, program, false, true)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, _ := startExperimentalServer(t, ctx, configPath)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// On a restricted host profile_perf must not be advertised. If the host
	// permits perf, this assertion is intentionally skipped.
	if containsTool(tools.Tools, "profile_perf") {
		t.Skip("host permits perf; restriction branch not exercised")
	}
	result := callTool(t, ctx, session, "system_capabilities", nil)
	text := toolResultText(result)
	if !containsText(text, "backend.perf") {
		t.Fatalf("system_capabilities does not report backend.perf: %s", text)
	}
}

func writeExperimentalConfig(t *testing.T, root, program string, vgdbEnabled, perfEnabled bool) (string, func()) {
	t.Helper()
	reportDir := filepath.Join(t.TempDir(), "reports")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": []}],
		"backends": {"valgrind": %t, "gdb": false, "lldb": false, "sanitizers": false, "ctest": false, "perf": %t, "vgdb": %t},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_sessions": 1,
		           "max_report_bytes": 16777216, "max_response_items": 100}
	}`, config.SchemaVersion, root, reportDir, program, root, vgdbEnabled || perfEnabled, perfEnabled, vgdbEnabled)
	return configPathFromDoc(t, doc), func() {}
}

func startExperimentalServer(t *testing.T, ctx context.Context, configPath string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binaryPath, "serve", "--config", configPath)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	transport := &mcp.IOTransport{Reader: io.NopCloser(stdoutPipe), Writer: stdinPipe}
	client := mcp.NewClient(&mcp.Implementation{Name: "experimental-e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		_ = session.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return session, stderr
}

func configPathFromDoc(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsText(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
