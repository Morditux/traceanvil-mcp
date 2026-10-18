package capabilities

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProbe struct {
	id       string
	backend  string
	features []string
	result   Result
	sleep    time.Duration
	panic    bool
}

func (p *fakeProbe) ID() string         { return p.id }
func (p *fakeProbe) Backend() string    { return p.backend }
func (p *fakeProbe) Features() []string { return p.features }
func (p *fakeProbe) Run(context.Context) Result {
	if p.sleep > 0 {
		time.Sleep(p.sleep)
	}
	if p.panic {
		panic("boom")
	}
	return p.result
}

func TestRegistryRefreshSortsAndStores(t *testing.T) {
	registry := NewRegistry([]Probe{
		&fakeProbe{id: "z", backend: "z", result: Result{State: StateAvailable, OK: true}},
		&fakeProbe{id: "a", backend: "a", result: Result{State: StateDisabled, Reason: "off", OK: true}},
	}, time.Second)

	records := registry.Refresh(context.Background())
	if len(records) != 2 {
		t.Fatalf("Refresh() returned %d records, want 2", len(records))
	}
	if records[0].ID != "a" || records[1].ID != "z" {
		t.Errorf("records order = %q, %q; want a, z", records[0].ID, records[1].ID)
	}
	if registry.State("z") != StateAvailable {
		t.Errorf("State(z) = %q, want %q", registry.State("z"), StateAvailable)
	}
	if registry.State("missing") != StateUnknown {
		t.Errorf("State(missing) = %q, want %q", registry.State("missing"), StateUnknown)
	}
}

func TestRegistryProbeStates(t *testing.T) {
	tests := []struct {
		name       string
		probe      Probe
		wantState  State
		wantReason string
	}{
		{
			name:       "disabled",
			probe:      NewBinaryProbe("x", "x", "unused", false, nil),
			wantState:  StateDisabled,
			wantReason: "disabled by configuration",
		},
		{
			name:       "missing binary",
			probe:      NewBinaryProbe("x", "x", "definitely-not-a-real-binary-xyz", true, nil),
			wantState:  StateUnavailable,
			wantReason: "not found on PATH",
		},
		{
			name:       "panic is contained",
			probe:      &fakeProbe{id: "x", backend: "x", panic: true},
			wantState:  StateUnknown,
			wantReason: "probe panicked",
		},
		{
			name:       "invalid state is normalized",
			probe:      &fakeProbe{id: "x", backend: "x", result: Result{State: State("bogus"), OK: true}},
			wantState:  StateUnknown,
			wantReason: "invalid state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewRegistry([]Probe{tt.probe}, time.Second)
			records := registry.Refresh(context.Background())

			if len(records) != 1 {
				t.Fatalf("Refresh() returned %d records, want 1", len(records))
			}
			if records[0].State != tt.wantState {
				t.Errorf("State = %q, want %q", records[0].State, tt.wantState)
			}
			if !strings.Contains(records[0].Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to contain %q", records[0].Reason, tt.wantReason)
			}
		})
	}
}

func TestRegistryTimeout(t *testing.T) {
	probe := &fakeProbe{
		id:      "slow",
		backend: "slow",
		sleep:   200 * time.Millisecond,
		result:  Result{State: StateAvailable, OK: true},
	}
	registry := NewRegistry([]Probe{probe}, 10*time.Millisecond)

	records := registry.Refresh(context.Background())
	if records[0].State != StateUnknown {
		t.Errorf("State = %q, want %q", records[0].State, StateUnknown)
	}
	if !strings.Contains(records[0].Reason, "timed out") {
		t.Errorf("Reason = %q, want a timeout reason", records[0].Reason)
	}
	if records[0].Probe.Success {
		t.Error("Probe.Success = true, want false")
	}
}

// deadlineProbe returns an available result immediately after its context
// expires, exercising the race between the result channel and the deadline.
type deadlineProbe struct{}

func (deadlineProbe) ID() string         { return "late" }
func (deadlineProbe) Backend() string    { return "late" }
func (deadlineProbe) Features() []string { return nil }
func (deadlineProbe) Run(ctx context.Context) Result {
	<-ctx.Done()
	return Result{State: StateAvailable, OK: true}
}

func TestRegistryRejectsLateAvailableResult(t *testing.T) {
	registry := NewRegistry([]Probe{deadlineProbe{}}, 10*time.Millisecond)

	// Repeat because select may pick either ready branch; the invariant must
	// hold regardless of scheduling.
	for range 20 {
		records := registry.Refresh(context.Background())
		record := records[0]
		if record.State != StateUnknown {
			t.Fatalf("State = %q, want %q for a result returned at the deadline", record.State, StateUnknown)
		}
		if !strings.Contains(record.Reason, "timed out") {
			t.Fatalf("Reason = %q, want a timeout reason", record.Reason)
		}
	}
}

