package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeConfigFile(t *testing.T, backends string) string {
	t.Helper()

	root := t.TempDir()
	reportDir := filepath.Join(t.TempDir(), "reports")

	doc := fmt.Sprintf(`{
		"schema_version": 1,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [],
		"backends": %s,
		"policy": {"profile": "safe-default", "attach": false, "evaluate": false}
	}`, root, reportDir, backends)

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFile(path, doc); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantCode:   ExitSuccess,
			wantStdout: "mcp-native-diagnostics ",
		},
		{
			name:       "help flag",
			args:       []string{"--help"},
			wantCode:   ExitSuccess,
			wantStdout: "Usage:",
		},
		{
			name:       "help command lists serve",
			args:       []string{"help"},
			wantCode:   ExitSuccess,
			wantStdout: "serve",
		},
		{
			name:       "no arguments is a usage error",
			args:       nil,
			wantCode:   ExitUsage,
			wantStderr: "Usage:",
		},
		{
			name:       "unknown command",
			args:       []string{"bogus"},
			wantCode:   ExitUsage,
			wantStderr: `unknown command "bogus"`,
		},
		{
			name:       "version rejects extra arguments",
			args:       []string{"version", "--json"},
			wantCode:   ExitUsage,
			wantStderr: "version takes no arguments",
		},
		{
			name:       "help rejects extra arguments",
			args:       []string{"help", "extra"},
			wantCode:   ExitUsage,
			wantStderr: "help takes no arguments",
		},
		{
			name:       "serve requires config",
			args:       []string{"serve"},
			wantCode:   ExitUsage,
			wantStderr: "--config is required",
		},
		{
			name:       "serve rejects unknown flag",
			args:       []string{"serve", "--bogus"},
			wantCode:   ExitUsage,
			wantStderr: "flag provided but not defined",
		},
		{
			name:       "serve rejects positional arguments",
			args:       []string{"serve", "--config", "x.json", "extra"},
			wantCode:   ExitUsage,
			wantStderr: "unexpected arguments",
		},
		{
			name:       "serve rejects invalid config",
			args:       []string{"serve", "--config", "does-not-exist.json"},
			wantCode:   ExitFailure,
			wantStderr: "config:",
		},
		{
			name:       "doctor rejects missing config",
			args:       []string{"doctor", "--config", "does-not-exist.json"},
			wantCode:   ExitFailure,
			wantStderr: "config:",
		},
		{
			name:       "validate-config rejects missing config",
			args:       []string{"validate-config", "--config", "does-not-exist.json"},
			wantCode:   ExitFailure,
			wantStderr: "config:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tt.args...)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout == "" && stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout, tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout, tt.wantStdout)
			}
			if tt.wantStderr == "" && stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestRunVersionWritesOnlyOneLine(t *testing.T) {
	code, stdout, stderr := runCLI(t, "version")

	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if lines := strings.Count(stdout, "\n"); lines != 1 {
		t.Errorf("stdout has %d lines, want 1: %q", lines, stdout)
	}
}

func TestValidateConfig(t *testing.T) {
	path := writeConfigFile(t, `{"valgrind": false, "gdb": false, "lldb": false}`)

	code, stdout, stderr := runCLI(t, "validate-config", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitSuccess, stderr)
	}
	if !strings.Contains(stdout, "config valid:") {
		t.Errorf("stdout = %q, want a success message", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestValidateConfigRejectsInvalidDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFile(path, `{"schema_version": 99}`); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "validate-config", "--config", path)
	if code != ExitFailure {
		t.Fatalf("exit code = %d, want %d", code, ExitFailure)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "schema_version") {
		t.Errorf("stderr = %q, want a schema_version error", stderr)
	}
}

func TestDoctorReportsDisabledBackends(t *testing.T) {
	path := writeConfigFile(t, `{"valgrind": false, "gdb": false, "lldb": false}`)

	code, stdout, stderr := runCLI(t, "doctor", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitSuccess, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"profile: safe-default",
		"backend.valgrind",
		"disabled",
		"tools:",
		"system_capabilities",
		"warnings:",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorFlagsMissingPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-root")
	reportDir := filepath.Join(t.TempDir(), "missing-reports")
	program := filepath.Join(root, "fixture")
	doc := fmt.Sprintf(`{
		"schema_version": 1,
		"workspace_roots": [%q],
		"report_dir": %q,
		"allowed_targets": [
			{"id": "fixture", "program": %q, "cwd": %q, "allowed_args": []}
		],
		"backends": {"valgrind": false, "gdb": false, "lldb": false}
	}`, root, reportDir, program, root)

	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFile(path, doc); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCLI(t, "doctor", "--config", path)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitSuccess, stderr)
	}
	for _, want := range []string{
		"workspace root",
		"does not exist",
		"report directory",
		"target \"fixture\" program",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
