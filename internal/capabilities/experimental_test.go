package capabilities

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestVgdbProbeReal(t *testing.T) {
	if _, err := exec.LookPath("vgdb"); err != nil {
		t.Skip("vgdb is not installed")
	}
	probe := NewVgdbProbe(true)
	result := probe.Run(context.Background())
	if result.State != StateAvailable {
		t.Fatalf("vgdb probe = %+v", result)
	}
	if result.Path == "" || result.Reason == "" {
		t.Fatalf("vgdb probe = %+v", result)
	}
}

func TestPerfProbeDisabled(t *testing.T) {
	result := NewPerfProbe(false).Run(context.Background())
	if result.State != StateDisabled {
		t.Fatalf("disabled perf probe = %+v", result)
	}
}

func TestPerfProbeRestrictedHostDoesNotExecute(t *testing.T) {
	if _, err := exec.LookPath("perf"); err != nil {
		t.Skip("perf is not installed")
	}
	scope, caps, err := perfPermissions()
	if err != nil {
		t.Skipf("cannot inspect perf permissions: %v", err)
	}
	if scope < 3 || caps.perfmon || caps.sysAdmin {
		t.Skip("host permits perf; restricted branch not exercised")
	}
	result := NewPerfProbe(true).Run(context.Background())
	if result.State != StateUnavailable {
		t.Fatalf("restricted perf probe = %+v", result)
	}
	if result.Reason == "" || result.Detail != "" {
		t.Fatalf("restricted probe reason/detail = %+v", result)
	}
}

func TestPerfProbeMetadataReadable(t *testing.T) {
	if _, err := os.Stat("/proc/sys/kernel/perf_event_paranoid"); err != nil {
		t.Skip("perf_event_paranoid is not readable")
	}
	if _, _, err := perfPermissions(); err != nil {
		t.Fatalf("perfPermissions() error = %v", err)
	}
}
