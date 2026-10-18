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
	"github.com/Morditux/traceanvil-mcp/internal/profile"
)

const e2eProfileSource = `#include <stdlib.h>
#include <string.h>
static volatile unsigned long sink;
static void work_a(unsigned n) { for (unsigned i = 0; i < n; i++) sink += i; }
static void work_b(unsigned n) { for (unsigned i = 0; i < n; i++) sink ^= (i * 3u); }
int main(void) {
  work_a(1000);
  work_b(2000);
  unsigned char *buf = malloc(1024 * 1024);
  if (!buf) return 1;
  memset(buf, 1, 1024 * 1024);
  sink += buf[0];
  free(buf);
  return 0;
}
`

func TestServeProfileWorkflow(t *testing.T) {
	if _, err := exec.LookPath("valgrind"); err != nil {
		t.Skip("valgrind is not installed")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("cc is not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "profile.c")
	if err := os.WriteFile(source, []byte(e2eProfileSource), 0o600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "profile-fixture")
	if output, err := exec.Command(cc, "-g", "-O0", "-fno-omit-frame-pointer", "-o", program, source).CombinedOutput(); err != nil {
		t.Fatalf("compile profile fixture: %v\n%s", err, output)
	}
	reportDir := filepath.Join(t.TempDir(), "reports")
	doc := fmt.Sprintf(`{
		"schema_version": %d,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": []}],
		"backends": {"valgrind": true, "gdb": false, "lldb": false, "sanitizers": false, "ctest": false},
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false},
		"limits": {"timeout_seconds": 60, "max_jobs": 1, "max_sessions": 1,
		           "max_report_bytes": 16777216, "max_response_items": 100}
	}`, config.SchemaVersion, root, reportDir, program, root)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

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
	client := mcp.NewClient(&mcp.Implementation{Name: "profile-e2e", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v\nserver stderr:\n%s", err, stderr.String())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"profile_callgrind", "profile_massif", "profile_compare", "profile_artifact"} {
		if !containsTool(tools.Tools, name) {
			t.Fatalf("tools = %v, missing %s", toolNames(tools.Tools), name)
		}
	}
	callgrindResult := callTool(t, ctx, session, "profile_callgrind", map[string]any{"target_id": "fixture"})
	var callgrind profile.Report
	decodeStructured(t, callgrindResult, &callgrind)
	if !callgrind.Complete || len(callgrind.Events) == 0 {
		t.Fatalf("callgrind report = %+v", callgrind)
	}
	massifResult := callTool(t, ctx, session, "profile_massif", map[string]any{"target_id": "fixture"})
	var massif profile.Report
	decodeStructured(t, massifResult, &massif)
	if !massif.Complete || massif.PeakHeapBytes < 1024*1024 {
		t.Fatalf("massif report = %+v", massif)
	}
	compareResult := callTool(t, ctx, session, "profile_compare", map[string]any{
		"base_run_id": callgrind.RunID, "target_run_id": callgrind.RunID,
	})
	var comparison profile.Comparison
	decodeStructured(t, compareResult, &comparison)
	if !comparison.Compatible {
		t.Fatalf("comparison = %+v", comparison)
	}
	artifactResult := callTool(t, ctx, session, "profile_artifact", map[string]any{
		"run_id": callgrind.RunID, "file_name": "callgrind.out", "max_bytes": 256,
	})
	var artifact profile.ArtifactOutput
	decodeStructured(t, artifactResult, &artifact)
	if artifact.Content == "" || !artifact.HasMore {
		t.Fatalf("artifact = %+v", artifact)
	}
	encoded, err := json.Marshal(massif)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("massif JSON: %v", err)
	}
}
