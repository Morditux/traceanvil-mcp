// Package daptest provides a small scripted DAP adapter for client tests. It
// deliberately implements its own Content-Length framing instead of importing
// the client framing, so tests exercise two independent decoders.
package daptest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

const (
	maxHeaderBytes = 64 << 10
	maxFrameBytes  = 4 << 20
)

// ErrClosed means the adapter transport was closed.
var ErrClosed = errors.New("daptest: adapter is closed")

// ErrProtocol means the client sent a malformed frame.
var ErrProtocol = errors.New("daptest: protocol error")

// Request is one client request received by the adapter.
type Request struct {
	Seq       int
	Command   string
	Arguments json.RawMessage
}

// DecodeArguments decodes the request arguments into v.
func (r Request) DecodeArguments(v any) error { return decodeRaw(r.Arguments, v) }

// Payload is the adapter's answer to one request. A nil payload from a Handler
// means the handler will respond later through Respond.
type Payload struct {
	Success bool
	Message string
	Body    any
}

// Handler processes one client request. It runs on the adapter read loop and
// must not block: use Respond from a goroutine for delayed answers, and do not
// call SendReverseRequest from a handler (that would wait on the same loop).
type Handler func(Request) *Payload

// Response is a response to an adapter-initiated request.
type Response struct {
	Seq        int
	RequestSeq int
	Success    bool
	Command    string
	Message    string
	Body       json.RawMessage
}

// DecodeBody decodes the response body into v.
func (r Response) DecodeBody(v any) error { return decodeRaw(r.Body, v) }

type incoming struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Command    string          `json:"command,omitempty"`
	Message    string          `json:"message,omitempty"`
	Event      string          `json:"event,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

type outgoing struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	RequestSeq int    `json:"request_seq,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	Command    string `json:"command,omitempty"`
	Message    string `json:"message,omitempty"`
	Event      string `json:"event,omitempty"`
	Arguments  any    `json:"arguments,omitempty"`
	Body       any    `json:"body,omitempty"`
}

// Adapter is a scripted DAP peer. NewPair returns the client side of an
// in-memory connection and the adapter side.
type Adapter struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu sync.Mutex

	mu       sync.Mutex
	handlers map[string]Handler
	pending  map[int]chan Response
	seq      int64

	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	err       error

	wg sync.WaitGroup
}

// NewPair returns a connected client transport and a running adapter.
func NewPair() (net.Conn, *Adapter) {
	clientConn, adapterConn := net.Pipe()
	adapter := &Adapter{
		conn:     adapterConn,
		reader:   bufio.NewReaderSize(adapterConn, 4096),
		handlers: make(map[string]Handler),
		pending:  make(map[int]chan Response),
		done:     make(chan struct{}),
	}
	adapter.wg.Add(1)
	go adapter.readLoop()
	return clientConn, adapter
}

// Handle registers the handler for one request command.
func (a *Adapter) Handle(command string, handler Handler) {
	a.mu.Lock()
	a.handlers[command] = handler
	a.mu.Unlock()
}

// Respond writes a response for req. It is safe to call from any goroutine.
func (a *Adapter) Respond(req Request, payload Payload) error {
	success := payload.Success
	return a.write(outgoing{
		Seq:        a.nextSeq(),
		Type:       "response",
		RequestSeq: req.Seq,
		Success:    &success,
		Command:    req.Command,
		Message:    payload.Message,
		Body:       payload.Body,
	})
}

// SendEvent writes one adapter event.
func (a *Adapter) SendEvent(name string, body any) error {
	return a.write(outgoing{
		Seq:   a.nextSeq(),
		Type:  "event",
		Event: name,
		Body:  body,
	})
}

// SendReverseRequest sends an adapter-initiated request and waits for the client
// response.
func (a *Adapter) SendReverseRequest(ctx context.Context, command string, args any) (Response, error) {
	if ctx == nil {
		return Response{}, errors.New("daptest: nil context")
	}
	seq := a.nextSeq()
	ch := make(chan Response, 1)
	a.mu.Lock()
	a.pending[seq] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, seq)
		a.mu.Unlock()
	}()

	if err := a.write(outgoing{Seq: seq, Type: "request", Command: command, Arguments: args}); err != nil {
		return Response{}, err
	}
	select {
	case response := <-ch:
		return response, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-a.done:
		return Response{}, a.adapterError()
	}
}

// Close closes the adapter transport and waits for its read loop. It is
// idempotent.
func (a *Adapter) Close() error {
	a.stop(ErrClosed)
	a.wg.Wait()
	return nil
}

// Err returns the adapter shutdown cause, if any.
func (a *Adapter) Err() error {
	a.errMu.Lock()
	defer a.errMu.Unlock()
	return a.err
}

