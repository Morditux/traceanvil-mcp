package dap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/dap/daptest"
)

func newClientPair(t *testing.T, opts Options) (*Client, *daptest.Adapter) {
	t.Helper()
	conn, adapter := daptest.NewPair()
	t.Cleanup(func() { _ = adapter.Close() })

	client, err := New(conn, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, adapter
}

func initializeClient(t *testing.T, client *Client, adapter *daptest.Adapter, capabilities map[string]any) *Capabilities {
	t.Helper()
	adapter.Handle("initialize", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true, Body: capabilities}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	caps, err := client.Initialize(ctx, InitializeRequestArguments{AdapterID: "mock"})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return caps
}

func requestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func TestClientInitializeCapabilitiesAndEvents(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	caps := initializeClient(t, client, adapter, map[string]any{
		"supportsConfigurationDoneRequest": true,
		"supportsConditionalBreakpoints":   false,
		"unknownCapability":                true,
	})
	if !caps.SupportsConfigurationDoneRequest {
		t.Error("supportsConfigurationDoneRequest = false")
	}
	if caps.SupportsConditionalBreakpoints {
		t.Error("supportsConditionalBreakpoints = true")
	}
	if !client.Supports("unknownCapability") {
		t.Error("unknownCapability is not reported")
	}
	if client.Supports("missingCapability") {
		t.Error("missing capability is reported as supported")
	}
	if state := client.State(); state != StateInitialized {
		t.Fatalf("state = %q, want initialized", state)
	}

	if _, err := client.Initialize(context.Background(), InitializeRequestArguments{AdapterID: "mock"}); !errors.Is(err, ErrInvalidState) {
		t.Errorf("second Initialize() error = %v, want ErrInvalidState", err)
	}

	ctx, cancel := requestContext(t)
	defer cancel()
	if err := adapter.SendEvent("initialized", nil); err != nil {
		t.Fatalf("SendEvent(initialized) error = %v", err)
	}
	event, err := client.WaitForEvent(ctx, "initialized")
	if err != nil {
		t.Fatalf("WaitForEvent(initialized) error = %v", err)
	}
	if event.Seq <= 0 {
		t.Errorf("event = %+v", event)
	}

	if err := client.BeginLaunch(); err != nil {
		t.Fatalf("BeginLaunch() error = %v", err)
	}
	if err := client.ConfirmRunning(); err != nil {
		t.Fatalf("ConfirmRunning() error = %v", err)
	}
	if state := client.State(); state != StateRunning {
		t.Fatalf("state = %q, want running", state)
	}

	ctx2, cancel2 := requestContext(t)
	defer cancel2()
	if err := adapter.SendEvent("stopped", map[string]any{"reason": "breakpoint"}); err != nil {
		t.Fatalf("SendEvent(stopped) error = %v", err)
	}
	stopped, err := client.WaitForEvent(ctx2, "stopped")
	if err != nil {
		t.Fatalf("WaitForEvent(stopped) error = %v", err)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := stopped.DecodeBody(&body); err != nil {
		t.Fatalf("DecodeBody() error = %v", err)
	}
	if body.Reason != "breakpoint" {
		t.Errorf("stopped body = %+v", body)
	}
	if state := client.State(); state != StateStopped {
		t.Errorf("state after stopped = %q", state)
	}

	if err := adapter.SendEvent("continued", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitForEvent(ctx2, "continued"); err != nil {
		t.Fatal(err)
	}
	if state := client.State(); state != StateRunning {
		t.Errorf("state after continued = %q", state)
	}

	if err := adapter.SendEvent("terminated", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitForEvent(ctx2, "terminated"); err != nil {
		t.Fatal(err)
	}
	if state := client.State(); state != StateTerminated {
		t.Errorf("state after terminated = %q", state)
	}
	if err := client.MarkCancelled(); !errors.Is(err, ErrInvalidState) {
		t.Errorf("MarkCancelled() after termination error = %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if state := client.State(); state != StateClosed {
		t.Errorf("state after Close = %q", state)
	}
	if !errors.Is(client.Err(), ErrClosed) {
		t.Errorf("Err() = %v, want ErrClosed", client.Err())
	}
}

func TestClientStateMethods(t *testing.T) {
	t.Run("before initialize", func(t *testing.T) {
		client, _ := newClientPair(t, Options{})
		if err := client.BeginLaunch(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("BeginLaunch() error = %v", err)
		}
		if err := client.ConfirmRunning(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("ConfirmRunning() error = %v", err)
		}
		if err := client.MarkCancelled(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("MarkCancelled() error = %v", err)
		}
		if err := client.MarkTimedOut(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("MarkTimedOut() error = %v", err)
		}
	})

	t.Run("timed out", func(t *testing.T) {
		client, adapter := newClientPair(t, Options{})
		initializeClient(t, client, adapter, nil)
		if err := client.BeginLaunch(); err != nil {
			t.Fatal(err)
		}
		if err := client.ConfirmRunning(); err != nil {
			t.Fatal(err)
		}
		if err := client.MarkTimedOut(); err != nil {
			t.Fatalf("MarkTimedOut() error = %v", err)
		}
		if state := client.State(); state != StateTimedOut {
			t.Errorf("state = %q, want timed_out", state)
		}
		if err := client.MarkCancelled(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("MarkCancelled() after timeout error = %v", err)
		}
	})

	t.Run("cancelled is idempotent", func(t *testing.T) {
		client, adapter := newClientPair(t, Options{})
		initializeClient(t, client, adapter, nil)
		if err := client.MarkCancelled(); err != nil {
			t.Fatalf("MarkCancelled() error = %v", err)
		}
		if err := client.MarkCancelled(); err != nil {
			t.Errorf("second MarkCancelled() error = %v", err)
		}
		if state := client.State(); state != StateCancelled {
			t.Errorf("state = %q", state)
		}
	})
}

func TestClientConcurrentRequestsOutOfOrder(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	adapter.Handle("slow", func(req daptest.Request) *daptest.Payload {
		go func() {
			time.Sleep(60 * time.Millisecond)
			_ = adapter.Respond(req, daptest.Payload{Success: true, Body: map[string]any{"value": "slow"}})
		}()
		return nil
	})
	adapter.Handle("fast", func(req daptest.Request) *daptest.Payload {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = adapter.Respond(req, daptest.Payload{Success: true, Body: map[string]any{"value": "fast"}})
		}()
		return nil
	})

	type result struct {
		command string
		value   string
		err     error
	}
	results := make(chan result, 2)
	request := func(command string) {
		ctx, cancel := requestContext(t)
		defer cancel()
		response, err := client.Request(ctx, command, nil)
		if err != nil {
			results <- result{command: command, err: err}
			return
		}
		var body struct {
			Value string `json:"value"`
		}
		if err := response.DecodeBody(&body); err != nil {
			results <- result{command: command, err: err}
			return
		}
		results <- result{command: command, value: body.Value}
	}
	go request("slow")
	go request("fast")

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("results = %+v, %+v", first, second)
	}
	if first.command != "fast" || first.value != "fast" {
		t.Errorf("first result = %+v, want the fast response", first)
	}
	if second.command != "slow" || second.value != "slow" {
		t.Errorf("second result = %+v, want the slow response", second)
	}
}

func TestClientLateResponseAfterTimeout(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	adapter.Handle("slow", func(req daptest.Request) *daptest.Payload {
		go func() {
			time.Sleep(80 * time.Millisecond)
			_ = adapter.Respond(req, daptest.Payload{Success: true, Body: map[string]any{"late": true}})
		}()
		return nil
	})
	adapter.Handle("ok", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true, Body: map[string]any{"ok": true}}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Request(ctx, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request(slow) error = %v, want deadline exceeded", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for client.LateResponses() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("late responses = %d, want at least 1", client.LateResponses())
		}
		time.Sleep(5 * time.Millisecond)
	}

	ctx2, cancel2 := requestContext(t)
	defer cancel2()
	response, err := client.Request(ctx2, "ok", nil)
	if err != nil {
		t.Fatalf("Request(ok) error = %v", err)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := response.DecodeBody(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Errorf("body = %+v", body)
	}
}

func TestClientDAPErrorResponse(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	adapter.Handle("fail", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{
			Success: false,
			Message: "boom",
			Body:    map[string]any{"code": 7},
		}
	})
	adapter.Handle("ok", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true}
	})

	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.Request(ctx, "fail", nil); err == nil {
		t.Fatal("Request(fail) error = nil")
	} else {
		var responseErr *ResponseError
		if !errors.As(err, &responseErr) {
			t.Fatalf("error type = %T (%v), want *ResponseError", err, err)
		}
		if responseErr.Command != "fail" || responseErr.Message != "boom" {
			t.Errorf("error = %+v", responseErr)
		}
		var body struct {
			Code int `json:"code"`
		}
		if err := responseErr.Response.DecodeBody(&body); err != nil {
			t.Fatal(err)
		}
		if body.Code != 7 {
			t.Errorf("body = %+v", body)
		}
	}

	if _, err := client.Request(ctx, "ok", nil); err != nil {
		t.Errorf("Request(ok) after a DAP error = %v", err)
	}
}

