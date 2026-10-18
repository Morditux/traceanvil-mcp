# Generic DAP client (J5, used by J6/J7)

J5 adds an internal Debug Adapter Protocol (DAP) client shared by the GDB and
LLDB backends. J6 uses it for real GDB sessions and J7 adds lldb-dap without
forking the client; backend policy, target resolution and debug tools live in
`internal/debugger`. The client is not callable directly over MCP.

```text
internal/dap/          framing, client, state machine, capabilities, errors
internal/dap/daptest/  reusable scripted adapter for tests (and J6/J7)
```

## Transport and framing

DAP over stdio is framed as:

```text
Content-Length: <byte length>\r\n\r\n<JSON UTF-8>
```

- `Content-Length` is always a **byte** count, never a rune count. Multi-byte
  UTF-8 is tested explicitly.
- Header names are matched case-insensitively; `Content-Length` is required and
  must appear exactly once. Unknown headers are ignored. LF-only headers are
  accepted in addition to CRLF.
- Header block limit: 64 KiB, enforced line by line with `bufio.ReadLine`
  fragments so a long line is rejected before it can grow an unbounded buffer.
- Frame limit: 16 MiB by default. `Content-Length` must be positive and is
  checked before the body allocation.
- The body is read with `io.ReadFull`; a truncated body is a protocol failure,
  not a clean EOF.
- The JSON envelope must have a non-negative `seq` and a `type` of `request`,
  `response` or `event`. lldb-dap starts at `seq:0` and was observed using zero
  for every message, so zero is accepted while negative values are rejected.
  Responses need a positive `request_seq`; events need a name; requests need a
  command.
- `body` is used by responses/events and `arguments` by requests. Both stay
  `json.RawMessage` until the backend decodes them.

Failures return `*ProtocolError` with a stable sentinel (`ErrMalformedFrame`,
`ErrHeaderTooLarge`, `ErrFrameTooLarge`) and the underlying cause. `errors.Is`
works for both the sentinel and causes such as `io.ErrUnexpectedEOF`.

## Client ownership

`dap.New(conn, Options)` starts exactly two goroutines:

1. **Reader** — the only owner of the input stream. It parses frames, dispatches
   responses to pending futures, delivers events and handles reverse requests.
2. **Writer** — the only owner of the output stream. All frames are serialized
   through a bounded queue.

`Client.Close` cancels the client context, fails pending futures, closes the
transport, waits for both goroutines and closes the event channel. It is
idempotent. The transport's `Close` must unblock reads and writes, as `net.Conn`
and OS pipes do.

### Requests and failures

- Request sequence numbers start at 1 and are assigned atomically.
- Before sending, a request registers a buffered response future in a
  `seq -> future` map. The future is removed on response, timeout, cancellation
  or shutdown.
- `success=true` returns a `*Response`. `success=false` returns a
  `*ResponseError` carrying the command, adapter message, raw body and full
  response. A DAP error does **not** kill the session.
- A response whose future no longer exists (timeout, cancellation, unknown
  `request_seq`) is ignored and counted by `LateResponses()`.
- `MaxPendingRequests` bounds unanswered futures; exceeding it returns
  `ErrPendingLimit`.
- The client must be initialized before non-`initialize` requests; otherwise
  `ErrNotInitialized` is returned.
- A request context bounds the wait for the response. A context canceled before
  send means no frame is written. A context canceled after a frame is already
  being written cannot recall it; this is an inherent limitation of a generic
  `io.Writer` and is documented in the ADR.
- `RequestAsync` writes the request frame and returns a `*Future` without
  waiting for the adapter response. `Future.Wait(ctx)` retrieves the correlated
  result exactly once and caches it for later calls. This exists for adapters
  whose launch response ordering is not synchronous. GDB only answers `launch`
  after `configurationDone`; lldb-dap answers `launch` before sending the DAP
  `initialized` event. Each backend applies its documented ordering in
  `internal/debugger/backend.go`. An async future still counts against
  `MaxPendingRequests` until it is awaited, cancelled or the client closes.

The writer queue is bounded (32 frames by default). When it is full, `send`
blocks until space is available or the context/Close ends the wait. Before a
queued frame is written, its context is checked again, so a timed-out message
that is still queued is dropped instead of being sent late. `WriteTimeout`, when
positive, uses `SetWriteDeadline` on transports that support it and has no effect
on plain pipes.

### Events

Events are delivered on a single bounded channel (`Events()`, 128 entries by
default). The consumer must drain it: a full buffer applies backpressure to the
reader, which delays both further events and responses. `WaitForEvent` is a
single-consumer helper that discards events with another name; backends that need
all events should consume `Events()` directly.

The client updates its state machine before delivering `stopped`, `continued`,
`exited` and `terminated`.

## Session state machine

```text
created -> starting -> initialized -> launching -> running <-> stopped
                                              \-> stopped (stop on entry)
running/stopped/launching/initialized -> terminated | cancelled | timed_out | failed
terminated/cancelled/timed_out/failed -> closed
```

`State` values: `created`, `starting`, `initialized`, `launching`, `running`,
`stopped`, `terminated`, `cancelled`, `timed_out`, `failed`, `closed`.

- `starting` means the `initialize` request is in flight.
- `initialized` means the **response** was accepted. The DAP `initialized` event
  is delivered separately; a backend may wait for it before `launch`/`attach` or
  `configurationDone`. This distinction is deliberate: adapters order the event
  and response differently.
- `BeginLaunch` moves `initialized -> launching`; both backends call it before sending
  `launch`/`attach`.
- `ConfirmRunning` moves `launching -> running`, and accepts an already
  `stopped` session (stop-on-entry).
- `stopped` events transition to `stopped`; `continued` returns to `running`;
  `exited`/`terminated` move to `terminated`.
