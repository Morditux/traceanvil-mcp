package capabilities

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func TestDAPProbeAvailable(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake gdb 1.0"
  exit 0
fi
body='{"seq":1,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
sleep 0.1
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.Contains(record.Version, "fake gdb") {
		t.Errorf("Version = %q", record.Version)
	}
	if record.Path == "" || !filepath.IsAbs(record.Path) {
		t.Errorf("Path = %q, want an absolute path", record.Path)
	}
	if !record.Probe.Success {
		t.Error("Probe.Success = false")
	}
	if len(record.Features) == 0 {
		t.Error("Features is empty")
	}
}

func TestDAPProbeUnsupported(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "old gdb 0.1"
  exit 0
fi
echo "unrecognized option --interpreter=dap" >&2
exit 1
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateUnsupported {
		t.Fatalf("State = %q, want unsupported (reason: %s)", record.State, record.Reason)
	}
	if !record.Probe.Success {
		t.Error("Probe.Success = false, want the probe mechanism to complete")
	}
	if !strings.Contains(record.Probe.Detail, "unrecognized") {
		t.Errorf("Detail = %q, want the adapter stderr", record.Probe.Detail)
	}
}

func TestDAPProbeTimeoutIsUnknown(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "slow gdb 1.0"
  exit 0
fi
sleep 5
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, 100*time.Millisecond)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateUnknown {
		t.Errorf("State = %q, want unknown", record.State)
	}
	if record.Probe.Success {
		t.Error("Probe.Success = true after a timeout")
	}
}

func TestDAPProbeDisabled(t *testing.T) {
	registry := NewRegistry([]Probe{NewGDBProbe(false)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateDisabled {
		t.Errorf("State = %q, want disabled", record.State)
	}
}

func TestGDBDAPProbeRealGDB(t *testing.T) {
	path, err := exec.LookPath("gdb")
	if err != nil {
		t.Skip("gdb is not installed")
	}
	if !filepath.IsAbs(path) {
		t.Skipf("gdb resolved through a relative PATH entry: %q", path)
	}

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, 5*time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.Contains(strings.ToLower(record.Version), "gdb") {
		t.Errorf("Version = %q, want a GDB version", record.Version)
	}
}

func TestConfigProbesIncludeGDBProbe(t *testing.T) {
	cfg := &config.Config{Backends: config.Backends{GDB: true}}
	probes := NewConfigProbes(cfg)
	var gdb Probe
	for _, probe := range probes {
		if probe.ID() == BackendGDB {
			gdb = probe
		}
	}
	if gdb == nil {
		t.Fatalf("NewConfigProbes() = %+v, want a GDB probe", probes)
	}
	if _, ok := gdb.(*DAPProbe); !ok {
		t.Errorf("GDB probe type = %T, want *DAPProbe", gdb)
	}
}

func TestDAPProbeMissingBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	registry := NewRegistry([]Probe{NewGDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateUnavailable {
		t.Errorf("State = %q, want unavailable", record.State)
	}
	if !strings.Contains(record.Reason, "not found") {
		t.Errorf("Reason = %q", record.Reason)
	}
}

func TestDAPProbeRejectsBrokenHardening(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake gdb 1.0"
  exit 0
fi
body='{"seq":1,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
echo "Undefined set command: debuginfod enabled" >&2
sleep 0.1
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateUnsupported {
		t.Fatalf("State = %q, want unsupported (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.Contains(record.Reason, "hardening") {
		t.Errorf("Reason = %q, want a hardening rejection", record.Reason)
	}
}
