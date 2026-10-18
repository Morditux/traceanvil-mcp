package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is one generic DAP session over a duplex byte stream. It owns the only
// reader, serializes writes, correlates responses by request sequence, delivers
// events on a single bounded channel and terminates pending requests on EOF or
// Close.
type Client struct {
	conn   io.ReadWriteCloser
	reader *bufio.Reader
	opts   Options

	seq   atomic.Int64
	state *stateMachine

	pendingMu sync.Mutex
	pending   map[int]chan *Response

	late atomic.Int64

	out    chan *outbound
	events chan Event

	done         chan struct{}
	shutdownOnce sync.Once
	errMu        sync.Mutex
	err          error

	baseCtx context.Context
	cancel  context.CancelFunc

	capsMu  sync.RWMutex
	caps    Capabilities
	capsRaw map[string]bool

	reverseSem chan struct{}
	wg         sync.WaitGroup
}

type outbound struct {
	ctx     context.Context
	payload []byte
	done    chan error
}

// New starts a client and its reader/writer goroutines. The caller owns the
// transport and must call Close to release the goroutines.
func New(conn io.ReadWriteCloser, opts Options) (*Client, error) {
	if conn == nil {
		return nil, protocolError("nil transport", ErrInvalidArgument)
	}
	opts = opts.withDefaults()

	baseCtx, cancel := context.WithCancel(context.Background())
	client := &Client{
		conn:       conn,
		reader:     bufio.NewReaderSize(conn, frameBufferSize),
		opts:       opts,
		state:      newStateMachine(),
		pending:    make(map[int]chan *Response),
		out:        make(chan *outbound, opts.WriteQueue),
		events:     make(chan Event, opts.EventBuffer),
		done:       make(chan struct{}),
		baseCtx:    baseCtx,
		cancel:     cancel,
		capsRaw:    make(map[string]bool),
		reverseSem: make(chan struct{}, opts.ReverseRequestConcurrency),
	}

	client.wg.Add(2)
	go client.readLoop()
	go client.writeLoop()
	return client, nil
}

// Request sends one DAP request and waits for its correlated response. A failed
// DAP response (success=false) is returned as *ResponseError; the session
// remains usable. A context cancellation removes the pending future; a response
// arriving later is counted as late and ignored.
func (c *Client) Request(ctx context.Context, command string, args any) (*Response, error) {
	future, err := c.RequestAsync(ctx, command, args)
	if err != nil {
		return nil, err
	}
	return future.Wait(ctx)
}

// Future is one in-flight DAP request created by RequestAsync.
//
// RequestAsync writes the request frame before returning, so callers can use it
// to order a request that blocks on another protocol step: a debug adapter may
// not answer launch until configurationDone has been received. The first Wait
// controls cancellation and caches its result; later calls return that same
// result or error even if their context is shorter or already expired.
type Future struct {
	client   *Client
	seq      int
	command  string
	response chan *Response

	once sync.Once
	mu   sync.Mutex
	res  *Response
	err  error
}

// Seq returns the request sequence assigned to the future.
func (f *Future) Seq() int { return f.seq }

// Command returns the DAP command name.
func (f *Future) Command() string { return f.command }

