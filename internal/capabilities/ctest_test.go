package capabilities

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func TestCTestProbeReal(t *testing.T) {
	if _, err := exec.LookPath("ctest"); err != nil {
		t.Skip("ctest is not installed")
	}
	cfg := &config.Config{Backends: config.Backends{CTest: true}}
	registry := NewRegistry(NewConfigProbes(cfg), 10*time.Second)
	records := registry.Refresh(context.Background())
	for _, record := range records {
		if record.ID != BackendCTest {
			continue
		}
		if record.State != StateAvailable {
			t.Fatalf("ctest state = %q (reason: %s, detail: %s)",
				record.State, record.Reason, record.Probe.Detail)
		}
		if record.Path == "" || record.Version == "" {
			t.Errorf("ctest capability lacks path/version: %+v", record)
		}
		return
	}
	t.Fatal("backend.ctest is not registered")
}
