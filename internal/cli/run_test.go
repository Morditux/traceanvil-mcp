package cli

import (
	"bytes"
	"strings"
	"testing"
)

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
			name:       "help command",
			args:       []string{"help"},
			wantCode:   ExitSuccess,
			wantStdout: "Commands:",
		},
		{
			name:       "no arguments is a usage error",
			args:       nil,
			wantCode:   ExitUsage,
			wantStderr: "Usage:",
		},
		{
			name:       "unknown command",
			args:       []string{"serve"},
			wantCode:   ExitUsage,
			wantStderr: `unknown command "serve"`,
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout == "" && stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr == "" && stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRunVersionWritesOnlyOneLine(t *testing.T) {
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"version"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	if lines := strings.Count(stdout.String(), "\n"); lines != 1 {
		t.Errorf("stdout has %d lines, want 1: %q", lines, stdout.String())
	}
}