// Wait blocks until the response arrives, the context expires or the client
// stops. A failed DAP response is returned as *ResponseError.
func (f *Future) Wait(ctx context.Context) (*Response, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	f.once.Do(func() {
		response, err := f.wait(ctx)
		f.mu.Lock()
		f.res, f.err = response, err
		f.mu.Unlock()
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res, f.err
}

func (f *Future) wait(ctx context.Context) (*Response, error) {
	// Prefer an already-delivered response over a concurrently expiring
	// context. The read loop may have correlated the response just before the
	// caller's deadline.
	select {
	case response := <-f.response:
		return responseResult(response)
	default:
	}

	select {
	case response := <-f.response:
		return responseResult(response)
	case <-ctx.Done():
		// The response may have been correlated between the two selects.
		select {
		case response := <-f.response:
			return responseResult(response)
		default:
		}
		f.client.removePending(f.seq)
		return nil, ctx.Err()
	case <-f.client.done:
		return nil, f.client.closedError()
	}
}

func responseResult(response *Response) (*Response, error) {
	if !response.Success {
		return nil, newResponseError(response)
	}
	return response, nil
}

// RequestAsync sends one DAP request and returns a future for its response. It
// returns after the request frame has been written, without waiting for the
// adapter. Callers must eventually call Wait (or close the client) so the
// pending entry does not linger until shutdown.
func (c *Client) RequestAsync(ctx context.Context, command string, args any) (*Future, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if command == "" {
		return nil, fmt.Errorf("%w: command must not be empty", ErrInvalidArgument)
	}
	if err := c.readyFor(command); err != nil {
		return nil, err
	}

	select {
	case <-c.done:
		return nil, c.closedError()
	default:
	}

	seq := int(c.seq.Add(1))
	responseCh := make(chan *Response, 1)

	c.pendingMu.Lock()
	if len(c.pending) >= c.opts.MaxPendingRequests {
		c.pendingMu.Unlock()
		return nil, ErrPendingLimit
	}
	c.pending[seq] = responseCh
	c.pendingMu.Unlock()

	request := wireRequest{Seq: seq, Type: "request", Command: command, Arguments: args}
	if err := c.send(ctx, request); err != nil {
		c.removePending(seq)
		return nil, err
	}

	return &Future{
		client:   c,
		seq:      seq,
		command:  command,
		response: responseCh,
	}, nil
}

// Initialize performs capability negotiation. On success the client state is
// initialized. The DAP "initialized" event is delivered separately; callers that
// need it should wait for it with WaitForEvent before sending launch/attach.
func (c *Client) Initialize(ctx context.Context, args InitializeRequestArguments) (*Capabilities, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	args.AdapterID = strings.TrimSpace(args.AdapterID)
	if args.AdapterID == "" {
		return nil, fmt.Errorf("%w: adapterID is required", ErrInvalidArgument)
	}
	if args.ClientID == "" {
		args.ClientID = c.opts.ClientID
	}
	if args.ClientName == "" {
		args.ClientName = c.opts.ClientName
	}
	if args.PathFormat == "" {
		args.PathFormat = "path"
	}
	trueValue := true
	if args.LinesStartAt1 == nil {
		args.LinesStartAt1 = &trueValue
	}
	if args.ColumnsStartAt1 == nil {
		args.ColumnsStartAt1 = &trueValue
	}

	if !c.state.transition(StateStarting) {
		return nil, ErrInvalidState
	}
	response, err := c.Request(ctx, "initialize", args)
	if err != nil {
		c.state.transition(StateFailed)
		return nil, err
	}
	capabilities, raw, err := decodeCapabilities(response.Body)
	if err != nil {
		c.state.transition(StateFailed)
		return nil, err
	}
	if !c.state.transition(StateInitialized) {
		return nil, ErrInvalidState
	}
	c.capsMu.Lock()
	c.caps = capabilities
	c.capsRaw = raw
	c.capsMu.Unlock()

	result := capabilities
	return &result, nil
}

// Capabilities returns the last negotiated capabilities (a value copy).
func (c *Client) Capabilities() Capabilities {
	c.capsMu.RLock()
	defer c.capsMu.RUnlock()
	return c.caps
}

// Supports reports a capability by its exact DAP name, including capabilities
// unknown to this build. Missing or non-boolean values are false.
func (c *Client) Supports(name string) bool {
	c.capsMu.RLock()
	defer c.capsMu.RUnlock()
	return c.capsRaw[name]
}

// Events returns the single event stream. The consumer must drain it; a full
// buffer applies backpressure to the read loop. The channel is closed on
// shutdown.
func (c *Client) Events() <-chan Event { return c.events }

// WaitForEvent consumes events until name arrives. Events with other names are
// discarded, so this helper is intended for the single event consumer.
func (c *Client) WaitForEvent(ctx context.Context, name string) (Event, error) {
	if ctx == nil {
		return Event{}, ErrNilContext
	}
	if name == "" {
		return Event{}, fmt.Errorf("%w: event name must not be empty", ErrInvalidArgument)
	}
	for {
		select {
		case event, ok := <-c.events:
			if !ok {
				return Event{}, c.closedError()
			}
			if event.Name == name {
				return event, nil
			}
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-c.done:
			return Event{}, c.closedError()
		}
	}
}

// State returns the current synchronized session state.
func (c *Client) State() State { return c.state.State() }

// BeginLaunch moves an initialized session into the launching state. Backends
// call it immediately before sending launch or attach.
func (c *Client) BeginLaunch() error {
	if c.State() != StateInitialized {
		return ErrInvalidState
	}
	if !c.state.transition(StateLaunching) {
		return ErrInvalidState
	}
	return nil
}

// ConfirmRunning marks a successful launch as running. A stop-on-entry event
// may already have moved the session to stopped; that is not an error.
func (c *Client) ConfirmRunning() error {
	switch c.State() {
	case StateRunning, StateStopped:
		return nil
	case StateLaunching:
		if !c.state.transition(StateRunning) {
			return ErrInvalidState
		}
		return nil
	default:
		return ErrInvalidState
	}
}

// MarkCancelled moves an active session to cancelled. It is idempotent.
func (c *Client) MarkCancelled() error {
	if c.State() == StateCancelled {
		return nil
	}
	if !c.State().Active() || !c.state.transition(StateCancelled) {
		return ErrInvalidState
	}
	return nil
}

// MarkTimedOut moves an active session to timed_out. It is idempotent.
func (c *Client) MarkTimedOut() error {
	if c.State() == StateTimedOut {
		return nil
	}
	if !c.State().Active() || !c.state.transition(StateTimedOut) {
		return ErrInvalidState
	}
	return nil
}

// LateResponses counts responses that arrived after their request future was
// removed (timeout, cancellation or an unknown request_seq).
func (c *Client) LateResponses() int64 { return c.late.Load() }

// Err returns the shutdown cause, or nil while the client is open.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

// Done is closed when the client stops, either from the adapter disconnecting,
// a protocol failure or Close.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close stops the client, fails pending requests and waits for the reader and
// writer to exit. It is idempotent. It requires the transport's Close to unblock
// pending reads and writes.
func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	_ = c.conn.Close()
	c.state.force(StateClosed)
	c.wg.Wait()
	return nil
}

func (c *Client) readyFor(command string) error {
	state := c.State()
	if command == "initialize" {
		if state == StateCreated || state == StateStarting {
			return nil
		}
		return ErrInvalidState
	}
	switch state {
	case StateInitialized, StateLaunching, StateRunning, StateStopped, StateTerminated:
		return nil
	case StateCreated, StateStarting:
		return ErrNotInitialized
	case StateFailed:
		if err := c.Err(); err != nil {
			return err
		}
		return ErrInvalidState
	case StateCancelled, StateTimedOut:
		return ErrInvalidState
	case StateClosed:
		return c.closedError()
	default:
		return ErrInvalidState
	}
}

func (c *Client) closedError() error {
	if err := c.Err(); err != nil {
		return err
	}
	return ErrClosed
}

func (c *Client) removePending(seq int) {
	c.pendingMu.Lock()
	delete(c.pending, seq)
	c.pendingMu.Unlock()
}

func (c *Client) send(ctx context.Context, message any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return protocolError("encode DAP message", err)
	}
	if int64(len(payload)) > c.opts.MaxFrameBytes {
		return protocolError("DAP message exceeds frame limit", ErrFrameTooLarge)
	}

	out := &outbound{ctx: ctx, payload: payload, done: make(chan error, 1)}
	select {
	case <-c.done:
		return c.closedError()
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case c.out <- out:
	case <-c.done:
		return c.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-out.done:
		return err
	case <-c.done:
		return c.closedError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	defer close(c.events)

	for {
		message, err := readMessage(c.reader, c.opts.MaxHeaderBytes, c.opts.MaxFrameBytes)
		if err != nil {
			cause := err
			var protocolErr *ProtocolError
			if !errors.As(err, &protocolErr) {
				if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
					cause = ErrEOF
				}
			}
			c.shutdown(cause)
			_ = c.conn.Close()
			return
		}

		select {
		case <-c.done:
			return
		default:
		}

		switch message.Type {
		case "response":
			c.handleResponse(message)
		case "event":
			c.handleEvent(message)
		case "request":
			c.handleReverseRequest(message)
		}
	}
}

