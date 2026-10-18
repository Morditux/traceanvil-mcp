package debugger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Morditux/traceanvil-mcp/internal/dap"
)

func TestTruncateUTF8(t *testing.T) {
	value := "hello " + strings.Repeat("é", 10)
	truncated, changed := truncateUTF8(value, 10)
	if !changed {
		t.Fatal("changed = false, want truncation")
	}
	if !utf8.ValidString(truncated) {
		t.Errorf("truncated string is invalid UTF-8: %q", truncated)
	}
	if len(truncated) > 10 {
		t.Errorf("len(truncated) = %d, want <= 10", len(truncated))
	}
	if got, changed := truncateUTF8("short", 10); changed || got != "short" {
		t.Errorf("truncateUTF8(short) = %q, %t", got, changed)
	}
	if got, changed := truncateUTF8("abc", 0); !changed || got != "" {
		t.Errorf("truncateUTF8(0) = %q, %t", got, changed)
	}
}

func TestNormalizeSource(t *testing.T) {
	root := t.TempDir()
	session := &dapSession{roots: []string{root}}

	workspace := session.normalizeSource("main.c", filepath.Join(root, "src", "main.c"))
	if workspace == nil || workspace.Path != "src/main.c" || workspace.External {
		t.Errorf("workspace source = %+v", workspace)
	}
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "f.c"), []byte("int f;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	linked := session.normalizeSource("f.c", filepath.Join(root, "link", "f.c"))
	if linked == nil || linked.Path != "real/f.c" || linked.External {
		t.Errorf("symlinked workspace source = %+v", linked)
	}
	external := session.normalizeSource("libc.c", "/usr/src/libc.c")
	if external == nil || external.Path != "" || !external.External {
		t.Errorf("external source = %+v", external)
	}
	relative := session.normalizeSource("weird.c", "./relative/weird.c")
	if relative == nil || relative.Path != "" || !relative.External {
		t.Errorf("relative source = %+v", relative)
	}
	absoluteName := session.normalizeSource("/usr/src/absolute.c", "")
	if absoluteName == nil || absoluteName.Name != "absolute.c" || absoluteName.Path != "" {
		t.Errorf("absolute name source = %+v", absoluteName)
	}
	if none := session.normalizeSource("", ""); none != nil {
		t.Errorf("empty source = %+v, want nil", none)
	}
}

func TestValidateLinesAndClampCount(t *testing.T) {
	lines, err := validateLines([]int{1, 2, 2, 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[0] != 1 || lines[2] != 100 {
		t.Errorf("lines = %v", lines)
	}
	for _, invalid := range [][]int{nil, {0}, {-1}, {maxSourceLine + 1}} {
		if _, err := validateLines(invalid); err == nil {
			t.Errorf("validateLines(%v) succeeded", invalid)
		}
	}
	tooMany := make([]int, maxBreakpoints+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	if _, err := validateLines(tooMany); err == nil {
		t.Error("validateLines accepted too many lines")
	}

	if got := clampCount(0, 10, 100); got != 10 {
		t.Errorf("clampCount(0) = %d", got)
	}
	if got := clampCount(1000, 10, 100); got != 10 {
		t.Errorf("clampCount(1000) = %d", got)
	}
	if got := clampCount(5, 10, 100); got != 5 {
		t.Errorf("clampCount(5) = %d", got)
	}
	if got := clampCount(500, 1000, 100); got != 100 {
		t.Errorf("clampCount(hard max) = %d", got)
	}
}

func TestMapDAPError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code ErrorCode
	}{
		{"deadline", context.DeadlineExceeded, CodeTimeout},
		{"canceled", context.Canceled, CodeCanceled},
		{"closed", dap.ErrClosed, CodeSessionTerminated},
		{"eof", dap.ErrEOF, CodeSessionTerminated},
		{"response", &dap.ResponseError{Command: "pause", Message: "not supported"}, CodeUnsupported},
		{"protocol", errors.New("boom"), CodeProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeOf(mapDAPError(tc.err, "test")); got != tc.code {
				t.Errorf("CodeOf() = %q, want %q", got, tc.code)
			}
		})
	}
	if err := mapDAPError(nil, "test"); err != nil {
		t.Errorf("mapDAPError(nil) = %v", err)
	}
}

func TestValidateDebuggerBinary(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "gdb")
	if err := os.WriteFile(valid, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateDebuggerBinary(valid); err != nil {
		t.Errorf("validateDebuggerBinary(valid) error = %v", err)
	}
	if err := validateDebuggerBinary(filepath.Join(root, "missing")); err == nil {
		t.Error("missing binary accepted")
	}
	if err := validateDebuggerBinary(root); err == nil {
		t.Error("directory accepted as a binary")
	}
	notExecutable := filepath.Join(root, "plain")
	if err := os.WriteFile(notExecutable, []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateDebuggerBinary(notExecutable); err == nil {
		t.Error("non-executable file accepted")
	}
	if err := validateDebuggerBinary("relative"); err == nil {
		t.Error("relative path accepted")
	}
}

func TestStateTerminalAndErrorCodes(t *testing.T) {
	for _, state := range []State{StateTerminated, StateDetached, StateFailed} {
		if !state.Terminal() {
			t.Errorf("%q is not terminal", state)
		}
	}
	for _, state := range []State{StateStarting, StateRunning, StateStopped} {
		if state.Terminal() {
			t.Errorf("%q is terminal", state)
		}
	}
	err := Wrap(CodeBackendUnavailable, errors.New("cause"), "backend is gone")
	if CodeOf(err) != CodeBackendUnavailable || !strings.Contains(err.Error(), "backend_unavailable") {
		t.Errorf("error = %v", err)
	}
}
