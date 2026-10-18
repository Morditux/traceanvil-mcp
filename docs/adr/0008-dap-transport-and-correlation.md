# ADR 0008 — Generic DAP transport, correlation and session state

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J5

## Context

J5 introduces the DAP client that J6 (GDB) and J7 (LLDB) will share. It must
handle real adapters over stdio: Content-Length framing, concurrent requests,
interleaved events, reverse requests, timeouts, shutdown and capability
negotiation. It must not assume that `launch` completes synchronously or that an
adapter's ordering of the `initialize` response and `initialized` event is fixed.

No MCP tool is exposed at J5 and no debugger is launched yet. The client is an
internal foundation, tested with a mock adapter and fuzz targets.

## Decision

### Package boundary

- `internal/dap` owns framing, the client, session state, capabilities and typed
  errors.
- `internal/dap/daptest` is a small scripted adapter with its own framing
  implementation. Tests therefore exercise two independent decoders.
- J6/J7 backends translate DAP commands and payloads; they do not reimplement
  framing, request correlation, timeouts or shutdown.

### Framing

- `Content-Length` counts **bytes**, is required exactly once, is parsed as a
  positive integer and is checked against a 16 MiB default limit before the body
  allocation. Headers are matched case-insensitively; unknown headers are
  ignored; LF-only input is accepted.
- The header block is limited to 64 KiB and read with `bufio.ReadLine`
  fragments, so a single long line cannot grow an unbounded allocation.
- The body is read with `io.ReadFull`. A truncated body is a protocol failure,
  not a clean EOF.
- The JSON envelope must have a positive `seq` and a valid `type`. Requests use
  `arguments`, responses and events use `body`; both remain raw until a backend
  decodes them.
- Framing errors are `*ProtocolError` values carrying a sentinel
  (`ErrMalformedFrame`, `ErrHeaderTooLarge`, `ErrFrameTooLarge`) and the cause.
  `errors.Is` works for both.

### Reader and writer ownership

- Exactly one reader goroutine owns the input. It dispatches responses to
  pending futures, updates state for terminal events, delivers events and
  handles reverse requests.
- Exactly one writer goroutine owns the output. Frames enter a bounded queue,
  are encoded by the caller before enqueueing, and are written atomically under
  the writer's sole ownership.
- `Close` cancels the client context, fails pending futures, closes the
  transport, waits for both goroutines and closes the event channel. It requires
  the transport's `Close` to unblock reads and writes.

### Request correlation and timeouts

- Request sequence numbers start at 1 and are assigned atomically.
- A request registers a buffered response future before sending. The future is
  removed on response, timeout, cancellation or shutdown.
- A `success=false` response is a request-level `*ResponseError`; the session
  remains usable. A response for a removed future is ignored and counted by
  `LateResponses()` (timeouts, cancellation, unknown `request_seq`).
- A context canceled before send means no frame is written. A frame already in
  progress cannot be recalled by cancellation; this is inherent to a generic
  `io.Writer`. The writer rechecks a queued frame's context before writing, so a
  message still queued at timeout is dropped.
- `WriteTimeout` uses `SetWriteDeadline` when the transport supports it. Plain
  OS pipes have no write deadline; `Close` is the only way to unblock them.
- `MaxPendingRequests` bounds futures. A full write queue blocks senders (bounded
  memory) until space or context/Close.
- Non-`initialize` requests require the initialized state and return
  `ErrNotInitialized` otherwise.

### Events and backpressure

- There is one bounded event channel. A slow consumer applies backpressure to
  the reader, which can delay responses; this is deliberate and documented.
- `WaitForEvent` consumes the same channel and discards unmatched events. It is
  a single-consumer convenience; backends that need every event consume
  `Events()` directly.
- The client updates `stopped`, `continued`, `exited` and `terminated` state
  before delivering the event.
- Reverse requests are handled in goroutines bounded by a semaphore, so a slow
  handler does not block responses but cannot grow unbounded. A nil handler
  rejects the request with `success=false`; a panic becomes a failed response;
  handlers receive a context canceled by `Close`.

### State machine

- States: `created`, `starting`, `initialized`, `launching`, `running`,
  `stopped`, `terminated`, `cancelled`, `timed_out`, `failed`, `closed`.
- `starting` means the initialize request is in flight. `initialized` means the
  initialize **response** was accepted. The DAP `initialized` event is delivered
  separately so a backend controls when `launch`/`attach` and
  `configurationDone` are sent.
- `BeginLaunch` performs `initialized -> launching`; `ConfirmRunning` performs
  `launching -> running` and accepts an already-stopped stop-on-entry session.
  This avoids assuming that a launch response implies running.
- Event transitions are synchronized and ignored when impossible; explicit API
  transitions return `ErrInvalidState`.
- `MarkCancelled`/`MarkTimedOut` are backend decisions. Transport/protocol
  failures move an active session to `failed`; a transport error after
  `terminated`/`cancelled`/`timed_out` keeps the session outcome until `Close`
  moves it to `closed`.

### Capabilities

- `Initialize` fills `clientID`, `clientName`, `adapterID` (required),
  `pathFormat: "path"`, `linesStartAt1` and `columnsStartAt1` defaults, and sends
  the optional client capability flags supplied by the caller.
- The response body is decoded into a typed subset and a raw boolean map.
  `Supports(name)` answers future capabilities without a client update. A body
  that is not an object, or a known capability of the wrong type, fails
  initialization and moves the session to `failed`.

### Limits

| Bound | Value |
|---|---|
| Header block | 64 KiB |
| One frame | 16 MiB |
| Write queue | 32 frames |
| Event buffer | 128 events |
| Pending futures | 128 |
| Concurrent reverse handlers | 8 |

## Consequences

- J6/J7 get one audited transport and correlation layer. They only build typed
  DAP commands and translate results.
- Backpressure is explicit: bounded queues and limits are preferable to dropping
  debugger events silently or growing memory without bound.
- A timed-out request can still have reached the adapter if its write was already
  in progress. Backends must not assume that a timeout proves the command was
  not executed.
- A slow event consumer delays responses. Backends must drain events promptly,
  or run a dedicated event loop.
- Reverse handlers are cancelled but not joined; a handler that ignores its
  context can outlive the client.
- `launch` is intentionally not wrapped in J5. J6 will implement launch/attach
  semantics, set breakpoints, drive configuration and own teardown.
- No MCP tool is added by J5; `debug_*` tools remain J6 work.