func (c *Client) writeLoop() {
	defer c.wg.Done()

	for {
		select {
		case <-c.done:
			return
		case out := <-c.out:
			select {
			case <-c.done:
				out.done <- ErrClosed
				return
			default:
			}
			if err := out.ctx.Err(); err != nil {
				out.done <- err
				continue
			}
			c.setWriteDeadline()
			err := writeFrame(c.conn, out.payload)
			c.clearWriteDeadline()
			if err != nil {
				err = protocolError("write frame", err)
			}
			out.done <- err
			if err != nil {
				c.shutdown(err)
				_ = c.conn.Close()
				return
			}
		}
	}
}

func (c *Client) setWriteDeadline() {
	if c.opts.WriteTimeout <= 0 {
		return
	}
	if conn, ok := c.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = conn.SetWriteDeadline(time.Now().Add(c.opts.WriteTimeout))
	}
}

func (c *Client) clearWriteDeadline() {
	if c.opts.WriteTimeout <= 0 {
		return
	}
	if conn, ok := c.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = conn.SetWriteDeadline(time.Time{})
	}
}

func (c *Client) handleResponse(message wireMessage) {
	response := &Response{
		Seq:        message.Seq,
		RequestSeq: message.RequestSeq,
		Command:    message.Command,
		Success:    message.Success,
		Message:    message.Message,
		Body:       message.Body,
	}

	c.pendingMu.Lock()
	responseCh, ok := c.pending[response.RequestSeq]
	if ok {
		delete(c.pending, response.RequestSeq)
	}
	c.pendingMu.Unlock()
	if !ok {
		c.late.Add(1)
		return
	}
	select {
	case responseCh <- response:
	default:
		c.late.Add(1)
	}
}