func TestRegistryInvalidSet(t *testing.T) {
	registry := NewRegistry(nil, time.Second)

	if err := registry.Set(Capability{ID: "", Kind: "backend", State: StateAvailable}); err == nil {
		t.Fatal("Set() error = nil, want an error")
	}
	if err := registry.Set(Capability{ID: "x", Kind: "backend", State: State("bogus")}); err == nil {
		t.Fatal("Set() error = nil, want an error")
	}
	if err := registry.Set(Capability{ID: "x", Kind: "backend", State: StateAvailable}); err != nil {
		t.Fatalf("Set() error = %v, want nil", err)
	}
	if record, ok := registry.Get("x"); !ok || record.LastChecked == "" {
		t.Errorf("Get(x) = %+v, %t; want a timestamped record", record, ok)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	probes := []Probe{
		&fakeProbe{id: "a", backend: "a", result: Result{State: StateAvailable, OK: true}},
		&fakeProbe{id: "b", backend: "b", result: Result{State: StateUnknown, OK: true}},
	}
	registry := NewRegistry(probes, time.Second)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			registry.Refresh(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = registry.Snapshot()
			_ = registry.State("a")
		}()
	}
	wg.Wait()

	if got := len(registry.Snapshot()); got != 2 {
		t.Errorf("Snapshot() length = %d, want 2", got)
	}
}

func TestBinaryProbeExecutesVersion(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "fake-tool", "#!/bin/sh\necho 'fake-tool 1.2.3'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{
		NewBinaryProbe("fake", "fake", "fake-tool", true, []string{"feature"}),
	}, time.Second)

	records := registry.Refresh(context.Background())
	record := records[0]
	if record.State != StateUnknown {
		t.Errorf("State = %q, want %q (binary discovery only)", record.State, StateUnknown)
	}
	if record.Version != "fake-tool 1.2.3" {
		t.Errorf("Version = %q, want %q", record.Version, "fake-tool 1.2.3")
	}
	if !filepath.IsAbs(record.Path) {
		t.Errorf("Path = %q, want an absolute path", record.Path)
	}
	if !record.Probe.Success {
		t.Errorf("Probe.Success = false, want true (%+v)", record.Probe)
	}
}

func TestBinaryProbeVersionFailure(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "broken-tool", "#!/bin/sh\necho 'boom' >&2\nexit 3\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{
		NewBinaryProbe("broken", "broken", "broken-tool", true, nil),
	}, time.Second)

	records := registry.Refresh(context.Background())
	record := records[0]
	if record.State != StateUnknown {
		t.Errorf("State = %q, want %q", record.State, StateUnknown)
	}
	if !strings.Contains(record.Reason, "version probe failed") {
		t.Errorf("Reason = %q, want a version failure", record.Reason)
	}
	if record.Probe.Success {
		t.Error("Probe.Success = true, want false")
	}
}

func TestBinaryProbeVersionTimeout(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "slow-tool", "#!/bin/sh\nsleep 5\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{
		NewBinaryProbe("slow", "slow", "slow-tool", true, nil),
	}, 50*time.Millisecond)

	records := registry.Refresh(context.Background())
	record := records[0]
	if record.State != StateUnknown {
		t.Errorf("State = %q, want %q", record.State, StateUnknown)
	}
	if !strings.Contains(record.Reason, "timed out") {
		t.Errorf("Reason = %q, want a timeout reason", record.Reason)
	}
}

func TestBinaryProbeWithoutVersion(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "adapter", "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{
		NewBinaryProbe("adapter", "adapter", "adapter", true, nil).WithoutVersionProbe(),
	}, time.Second)

	records := registry.Refresh(context.Background())
	record := records[0]
	if record.State != StateUnknown {
		t.Errorf("State = %q, want %q", record.State, StateUnknown)
	}
	if record.Version != "" {
		t.Errorf("Version = %q, want empty (not executed)", record.Version)
	}
	if record.Path == "" {
		t.Error("Path is empty, want the resolved binary")
	}
}

func TestNewConfigProbes(t *testing.T) {
	probes := NewConfigProbes(nil)
	if len(probes) != 0 {
		t.Errorf("NewConfigProbes(nil) = %d probes, want 0", len(probes))
	}
}

func TestBinaryProbeSanitizesVersionOutput(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "escape-tool", "#!/bin/sh\nprintf 'escape-tool \x1b[31m1.0\x1b[0m\n'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	registry := NewRegistry([]Probe{
		NewBinaryProbe("escape", "escape", "escape-tool", true, nil),
	}, time.Second)

	records := registry.Refresh(context.Background())
	version := records[0].Version
	if strings.ContainsRune(version, '\x1b') {
		t.Errorf("Version = %q, want terminal escape characters removed", version)
	}
	if !strings.Contains(version, "escape-tool") {
		t.Errorf("Version = %q, want the printable tool name preserved", version)
	}
}

func TestBinaryProbeRejectsRelativePathEntry(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "relative-tool", "#!/bin/sh\necho 'relative-tool 1.0'\n")
	t.Chdir(dir)
	t.Setenv("PATH", ".")

	registry := NewRegistry([]Probe{
		NewBinaryProbe("relative", "relative", "relative-tool", true, nil),
	}, time.Second)

	records := registry.Refresh(context.Background())
	record := records[0]
	if record.State != StateUnavailable {
		t.Errorf("State = %q, want %q", record.State, StateUnavailable)
	}
	if !strings.Contains(record.Reason, "relative PATH") {
		t.Errorf("Reason = %q, want a relative PATH rejection", record.Reason)
	}
}

func writeExecutable(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
