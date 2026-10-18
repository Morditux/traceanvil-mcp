package debugger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Session is one live or retained debugger session. The GDB/LLDB implementation
// owns a DAP client and a debugger process; tests can provide a fake.
type Session interface {
	ID() string
	Status() Status
	WorkingDir() string

	SetBreakpoints(context.Context, BreakpointsRequest) (BreakpointsResult, error)
	Continue(context.Context, ContinueRequest) (ActionResult, error)
	Step(context.Context, StepRequest) (ActionResult, error)
	Pause(context.Context, PauseRequest) (ActionResult, error)
	Threads(context.Context, ThreadsRequest) (ThreadsResult, error)
	StackTrace(context.Context, StackRequest) (StackResult, error)
	Scopes(context.Context, ScopesRequest) (ScopesResult, error)
	Variables(context.Context, VariablesRequest) (VariablesResult, error)
	Stop(context.Context, StopRequest) (StopResult, error)

	Done() <-chan struct{}
	Close()
}

// ManagerOptions bounds session count and retention. Zero values select the
// documented defaults.
type ManagerOptions struct {
	MaxSessions   int
	IdleTimeout   time.Duration
	RetainTimeout time.Duration
	ReapInterval  time.Duration
	Logger        *slog.Logger
}

// Default session retention bounds.
const (
	DefaultIdleTimeout   = 15 * time.Minute
	DefaultRetainTimeout = 5 * time.Minute
	DefaultReapInterval  = 30 * time.Second
	defaultRetainedCount = 32
)

// LimitError reports that the concurrent live-session quota is exhausted.
type LimitError struct {
	Max int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("debugger: session limit reached (%d live sessions)", e.Max)
}

// ErrClosed is returned when a manager operation follows Close.
var ErrClosed = errors.New("debugger: manager is closed")

// SessionIDPattern is the only accepted session id form.
const SessionIDPattern = `^dbg_[0-9a-f]{32}$`

type sessionRecord struct {
	session  Session
	lastUsed time.Time
}

// Manager owns session identifiers, quotas, retention and shutdown. It never
// interprets debugger actions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*sessionRecord
	starting int
	closed   bool
	opts     ManagerOptions
	logger   *slog.Logger

	base     context.Context
	stopBase context.CancelFunc
	startWG  sync.WaitGroup
	reaperWG sync.WaitGroup
}

