package dap

import "sync"

// State is the lifecycle of one DAP session. It is intentionally independent of
// the debuggee's business state: "stopped" means the debugger reported a stop,
// not that an analysis succeeded.
type State string

const (
	// StateCreated is a client that has not started initialize.
	StateCreated State = "created"
	// StateStarting means the initialize request is in flight.
	StateStarting State = "starting"
	// StateInitialized means the initialize response was accepted. The DAP
	// "initialized" event is delivered separately; a backend may wait for it
	// before sending launch/attach or configurationDone.
	StateInitialized State = "initialized"
	// StateLaunching means launch or attach was sent.
	StateLaunching State = "launching"
	// StateRunning means the debuggee is running.
	StateRunning State = "running"
	// StateStopped means the debugger reported a stopped event.
	StateStopped State = "stopped"
	// StateTerminated means the debuggee ended (exited/terminated event).
	StateTerminated State = "terminated"
	// StateCancelled means the session was cancelled by the server.
	StateCancelled State = "cancelled"
	// StateTimedOut means the session exceeded its overall deadline.
	StateTimedOut State = "timed_out"
	// StateFailed means a transport or protocol failure ended the session.
	StateFailed State = "failed"
	// StateClosed means the transport was closed. It is the final state.
	StateClosed State = "closed"
)

// Valid reports whether s is a known state.
func (s State) Valid() bool {
	switch s {
	case StateCreated, StateStarting, StateInitialized, StateLaunching,
		StateRunning, StateStopped, StateTerminated, StateCancelled,
		StateTimedOut, StateFailed, StateClosed:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further session progress is expected. Closed is
// terminal; failed and cancelled can still be cleaned up into closed.
func (s State) Terminal() bool {
	switch s {
	case StateTerminated, StateCancelled, StateTimedOut, StateFailed, StateClosed:
		return true
	default:
		return false
	}
}

// Active reports whether the session can still be cancelled or timed out.
func (s State) Active() bool {
	switch s {
	case StateStarting, StateInitialized, StateLaunching, StateRunning, StateStopped:
		return true
	default:
		return false
	}
}

// stateTransitions is the allowed transition table. Event-driven transitions
// use tryTransition and ignore impossible events; API transitions return
// ErrInvalidState.
var stateTransitions = map[State]map[State]bool{
	StateCreated: {
		StateStarting: true,
		StateFailed:   true,
		StateClosed:   true,
	},
	StateStarting: {
		StateInitialized: true,
		StateCancelled:   true,
		StateTimedOut:    true,
		StateTerminated:  true,
		StateFailed:      true,
		StateClosed:      true,
	},
	StateInitialized: {
		StateLaunching:  true,
		StateStopped:    true,
		StateTerminated: true,
		StateCancelled:  true,
		StateTimedOut:   true,
		StateFailed:     true,
		StateClosed:     true,
	},
	StateLaunching: {
		StateRunning:    true,
		StateStopped:    true,
		StateTerminated: true,
		StateCancelled:  true,
		StateTimedOut:   true,
		StateFailed:     true,
		StateClosed:     true,
	},
	StateRunning: {
		StateStopped:    true,
		StateTerminated: true,
		StateCancelled:  true,
		StateTimedOut:   true,
		StateFailed:     true,
		StateClosed:     true,
	},
	StateStopped: {
		StateRunning:    true,
		StateTerminated: true,
		StateCancelled:  true,
		StateTimedOut:   true,
		StateFailed:     true,
		StateClosed:     true,
	},
	StateTerminated: {StateClosed: true},
	StateCancelled:  {StateClosed: true},
	StateTimedOut:   {StateClosed: true},
	StateFailed:     {StateClosed: true},
	StateClosed:     {},
}

// stateMachine serializes state reads and transitions.
type stateMachine struct {
	mu    sync.Mutex
	state State
}

func newStateMachine() *stateMachine {
	return &stateMachine{state: StateCreated}
}

func (m *stateMachine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// transition moves to next only when the table allows it.
func (m *stateMachine) transition(next State) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !stateTransitions[m.state][next] {
		return false
	}
	m.state = next
	return true
}

// tryTransition is the event-driven form: an impossible event is ignored
// instead of failing the transport.
func (m *stateMachine) tryTransition(next State) bool {
	return m.transition(next)
}

// force sets a terminal state regardless of the table. It is used for shutdown
// and for states that supersede the current session (failed/closed).
func (m *stateMachine) force(next State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = next
}
