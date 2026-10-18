package capabilities

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

// readDAPInitializeRequest makes the fake adapter wait for the complete client
// request before responding. Sending a canned response immediately can race
// request registration under the race detector. After responding, fixtures
// drain stdin until the client closes it instead of guessing a sleep duration.
const readDAPInitializeRequest = `
carriage_return="$(printf '\r')"
IFS=' ' read -r header length || exit 1
[ "$header" = "Content-Length:" ] || exit 1
length="${length%"$carriage_return"}"
IFS= read -r blank || exit 1
[ "$blank" = "$carriage_return" ] || exit 1
dd bs=1 count="$length" of=/dev/null 2>/dev/null || exit 1
`

func TestDAPProbeAvailable(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake gdb 1.0"
  exit 0
fi
`+readDAPInitializeRequest+`body='{"seq":1,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
cat >/dev/null
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

func TestDAPProbeEOFBeforeInitialize(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "gdb", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "fake gdb 1.0"
  exit 0
fi
`+readDAPInitializeRequest+`exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{NewGDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateUnsupported {
		t.Fatalf("State = %q, want unsupported (reason: %s)", record.State, record.Reason)
	}
	if !strings.Contains(record.Reason, "before completing") {
		t.Errorf("Reason = %q, want an incomplete handshake", record.Reason)
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

func TestConfigProbesIncludeGDBAndLLDBProbes(t *testing.T) {
	cfg := &config.Config{Backends: config.Backends{GDB: true, LLDB: true}}
	probes := NewConfigProbes(cfg)
	var gdb, lldb Probe
	for _, probe := range probes {
		switch probe.ID() {
		case BackendGDB:
			gdb = probe
		case BackendLLDB:
			lldb = probe
		}
	}
	if gdb == nil {
		t.Fatalf("NewConfigProbes() = %+v, want a GDB probe", probes)
	}
	if _, ok := gdb.(*DAPProbe); !ok {
		t.Errorf("GDB probe type = %T, want *DAPProbe", gdb)
	}
	if lldb == nil {
		t.Fatalf("NewConfigProbes() = %+v, want an LLDB probe", probes)
	}
	if _, ok := lldb.(*DAPProbe); !ok {
		t.Errorf("LLDB probe type = %T, want *DAPProbe", lldb)
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
`+readDAPInitializeRequest+`body='{"seq":1,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
echo "Undefined set command: debuginfod enabled" >&2
cat >/dev/null
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

func TestLLDBProbeVersionsDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "lldb-dap-21", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "lldb-dap 21.1.8"
  exit 0
fi
`+readDAPInitializeRequest+`body='{"seq":0,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
cat >/dev/null
`)
	t.Setenv("PATH", dir)

	registry := NewRegistry([]Probe{NewLLDBProbe(true)}, time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.HasSuffix(record.Path, "lldb-dap-21") {
		t.Errorf("Path = %q, want the versioned adapter", record.Path)
	}
	if record.Version != "lldb-dap 21.1.8" {
		t.Errorf("Version = %q", record.Version)
	}
}

func TestLLDBProbeRealLLDB(t *testing.T) {
	path, err := exec.LookPath("lldb-dap")
	if err != nil {
		t.Skip("lldb-dap is not installed")
	}
	if !filepath.IsAbs(path) {
		t.Skipf("lldb-dap resolved through a relative PATH entry: %q", path)
	}
	registry := NewRegistry([]Probe{NewLLDBProbe(true)}, 5*time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.Contains(strings.ToLower(record.Version), "lldb") {
		t.Errorf("Version = %q", record.Version)
	}
}

func TestLLDBProbeToleratesSlowVersion(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "lldb-dap-21", `#!/bin/sh
if [ "$1" = "--version" ]; then
  /bin/sleep 3
  exit 0
fi
`+readDAPInitializeRequest+`body='{"seq":0,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
cat >/dev/null
`)
	t.Setenv("PATH", dir)

	registry := NewRegistry([]Probe{NewLLDBProbe(true)}, 5*time.Second)
	start := time.Now()
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("probe took %s, should not wait for the full --version hang", elapsed)
	}
	if record.Version != "" {
		t.Errorf("Version = %q, want empty when the optional version probe times out", record.Version)
	}
}

func TestLLDBDiscoveryOrderAndCap(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"lldb-dap", "lldb-dap-3", "lldb-dap-9", "lldb-dap-14", "lldb-dap-21"} {
		writeExecutable(t, dir, name, "#!/bin/sh\nexit 0\n")
	}
	t.Setenv("PATH", dir)
	candidates := discoverLLDBDAP()
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, filepath.Base(candidate))
	}
	want := []string{"lldb-dap", "lldb-dap-21"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discoverLLDBDAP() = %v, want %v", got, want)
	}
}

func TestLLDBDiscoverySkipsRelativePathButFindsLaterAbsolute(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "lldb-dap", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", "relative"+string(os.PathListSeparator)+dir)
	candidates := discoverLLDBDAP()
	if len(candidates) != 1 || filepath.Base(candidates[0]) != "lldb-dap" {
		t.Fatalf("discoverLLDBDAP() = %v, want the absolute lldb-dap", candidates)
	}
}

func TestLLDBProbeFallsBackAfterHungCandidate(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "lldb-dap", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "broken lldb-dap"
  exit 0
fi
/bin/sleep 10
`)
	writeExecutable(t, dir, "lldb-dap-21", `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "lldb-dap 21.1.8"
  exit 0
fi
`+readDAPInitializeRequest+`body='{"seq":0,"type":"response","request_seq":1,"command":"initialize","success":true,"body":{"supportsConfigurationDoneRequest":true}}'
printf 'Content-Length: %d\r\n\r\n%s' "${#body}" "$body"
cat >/dev/null
`)
	t.Setenv("PATH", dir)

	registry := NewRegistry([]Probe{NewLLDBProbe(true)}, 5*time.Second)
	record := registry.Refresh(context.Background())[0]
	if record.State != StateAvailable {
		t.Fatalf("State = %q, want available (reason: %s, detail: %s)",
			record.State, record.Reason, record.Probe.Detail)
	}
	if !strings.HasSuffix(record.Path, "lldb-dap-21") {
		t.Errorf("Path = %q, want the working versioned candidate", record.Path)
	}
}