func (c *Client) handleEvent(message wireMessage) {
	c.applyEventState(message.Event)
	event := Event{
		Seq:        message.Seq,
		Name:       message.Event,
		Body:       message.Body,
		ReceivedAt: time.Now().UTC(),
	}
	select {
	case c.events <- event:
	case <-c.done:
	}
}

func (c *Client) applyEventState(name string) {
	switch name {
	case "stopped":
		c.state.tryTransition(StateStopped)
	case "continued":
		c.state.tryTransition(StateRunning)
	case "exited", "terminated":
		c.state.tryTransition(StateTerminated)
	}
}

func (c *Client) handleReverseRequest(message wireMessage) {
	select {
	case c.reverseSem <- struct{}{}:
	case <-c.done:
		return
	}

	go func() {
		defer func() { <-c.reverseSem }()
		request := ReverseRequest{Seq: message.Seq, Command: message.Command, Arguments: message.Arguments}
		body, err := c.runReverseRequest(request)
		response := wireResponse{
			Seq:        int(c.seq.Add(1)),
			Type:       "response",
			RequestSeq: message.Seq,
			Command:    message.Command,
			Success:    err == nil,
			Body:       body,
		}
		if err != nil {
			response.Message = err.Error()
		}
		_ = c.send(c.baseCtx, response)
	}()
}

func (c *Client) runReverseRequest(request ReverseRequest) (body any, err error) {
	handler := c.opts.ReverseRequestHandler
	if handler == nil {
		return nil, fmt.Errorf("unsupported reverse request %q", request.Command)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			body = nil
			err = fmt.Errorf("reverse request handler panicked: %v", recovered)
		}
	}()
	return handler(c.baseCtx, request)
}

func (c *Client) shutdown(cause error) {
	if cause == nil {
		cause = ErrClosed
	}
	c.shutdownOnce.Do(func() {
		c.errMu.Lock()
		c.err = cause
		c.errMu.Unlock()

		c.cancel()

		state := c.State()
		switch {
		case errors.Is(cause, ErrClosed):
			c.state.force(StateClosed)
		case state == StateTerminated || state == StateCancelled || state == StateTimedOut || state == StateClosed:
			// Keep the session outcome; Close will move it to closed.
		default:
			c.state.force(StateFailed)
		}

		c.pendingMu.Lock()
		c.pending = make(map[int]chan *Response)
		c.pendingMu.Unlock()

		close(c.done)
	})
}