func TestClientEOFUnblocksPendingRequest(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	seen := make(chan struct{}, 1)
	adapter.Handle("never", func(daptest.Request) *daptest.Payload {
		select {
		case seen <- struct{}{}:
		default:
		}
		return nil
	})

	requestErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Request(ctx, "never", nil)
		requestErr <- err
	}()
	waitForSignal(t, seen)

	if err := adapter.Close(); err != nil {
		t.Fatalf("adapter.Close() error = %v", err)
	}
	select {
	case err := <-requestErr:
		if !errors.Is(err, ErrEOF) {
			t.Errorf("Request() error = %v, want ErrEOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request() did not return after EOF")
	}
	if state := client.State(); state != StateFailed {
		t.Errorf("state = %q, want failed", state)
	}
	if !errors.Is(client.Err(), ErrEOF) {
		t.Errorf("Err() = %v, want ErrEOF", client.Err())
	}
	select {
	case _, ok := <-client.Events():
		if ok {
			t.Error("events channel delivered an event, want it closed")
		}
	case <-time.After(time.Second):
		t.Fatal("events channel was not closed")
	}
}

func TestClientCloseUnblocksPendingRequest(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	seen := make(chan struct{}, 1)
	adapter.Handle("never", func(daptest.Request) *daptest.Payload {
		select {
		case seen <- struct{}{}:
		default:
		}
		return nil
	})
	requestErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Request(ctx, "never", nil)
		requestErr <- err
	}()
	waitForSignal(t, seen)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-requestErr:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Request() error = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request() did not return after Close")
	}
	if state := client.State(); state != StateClosed {
		t.Errorf("state = %q, want closed", state)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close() error = %v", err)
	}
	if _, err := client.Request(context.Background(), "threads", nil); err == nil {
		t.Error("Request() after Close error = nil")
	}
	select {
	case _, ok := <-client.Events():
		if ok {
			t.Error("events channel delivered an event, want it closed")
		}
	case <-time.After(time.Second):
		t.Fatal("events channel was not closed")
	}
}

