package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Morditux/traceanvil-mcp/internal/config"
	"github.com/Morditux/traceanvil-mcp/internal/ctest"
)

func TestServeCTestDiscovery(t *testing.T) {
	cmake, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("cmake is not installed")
	}
	ctestBinary, err := exec.LookPath("ctest")
	if err != nil {
		t.Skip("ctest is not installed")
	}
	if !filepath.IsAbs(cmake) || !filepath.IsAbs(ctestBinary) {
		t.Skip("cmake/ctest resolved through a relative PATH entry")
	}
	root := t.TempDir()
	build := filepath.Join(root, "build")
	configure := exec.Command(cmake, "-S", filepath.Join("..", "..", "testdata", "fixtures", "ctest"), "-B", build)
	configure.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := configure.CombinedOutput(); err != nil {
		t.Fatalf("configure CTest fixture: %v\n%s", err, output)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [],
		"backends": {"valgrind": false, "gdb": false, "lldb": false, "sanitizers": false, "ctest": true},
		"ctest": {
			"test_timeout_seconds": 30,
			"projects": [{
				"id": "fixture",
				"test_dir": %q,
				"allowed_tests": ["pass", "fail"]
			}]
		},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_sessions": 1,
		           "max_report_bytes": 1048576, "max_response_items": 100}
	}`, config.SchemaVersion, root, reportDir, build)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

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
	client := mcp.NewClient(&mcp.Implementation{Name: "ctest-e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ctest_discover", "ctest_run", "ctest_status", "ctest_cancel", "ctest_report", "ctest_artifact"} {
		if !containsTool(tools.Tools, name) {
			t.Errorf("tools = %v, missing %s", toolNames(tools.Tools), name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ctest_discover", Arguments: map[string]any{"project_id": "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("ctest_discover failed: %s", toolResultText(result))
	}
	rawResult, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var discovery ctest.Discovery
	if err := json.Unmarshal(rawResult, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.Tests) != 2 || !discovery.Complete {
		t.Fatalf("discovery = %+v", discovery)
	}
	if err := session.Close(); err != nil {
		t.Logf("close session: %v", err)
	}
	waitForExit(t, cmd, &stderr)
	assertProtocolOnly(t, raw.Bytes())
}
