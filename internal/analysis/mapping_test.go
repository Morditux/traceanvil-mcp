package analysis

import (
	"strings"
	"testing"

	"github.com/Morditux/traceanvil-mcp/internal/jobs"
	"github.com/Morditux/traceanvil-mcp/internal/valgrind"
)

func TestPageLimitClamping(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxResponseItems = 10
		opts.MaxPageItems = 50
	})

	if service.maxPageItems != 10 {
		t.Fatalf("maxPageItems = %d, want 10 (clamped by max_response_items)", service.maxPageItems)
	}
	tests := []struct {
		requested int
		want      int
		wantErr   bool
	}{
		{requested: 0, want: 10},
		{requested: 1, want: 1},
		{requested: 10, want: 10},
		{requested: 50, want: 10},
		{requested: -1, wantErr: true},
	}
	for _, test := range tests {
		got, err := service.pageLimit(test.requested)
		if test.wantErr {
			mustCode(t, err, CodeInvalidArgument)
			continue
		}
		if err != nil {
			t.Fatalf("pageLimit(%d) error = %v", test.requested, err)
		}
		if got != test.want {
			t.Errorf("pageLimit(%d) = %d, want %d", test.requested, got, test.want)
		}
	}
}

func TestDefaultPageLimit(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxResponseItems = 100
		opts.MaxPageItems = 500
	})
	got, err := service.pageLimit(0)
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultPageItems {
		t.Errorf("pageLimit(0) = %d, want %d", got, DefaultPageItems)
	}
}

func TestLimitUTF8(t *testing.T) {
	// The euro sign is three bytes; truncating in the middle must back off to a
	// valid prefix.
	value := strings.Repeat("a", 4) + "€" + "b"
	limited := limitUTF8(value, 5)
	if limited != strings.Repeat("a", 4) {
		t.Errorf("limitUTF8() = %q, want four ASCII bytes", limited)
	}
	if limitUTF8("short", 10) != "short" {
		t.Error("limitUTF8() changed a short string")
	}
}

func TestStateAndPhaseMappings(t *testing.T) {
	states := map[jobs.State]State{
		jobs.StateQueued:    StateQueued,
		jobs.StateRunning:   StateRunning,
		jobs.StateCompleted: StateCompleted,
		jobs.StateCancelled: StateCancelled,
		jobs.StateFailed:    StateFailed,
		jobs.StateTimedOut:  StateTimedOut,
		jobs.State("other"): StateUnknown,
	}
	for input, want := range states {
		if got := stateFromJobs(input); got != want {
			t.Errorf("stateFromJobs(%q) = %q, want %q", input, got, want)
		}
	}

	phases := map[valgrind.Phase]Phase{
		valgrind.PhaseQueued:    PhaseQueued,
		valgrind.PhaseRunning:   PhaseRunning,
		valgrind.PhaseAnalyzing: PhaseAnalyzing,
		valgrind.PhaseCompleted: PhaseCompleted,
		valgrind.PhaseFailed:    PhaseFailed,
		valgrind.PhaseCancelled: PhaseCancelled,
		valgrind.PhaseTimedOut:  PhaseTimedOut,
		valgrind.Phase("other"): PhaseUnknown,
	}
	for input, want := range phases {
		if got := phaseFromValgrind(input); got != want {
			t.Errorf("phaseFromValgrind(%q) = %q, want %q", input, got, want)
		}
	}

	for _, state := range []State{StateQueued, StateRunning, StateCompleted, StateCancelled, StateFailed, StateTimedOut, StateUnknown} {
		_ = phaseFromState(state)
	}
}
