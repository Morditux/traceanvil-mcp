package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServeInProcess(t *testing.T) {
	configPath := writeConfigFile(t, `{"valgrind": false, "gdb": false, "lldb": false}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverStdin, clientStdin := io.Pipe()
	clientStdout, serverStdout := io.Pipe()
	t.Cleanup(func() {
		_ = serverStdin.Close()
		_ = clientStdin.Close()
		_ = clientStdout.Close()
		_ = serverStdout.Close()
	})

	var stderr lockedBuffer
	exit := make(chan int, 1)
	go func() {
		exit <- Run(ctx, []string{"serve", "--config", configPath}, serverStdin, serverStdout, &stderr)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "in-process", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: clientStdout, Writer: clientStdin}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v\nstderr:\n%s", err, stderr.String())
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "system_capabilities" {
		t.Fatalf("tools = %d, want only system_capabilities", len(tools.Tools))
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "system_capabilities",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() returned an error result: %+v", result.Content)
	}

	if err := session.Close(); err != nil {
		t.Logf("close session: %v", err)
	}

	select {
	case code := <-exit:
		if code != ExitSuccess {
			t.Fatalf("serve exit code = %d, want %d\nstderr:\n%s", code, ExitSuccess, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("serve did not stop after the client disconnected\nstderr:\n%s", stderr.String())
	}

	if !strings.Contains(stderr.String(), "serving MCP over stdio") {
		t.Errorf("stderr = %q, want a startup log line", stderr.String())
	}
}

func TestServeHelp(t *testing.T) {
	code, stdout, stderr := runCLI(t, "serve", "--help")

	if code != ExitSuccess {
		t.Errorf("exit code = %d, want %d", code, ExitSuccess)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Usage: mcp-native-diagnostics serve") {
		t.Errorf("stderr = %q, want serve usage", stderr)
	}
}

func TestDoctorFlagsNonDirectoryPaths(t *testing.T) {
	root := t.TempDir()
	programDir := root + "/program-dir"
	if err := os.Mkdir(programDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plainProgram := root + "/plain-program"
	cwdFile := root + "/cwd-file"
	reportFile := root + "/report-file"

	if err := writeFile(plainProgram, "not executable"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(cwdFile, "not a directory"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(reportFile, "not a directory"); err != nil {
		t.Fatal(err)
	}

	doc := `{
		"schema_version": 1,
		"workspace_roots": [` + strconv.Quote(root) + `],
		"report_dir": ` + strconv.Quote(reportFile) + `,
		"allowed_targets": [
			{"id": "dir-program", "program": ` + strconv.Quote(programDir) + `, "cwd": ` + strconv.Quote(root) + `, "allowed_args": []},
			{"id": "plain-program", "program": ` + strconv.Quote(plainProgram) + `, "cwd": ` + strconv.Quote(cwdFile) + `, "allowed_args": []}
		],
		"backends": {"valgrind": false, "gdb": false, "lldb": false}
	}`

	path := root + "/config.json"
	if err := writeFile(path, doc); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "doctor", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitSuccess, stderr)
	}
	for _, want := range []string{
		"is a directory",     // target program is a directory
		"is not executable",  // plain program
		"is not a directory", // cwd and report directory
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorFlagsNonDirectoryRoot(t *testing.T) {
	rootFile := t.TempDir() + "/root-file"
	if err := writeFile(rootFile, "not a directory"); err != nil {
		t.Fatal(err)
	}
	reportDir := t.TempDir()

	doc := `{
		"schema_version": 1,
		"workspace_roots": [` + strconv.Quote(rootFile) + `],
		"report_dir": ` + strconv.Quote(reportDir) + `,
		"allowed_targets": [],
		"backends": {"valgrind": false, "gdb": false, "lldb": false}
	}`
	path := t.TempDir() + "/config.json"
	if err := writeFile(path, doc); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "doctor", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "workspace root") || !strings.Contains(stdout, "is not a directory") {
		t.Errorf("stdout does not flag the workspace root:\n%s", stdout)
	}
}

// lockedBuffer is safe for concurrent writes from the server goroutine and
// reads from the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