func TestClientRequestBeforeInitializeAndInvalidArguments(t *testing.T) {
	client, _ := newClientPair(t, Options{})
	if _, err := client.Request(context.Background(), "threads", nil); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("Request() before Initialize error = %v, want ErrNotInitialized", err)
	}
	if _, err := client.Initialize(context.Background(), InitializeRequestArguments{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Initialize() without adapterID error = %v, want ErrInvalidArgument", err)
	}
	if state := client.State(); state != StateCreated {
		t.Errorf("state = %q, want created", state)
	}
	if _, err := client.Request(nil, "threads", nil); !errors.Is(err, ErrNilContext) {
		t.Errorf("Request(nil) error = %v, want ErrNilContext", err)
	}
}

func TestClientInitializeFailureMarksFailed(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	adapter.Handle("initialize", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: false, Message: "refused"}
	})
	if _, err := client.Initialize(context.Background(), InitializeRequestArguments{AdapterID: "mock"}); err == nil {
		t.Fatal("Initialize() error = nil")
	}
	if state := client.State(); state != StateFailed {
		t.Errorf("state = %q, want failed", state)
	}
	if _, err := client.Request(context.Background(), "threads", nil); err == nil {
		t.Error("Request() after failed Initialize error = nil")
	}
}

func TestClientPendingLimit(t *testing.T) {
	client, adapter := newClientPair(t, Options{MaxPendingRequests: 1})
	initializeClient(t, client, adapter, nil)

	seen := make(chan struct{}, 1)
	adapter.Handle("never", func(daptest.Request) *daptest.Payload {
		select {
		case seen <- struct{}{}:
		default:
		}
		return nil
	})
	first := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Request(ctx, "never", nil)
		first <- err
	}()
	waitForSignal(t, seen)

	if _, err := client.Request(context.Background(), "never", nil); !errors.Is(err, ErrPendingLimit) {
		t.Errorf("second Request() error = %v, want ErrPendingLimit", err)
	}
	_ = client.Close()
	select {
	case err := <-first:
		if err == nil {
			t.Error("first Request() error = nil after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Request() did not return after Close")
	}
}