func (a *Adapter) readLoop() {
	defer a.wg.Done()
	for {
		message, err := readIncoming(a.reader)
		if err != nil {
			a.stop(err)
			return
		}
		switch message.Type {
		case "request":
			a.handleRequest(Request{
				Seq:       message.Seq,
				Command:   message.Command,
				Arguments: message.Arguments,
			})
		case "response":
			a.handleResponse(message)
		default:
			a.stop(fmt.Errorf("%w: unexpected message type %q", ErrProtocol, message.Type))
			return
		}
	}
}

func (a *Adapter) handleRequest(req Request) {
	a.mu.Lock()
	handler := a.handlers[req.Command]
	a.mu.Unlock()
	if handler == nil {
		_ = a.Respond(req, Payload{Success: false, Message: "unsupported command: " + req.Command})
		return
	}
	payload := safeHandle(handler, req)
	if payload != nil {
		_ = a.Respond(req, *payload)
	}
}

func safeHandle(handler Handler, req Request) (payload *Payload) {
	defer func() {
		if recovered := recover(); recovered != nil {
			payload = &Payload{Success: false, Message: fmt.Sprintf("handler panic: %v", recovered)}
		}
	}()
	return handler(req)
}

func (a *Adapter) handleResponse(message incoming) {
	a.mu.Lock()
	ch := a.pending[message.RequestSeq]
	if ch != nil {
		delete(a.pending, message.RequestSeq)
	}
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- Response{
		Seq:        message.Seq,
		RequestSeq: message.RequestSeq,
		Success:    message.Success,
		Command:    message.Command,
		Message:    message.Message,
		Body:       message.Body,
	}:
	default:
	}
}

func (a *Adapter) write(message outgoing) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("daptest: encode: %w", err)
	}
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("daptest: frame exceeds %d bytes", maxFrameBytes)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))

	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := writeAll(a.conn, []byte(header)); err != nil {
		return fmt.Errorf("daptest: write header: %w", err)
	}
	if err := writeAll(a.conn, payload); err != nil {
		return fmt.Errorf("daptest: write body: %w", err)
	}
	return nil
}

func (a *Adapter) nextSeq() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	return int(a.seq)
}

func (a *Adapter) stop(cause error) {
	if cause == nil {
		cause = ErrClosed
	}
	a.closeOnce.Do(func() {
		if !errors.Is(cause, ErrClosed) {
			a.errMu.Lock()
			if a.err == nil {
				a.err = cause
			}
			a.errMu.Unlock()
		}
		close(a.done)
		_ = a.conn.Close()
	})
}

func (a *Adapter) adapterError() error {
	if err := a.Err(); err != nil {
		return err
	}
	return ErrClosed
}

func readIncoming(reader *bufio.Reader) (incoming, error) {
	var (
		headerBytes   int64
		contentLength int64 = -1
	)
	for {
		remaining := int64(maxHeaderBytes) - headerBytes
		if remaining <= 0 {
			return incoming{}, fmt.Errorf("%w: header too large", ErrProtocol)
		}
		line, err := readLine(reader, remaining)
		if err != nil {
			return incoming{}, err
		}
		headerBytes += int64(len(line)) + 2
		if headerBytes > maxHeaderBytes {
			return incoming{}, fmt.Errorf("%w: header too large", ErrProtocol)
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return incoming{}, fmt.Errorf("%w: malformed header", ErrProtocol)
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		if contentLength >= 0 {
			return incoming{}, fmt.Errorf("%w: duplicate Content-Length", ErrProtocol)
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed <= 0 {
			return incoming{}, fmt.Errorf("%w: invalid Content-Length", ErrProtocol)
		}
		contentLength = parsed
	}
	if contentLength < 0 {
		return incoming{}, fmt.Errorf("%w: missing Content-Length", ErrProtocol)
	}
	if contentLength > maxFrameBytes {
		return incoming{}, fmt.Errorf("%w: frame too large", ErrProtocol)
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return incoming{}, fmt.Errorf("%w: truncated frame: %w", ErrProtocol, err)
	}
	var message incoming
	if err := json.Unmarshal(body, &message); err != nil {
		return incoming{}, fmt.Errorf("%w: invalid JSON: %w", ErrProtocol, err)
	}
	if message.Seq <= 0 || message.Type == "" {
		return incoming{}, fmt.Errorf("%w: invalid envelope", ErrProtocol)
	}
	return message, nil
}

func readLine(reader *bufio.Reader, limit int64) (string, error) {
	var line []byte
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil {
			return "", err
		}
		if int64(len(line)+len(fragment)) > limit {
			return "", fmt.Errorf("%w: header line too large", ErrProtocol)
		}
		line = append(line, fragment...)
		if !isPrefix {
			return string(line), nil
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func decodeRaw(raw json.RawMessage, v any) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}