// NewManager builds a manager and starts its retention reaper.
func NewManager(opts ManagerOptions) *Manager {
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 1
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.RetainTimeout <= 0 {
		opts.RetainTimeout = DefaultRetainTimeout
	}
	if opts.ReapInterval <= 0 {
		opts.ReapInterval = DefaultReapInterval
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	base, stop := context.WithCancel(context.Background())
	manager := &Manager{
		sessions: make(map[string]*sessionRecord),
		opts:     opts,
		logger:   opts.Logger,
		base:     base,
		stopBase: stop,
	}
	manager.reaperWG.Add(1)
	go manager.reaper()
	return manager
}

// Start admits one session launch under the live-session quota, invokes start
// and registers the result. The start context is canceled if the manager
// closes, but never by the caller's request cancellation alone after the
// session has started: an established debuggee outlives its start request.
func (m *Manager) Start(ctx context.Context, start func(context.Context, string) (Session, error)) (Session, error) {
	if ctx == nil {
		return nil, Errorf(CodeInvalidArgument, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if start == nil {
		return nil, Errorf(CodeInternal, "nil session start function")
	}

	id, err := newSessionID()
	if err != nil {
		return nil, Wrap(CodeInternal, err, "generate session id")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if m.liveLocked()+m.starting >= m.opts.MaxSessions {
		m.mu.Unlock()
		return nil, &LimitError{Max: m.opts.MaxSessions}
	}
	m.starting++
	m.startWG.Add(1)
	m.mu.Unlock()
	defer m.startWG.Done()

	startCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Closing the manager aborts an in-progress launch instead of waiting for
	// its startup timeout.
	go func() {
		select {
		case <-m.base.Done():
			cancel()
		case <-startCtx.Done():
		}
	}()

	session, startErr := start(startCtx, id)
	cancel()

	m.mu.Lock()
	m.starting--
	if startErr != nil {
		m.mu.Unlock()
		if session != nil {
			session.Close()
		}
		return nil, startErr
	}
	if session == nil {
		m.mu.Unlock()
		return nil, Errorf(CodeInternal, "session start returned nil")
	}
	if session.ID() != id {
		m.mu.Unlock()
		session.Close()
		return nil, Errorf(CodeInternal, "session start returned id %q, want %q", session.ID(), id)
	}
	if m.closed {
		m.mu.Unlock()
		session.Close()
		return nil, ErrClosed
	}
	m.sessions[id] = &sessionRecord{session: session, lastUsed: time.Now()}
	m.mu.Unlock()
	return session, nil
}

// Get returns a session and refreshes its activity timestamp. The returned
// session may be a retained terminal record; callers inspect Status.
func (m *Manager) Get(sessionID string) (Session, error) {
	if sessionID == "" {
		return nil, Errorf(CodeInvalidArgument, "session_id must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	record, ok := m.sessions[sessionID]
	if !ok {
		return nil, Errorf(CodeSessionNotFound, "session %q does not exist or has expired", sessionID)
	}
	record.lastUsed = time.Now()
	return record.session, nil
}

// HasSessions reports whether at least one retained session exists.
func (m *Manager) HasSessions() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions) > 0
}

// LiveCount returns the number of non-terminal sessions.
func (m *Manager) LiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.liveLocked()
}

func (m *Manager) liveLocked() int {
	count := 0
	for _, record := range m.sessions {
		if !record.session.Status().State.Terminal() {
			count++
		}
	}
	return count
}

func (m *Manager) reaper() {
	defer m.reaperWG.Done()
	ticker := time.NewTicker(m.opts.ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.base.Done():
			return
		case <-ticker.C:
			m.Reap()
		}
	}
}

// Reap closes idle sessions and expires retained terminal sessions. It is safe
// to call concurrently and is also invoked by the background reaper.
func (m *Manager) Reap() {
	now := time.Now()
	var closeList []Session

	m.mu.Lock()
	for id, record := range m.sessions {
		state := record.session.Status().State
		age := now.Sub(record.lastUsed)
		if state.Terminal() {
			if age > m.opts.RetainTimeout {
				delete(m.sessions, id)
				closeList = append(closeList, record.session)
			}
			continue
		}
		if age > m.opts.IdleTimeout {
			delete(m.sessions, id)
			closeList = append(closeList, record.session)
		}
	}

	// A flood of terminal sessions must not grow the map without bound even
	// when every session is recent. Evict the oldest terminal records first;
	// live sessions are never evicted here.
	if limit := m.opts.MaxSessions + defaultRetainedCount; len(m.sessions) > limit {
		type candidate struct {
			id   string
			used time.Time
		}
		var terminal []candidate
		for id, record := range m.sessions {
			if record.session.Status().State.Terminal() {
				terminal = append(terminal, candidate{id: id, used: record.lastUsed})
			}
		}
		for len(m.sessions) > limit && len(terminal) > 0 {
			oldest := 0
			for i := range terminal {
				if terminal[i].used.Before(terminal[oldest].used) {
					oldest = i
				}
			}
			record := m.sessions[terminal[oldest].id]
			delete(m.sessions, terminal[oldest].id)
			closeList = append(closeList, record.session)
			terminal = append(terminal[:oldest], terminal[oldest+1:]...)
		}
	}
	m.mu.Unlock()

	for _, session := range closeList {
		m.logger.Info("closing retained debug session", "session_id", session.ID())
		session.Close()
	}
}

// Close stops admission, cancels in-progress launches, closes every session and
// waits for the reaper and startup goroutine. It is idempotent.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.stopBase()
	sessions := make([]Session, 0, len(m.sessions))
	for _, record := range m.sessions {
		sessions = append(sessions, record.session)
	}
	m.sessions = make(map[string]*sessionRecord)
	m.mu.Unlock()

	m.startWG.Wait()
	m.reaperWG.Wait()

	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Add(1)
		go func(session Session) {
			defer wg.Done()
			session.Close()
		}(session)
	}
	wg.Wait()
}

func newSessionID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "dbg_" + hex.EncodeToString(data[:]), nil
}