func TestClientReverseRequests(t *testing.T) {
	var mu sync.Mutex
	var commands []string
	client, adapter := newClientPair(t, Options{
		ReverseRequestHandler: func(_ context.Context, request ReverseRequest) (any, error) {
			mu.Lock()
			commands = append(commands, request.Command)
			mu.Unlock()
			switch request.Command {
			case "runInTerminal":
				var args struct {
					Kind string `json:"kind"`
				}
				if err := request.DecodeArguments(&args); err != nil {
					return nil, err
				}
				return map[string]any{"accepted": true, "kind": args.Kind}, nil
			default:
				return nil, fmt.Errorf("unsupported %q", request.Command)
			}
		},
	})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := requestContext(t)
	defer cancel()
	response, err := adapter.SendReverseRequest(ctx, "runInTerminal", map[string]any{"kind": "integrated"})
	if err != nil {
		t.Fatalf("SendReverseRequest() error = %v", err)
	}
	if !response.Success {
		t.Fatalf("reverse response = %+v", response)
	}
	var body struct {
		Accepted bool   `json:"accepted"`
		Kind     string `json:"kind"`
	}
	if err := response.DecodeBody(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Accepted || body.Kind != "integrated" {
		t.Errorf("body = %+v", body)
	}

	response, err = adapter.SendReverseRequest(ctx, "custom", nil)
	if err != nil {
		t.Fatalf("SendReverseRequest(custom) error = %v", err)
	}
	if response.Success {
		t.Error("custom reverse request succeeded, want a rejection")
	}
	if response.Message == "" {
		t.Error("custom reverse request has no rejection message")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 2 {
		t.Errorf("commands = %v, want two handler calls", commands)
	}
}

func TestClientDefaultReverseRequestRejected(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := requestContext(t)
	defer cancel()
	response, err := adapter.SendReverseRequest(ctx, "runInTerminal", nil)
	if err != nil {
		t.Fatalf("SendReverseRequest() error = %v", err)
	}
	if response.Success {
		t.Error("reverse request succeeded without a handler")
	}
	if response.Message == "" {
		t.Error("rejection has no message")
	}
}

func TestClientWaitForEventTimeout(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.WaitForEvent(ctx, "stopped"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForEvent() error = %v, want deadline exceeded", err)
	}

	ctx2, cancel2 := requestContext(t)
	defer cancel2()
	if err := adapter.SendEvent("stopped", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WaitForEvent(ctx2, "stopped"); err != nil {
		t.Fatalf("WaitForEvent() error = %v", err)
	}
}

func TestClientCanceledBeforeSend(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	called := make(chan struct{}, 1)
	adapter.Handle("never", func(daptest.Request) *daptest.Payload {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Request(ctx, "never", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Request() error = %v, want context canceled", err)
	}
	select {
	case <-called:
		t.Error("cancelled request reached the adapter")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClientEncodeErrorDoesNotKillSession(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.Request(ctx, "bad", make(chan int)); err == nil {
		t.Fatal("Request() with a non-JSON argument error = nil")
	}
	adapter.Handle("ok", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true}
	})
	if _, err := client.Request(ctx, "ok", nil); err != nil {
		t.Errorf("Request(ok) after an encode error = %v", err)
	}
}

type blockingTransport struct {
	closed       chan struct{}
	writeStarted chan struct{}
	closeOnce    sync.Once
	writeOnce    sync.Once
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{
		closed:       make(chan struct{}),
		writeStarted: make(chan struct{}),
	}
}

func (b *blockingTransport) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingTransport) Write([]byte) (int, error) {
	b.writeOnce.Do(func() { close(b.writeStarted) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingTransport) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestClientBlockedTransportCancellationAndClose(t *testing.T) {
	transport := newBlockingTransport()
	client, err := New(transport, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	requestErr := make(chan error, 1)
	go func() {
		_, err := client.Request(ctx, "initialize", InitializeRequestArguments{AdapterID: "mock"})
		requestErr <- err
	}()
	waitForSignal(t, transport.writeStarted)
	cancel()

	select {
	case err := <-requestErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Request() error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request() did not return after cancellation")
	}

	done := make(chan struct{})
	go func() {
		_ = client.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() blocked on a blocked transport")
	}
	if state := client.State(); state != StateClosed {
		t.Errorf("state = %q, want closed", state)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a test signal")
	}
}

func TestClientConfirmRunningAfterStopOnEntry(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)
	if err := client.BeginLaunch(); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SendEvent("stopped", map[string]any{"reason": "entry"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.WaitForEvent(ctx, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := client.ConfirmRunning(); err != nil {
		t.Fatalf("ConfirmRunning() after stop-on-entry error = %v", err)
	}
	if state := client.State(); state != StateStopped {
		t.Errorf("state = %q, want stopped", state)
	}
}

func TestClientWaitForEventAfterClose(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.WaitForEvent(ctx, "stopped"); !errors.Is(err, ErrClosed) {
		t.Errorf("WaitForEvent() after Close error = %v, want ErrClosed", err)
	}
	if _, err := client.WaitForEvent(ctx, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("WaitForEvent(empty) error = %v, want ErrInvalidArgument", err)
	}
}

func TestClientFrameTooLargeRequest(t *testing.T) {
	client, adapter := newClientPair(t, Options{MaxFrameBytes: 4096})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.Request(ctx, "echo", strings.Repeat("x", 5000)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("Request() error = %v, want ErrFrameTooLarge", err)
	}
	adapter.Handle("ok", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true}
	})
	if _, err := client.Request(ctx, "ok", nil); err != nil {
		t.Errorf("Request(ok) after an oversized frame = %v", err)
	}
}

func TestClientWriteDeadlinePath(t *testing.T) {
	client, adapter := newClientPair(t, Options{WriteTimeout: time.Second})
	initializeClient(t, client, adapter, nil)
	adapter.Handle("ok", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true}
	})
	ctx, cancel := requestContext(t)
	defer cancel()
	if _, err := client.Request(ctx, "ok", nil); err != nil {
		t.Fatalf("Request() error = %v", err)
	}
}

func TestClientReverseHandlerPanic(t *testing.T) {
	client, adapter := newClientPair(t, Options{
		ReverseRequestHandler: func(context.Context, ReverseRequest) (any, error) {
			panic("boom")
		},
	})
	initializeClient(t, client, adapter, nil)

	ctx, cancel := requestContext(t)
	defer cancel()
	response, err := adapter.SendReverseRequest(ctx, "runInTerminal", nil)
	if err != nil {
		t.Fatalf("SendReverseRequest() error = %v", err)
	}
	if response.Success {
		t.Error("panicking handler produced a successful response")
	}
	if response.Message == "" {
		t.Error("panicking handler produced no rejection message")
	}
}

func TestClientInitializeRejectsInvalidCapabilities(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	adapter.Handle("initialize", func(daptest.Request) *daptest.Payload {
		return &daptest.Payload{Success: true, Body: []string{"not", "capabilities"}}
	})
	if _, err := client.Initialize(context.Background(), InitializeRequestArguments{AdapterID: "mock"}); err == nil {
		t.Fatal("Initialize() error = nil, want a capabilities decoding error")
	}
	if state := client.State(); state != StateFailed {
		t.Errorf("state = %q, want failed", state)
	}
}

func TestDecodeRawInvalidJSON(t *testing.T) {
	event := Event{Body: []byte(`{"broken":`)}
	var target struct{}
	if err := event.DecodeBody(&target); err == nil {
		t.Error("DecodeBody() error = nil for invalid JSON")
	}
	response := Response{Body: []byte(`{"broken":`)}
	if err := response.DecodeBody(&target); err == nil {
		t.Error("Response.DecodeBody() error = nil for invalid JSON")
	}
}

func TestNewRejectsNilTransport(t *testing.T) {
	if _, err := New(nil, Options{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("New(nil) error = %v, want ErrInvalidArgument", err)
	}
}

func TestClientUnknownResponseSequenceIsLate(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	if err := adapter.Respond(daptest.Request{Seq: 999, Command: "unknown"}, daptest.Payload{Success: true}); err != nil {
		t.Fatalf("Respond() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for client.LateResponses() < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("late responses = %d, want at least 1", client.LateResponses())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClientWaitForEventDiscardsOtherEvents(t *testing.T) {
	client, adapter := newClientPair(t, Options{})
	initializeClient(t, client, adapter, nil)

	for _, name := range []string{"output", "stopped"} {
		if err := adapter.SendEvent(name, nil); err != nil {
			t.Fatalf("SendEvent(%s) error = %v", name, err)
		}
	}
	ctx, cancel := requestContext(t)
	defer cancel()
	event, err := client.WaitForEvent(ctx, "stopped")
	if err != nil {
		t.Fatalf("WaitForEvent(stopped) error = %v", err)
	}
	if event.Name != "stopped" {
		t.Errorf("event = %q, want stopped", event.Name)
	}
}