- `MarkCancelled`/`MarkTimedOut` are explicit backend decisions; they never
  infer success or failure from a raw DAP event.
- `launch` is not assumed to complete synchronously. A backend decides when to
  call `ConfirmRunning`.

The transition table is synchronized by a mutex. Event transitions that are not
allowed are ignored rather than corrupting the transport; explicit API
transitions return `ErrInvalidState`.

## Capability negotiation

`Client.Initialize` sends `initialize` with:

- `clientID`/`clientName` from `Options` (defaults to this server);
- the caller's `adapterID` (required);
- `pathFormat: "path"`, `linesStartAt1: true`, `columnsStartAt1: true` by default;
- optional capability flags provided by the caller.

The response body is decoded twice: into the typed `Capabilities` subset and into
a raw boolean map. `Supports(name)` answers any capability, including future
ones this build does not know. A missing or non-boolean value is `false`. A body
that is not an object, or a known capability with the wrong type, fails
initialization with a protocol error and moves the session to `failed`.
A failed initialization leaves the transport open for diagnostics; explicit
requests then return `ErrInvalidState` and the caller should `Close`. A transport
failure returns its shutdown cause instead.

## Reverse requests

Adapters may send `request` messages (for example `runInTerminal`).
`Options.ReverseRequestHandler` receives a `ReverseRequest` with raw arguments and
returns a body or an error. The client runs the handler in a goroutine bounded by
`ReverseRequestConcurrency` (8 by default), passes the client context, and sends
a `response` with the same `request_seq`.

- A nil handler rejects every reverse request with `success=false` and an
  explicit message.
- A handler may call `Client.Request`; it runs outside the reader loop.
- A panic is recovered and reported as a failed response.
- Handlers are canceled on `Close` but are not joined; a handler that ignores
  its context can outlive the client. This is a documented handler contract.

## Mock adapter

`internal/dap/daptest` is a scripted adapter over `net.Pipe` with its **own**
framing implementation, so client tests exercise two independent decoders.

```go
conn, adapter := daptest.NewPair()
adapter.Handle("initialize", func(req daptest.Request) *daptest.Payload {
    return &daptest.Payload{Success: true, Body: map[string]any{
        "supportsConfigurationDoneRequest": true,
    }}
})
client, err := dap.New(conn, dap.Options{})
```

Available helpers: `Handle`, `Respond`, `SendEvent`, `SendReverseRequest`,
`Close`, `Err`. A handler returns `nil` to answer later from a goroutine; it must
not block the adapter read loop.

## Limits

| Bound | Default |
|---|---|
| Header block | 64 KiB |
| One frame (in and out) | 16 MiB |
| Write queue | 32 frames |
| Event buffer | 128 events |
| Pending request futures | 128 |
| Concurrent reverse handlers | 8 |
| Reader buffer | 4 KiB |

All limits are configurable through `dap.Options` except the reader buffer and
the mock adapter's independent frame bounds.

## Error taxonomy

| Error | Meaning |
|---|---|
| `ErrClosed` | Client closed by its owner. |
| `ErrEOF` | Adapter closed the connection while the client was open. |
| `ErrNotInitialized` | Non-`initialize` request before initialization. |
| `ErrInvalidState` | Disallowed explicit state transition. |
| `ErrInvalidArgument` | Invalid caller value (for example missing `adapterID`). |
| `ErrNilContext` | Nil context passed to an API. |
| `ErrFrameTooLarge` | Frame payload exceeds the limit. |
| `ErrHeaderTooLarge` | Header block or line exceeds the limit. |
| `ErrMalformedFrame` | Missing/duplicate/invalid header or invalid JSON/envelope. |
| `ErrPendingLimit` | Too many unanswered requests. |
| `*ProtocolError` | Framing/encoding failure, wrapping a sentinel and cause. |
| `*ResponseError` | Well-formed DAP response with `success=false`. |

## Tests

```sh
go test ./internal/dap
go test -race ./internal/dap
go test -cover ./internal/dap
go test -fuzz=FuzzReadMessage -fuzztime=5s ./internal/dap
```

Coverage includes fragmented headers and bodies, multiple frames per read,
UTF-8 byte-length frames, zero (`lldb-dap`) and negative sequence numbers,
malformed/oversized/truncated frames, missing and invalid envelope fields, write short-writes, out-of-order responses, late
responses after timeout, DAP error responses, EOF and Close unblocking pending
requests, pending limits, cancellation before send, a blocked transport,
capability negotiation, reverse requests (success, rejection and panic), event
state transitions, stop-on-entry, async launch/configurationDone ordering,
async cancellation and late responses, and the fuzz decoder.

## Limitations

- No read timeout: an idle DAP session is expected. Request contexts bound waits
  for individual responses; `Close` must unblock the transport.
- A write already in progress cannot be recalled by request cancellation; queued
  frames are dropped when their context is already canceled.
- `WriteTimeout` only works when the transport exposes `SetWriteDeadline`.
- The event channel has one consumer; `WaitForEvent` discards unmatched events.
- Reverse handlers are cancelled but not joined; a blocking handler can outlive
  the client.
- The client has no generic read timeout; an idle session is expected. Both
  backends bound each request and close the transport to unblock a session.
- GDB 17.1 dispatches `launch` and `configurationDone` concurrently and can
  reject `configurationDone` with "launch or attach not specified" before the
  launch state is installed. The GDB backend retries only that transient response
  with a bounded backoff; lldb-dap uses the opposite launch ordering. These are
  adapter quirks, not generic DAP rules.
- J5 itself launches no debugger and adds no MCP tool. The GDB backend and debug
  tools are J6; LLDB is J7. Attach and expression evaluation remain later work.
