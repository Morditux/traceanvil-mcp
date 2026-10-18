package dap

import (
	"testing"
)

func TestStateProperties(t *testing.T) {
	states := []State{
		StateCreated, StateStarting, StateInitialized, StateLaunching,
		StateRunning, StateStopped, StateTerminated, StateCancelled,
		StateTimedOut, StateFailed, StateClosed,
	}
	for _, state := range states {
		if !state.Valid() {
			t.Errorf("%q is not valid", state)
		}
	}
	if State("bogus").Valid() {
		t.Error("bogus state is valid")
	}
	for _, state := range []State{StateStarting, StateInitialized, StateLaunching, StateRunning, StateStopped} {
		if !state.Active() {
			t.Errorf("%q is not active", state)
		}
		if state.Terminal() {
			t.Errorf("%q is terminal", state)
		}
	}
	for _, state := range []State{StateTerminated, StateCancelled, StateTimedOut, StateFailed, StateClosed} {
		if !state.Terminal() {
			t.Errorf("%q is not terminal", state)
		}
		if state.Active() {
			t.Errorf("%q is active", state)
		}
	}
}

func TestStateMachineTransitions(t *testing.T) {
	tests := []struct {
		from State
		to   State
		want bool
	}{
		{StateCreated, StateStarting, true},
		{StateCreated, StateInitialized, false},
		{StateStarting, StateInitialized, true},
		{StateStarting, StateFailed, true},
		{StateInitialized, StateLaunching, true},
		{StateInitialized, StateStopped, true},
		{StateInitialized, StateRunning, false},
		{StateLaunching, StateRunning, true},
		{StateLaunching, StateStopped, true},
		{StateRunning, StateStopped, true},
		{StateRunning, StateRunning, false},
		{StateStopped, StateRunning, true},
		{StateStopped, StateTerminated, true},
		{StateTerminated, StateClosed, true},
		{StateTerminated, StateRunning, false},
		{StateFailed, StateClosed, true},
		{StateFailed, StateRunning, false},
		{StateClosed, StateStarting, false},
	}

	for _, test := range tests {
		machine := &stateMachine{state: test.from}
		if got := machine.transition(test.to); got != test.want {
			t.Errorf("%q -> %q = %v, want %v", test.from, test.to, got, test.want)
		}
		if test.want {
			if got := machine.State(); got != test.to {
				t.Errorf("state after %q -> %q = %q", test.from, test.to, got)
			}
		} else if got := machine.State(); got != test.from {
			t.Errorf("state changed after a rejected transition: %q -> %q", test.from, got)
		}
	}
}

func TestStateMachineForce(t *testing.T) {
	machine := newStateMachine()
	if machine.State() != StateCreated {
		t.Fatalf("initial state = %q", machine.State())
	}
	machine.force(StateFailed)
	if machine.State() != StateFailed {
		t.Errorf("forced state = %q", machine.State())
	}
	machine.force(StateClosed)
	if machine.State() != StateClosed {
		t.Errorf("forced state = %q", machine.State())
	}
}
