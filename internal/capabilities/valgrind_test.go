package capabilities

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/config"
)

func TestValgrindConcurrencyToolProbesReal(t *testing.T) {
	if _, err := exec.LookPath("valgrind"); err != nil {
		t.Skip("valgrind is not installed")
	}
	cfg := &config.Config{Backends: config.Backends{Valgrind: true}}
	registry := NewRegistry(NewConfigProbes(cfg), 15*time.Second)
	records := registry.Refresh(context.Background())
	want := map[string]bool{
		BackendValgrind:         false,
		BackendValgrindHelgrind: false,
		BackendValgrindDRD:      false,
	}
	for _, record := range records {
		if _, ok := want[record.ID]; !ok {
			continue
		}
		if record.State != StateAvailable {
			t.Errorf("%s state = %q (reason: %s)", record.ID, record.State, record.Reason)
			continue
		}
		want[record.ID] = true
	}
	for id, ok := range want {
		if !ok {
			t.Errorf("%s is not available", id)
		}
	}
}
