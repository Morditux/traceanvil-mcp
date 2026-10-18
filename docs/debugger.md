# GDB backend and debug API (J6)

J6 adds the first real debugger backend: GDB over the Debug Adapter Protocol
(DAP). It uses the generic DAP client from J5 and exposes a bounded MCP surface
for launching a declared target, setting breakpoints, inspecting threads, stacks,
scopes and variables, resuming/pausing/stepping and releasing the session.

The backend is deliberately narrower than GDB:

- no arbitrary PID attach;
- no expression evaluation;
- no raw memory read or write;
- no GDB command, Python script or init file supplied by a client;
- no GDB/MI fallback;
- no LLDB backend yet (J7);
- no debuggee terminal I/O.

## Code map

```text
internal/debugger/
├── types.go      debug_* request/response types, states and bounds
├── errors.go     stable named error codes
├── manager.go    session ids, concurrency quota, retention, shutdown
├── service.go    target policy, source resolution, API validation
├── gdb.go        DAP session, startup handshake, tools and event loop
└── process.go    GDB process, fixed arguments, process-group cleanup
```

`internal/mcpserver/debug.go` only translates MCP calls to the service. The
generic framing/correlation client is `internal/dap`.

## Launching GDB

The server starts one GDB process per session with a fixed argument list:

```sh
gdb --quiet --nx --interpreter=dap \
  -ex 'set startup-with-shell off' \
  -ex 'set confirm off' \
  -ex 'set pagination off' \
  -ex 'set auto-load python-scripts off' \
  -ex 'set auto-load gdb-scripts off' \
  -ex 'set auto-load local-gdbinit off' \
  -ex 'set debuginfod enabled off' \
  -ex 'set history save off' \
  -ex 'set inferior-tty /dev/null'
```

- `--nx` skips user init files; `--quiet` reduces banner output.
- Python scripts, GDB command scripts and local `.gdbinit` auto-loading are
  disabled so a target cannot get GDB to execute auto-loaded scripts.
- `debuginfod` is disabled so symbolization cannot perform network requests.
- `startup-with-shell` is disabled; the debuggee is launched with an argument
  vector, not a shell.
- The inferior tty is `/dev/null`, so the debuggee cannot consume DAP bytes or
  write into the protocol stream. Debuggee stdout/stderr are not exposed.
- Startup fails closed if GDB stderr reports that one of these hardening commands
  was undefined, instead of running without the requested restriction.
- GDB runs with a minimal environment (`PATH`, `LANG`/`LC_ALL=C`, `TERM=dumb`)
  and a private `HOME`/`TMPDIR` directory (`0700`, removed on teardown). The
  server environment and secrets are not inherited.
- GDB runs in a dedicated process group; termination sends TERM, then KILL after
  a short grace period. GDB 17.1 places the inferior in its own process group, so
  debuggee termination relies on GDB's own teardown and ptrace behavior, not on
  the GDB group alone. A debuggee that survives GDB or changes group can escape
  group cleanup; system-level isolation is required for hostile targets.

The GDB binary is resolved by the capability probe and revalidated immediately
before every start: absolute path, regular file, executable. The target is
resolved through the same workspace policy as Valgrind runs: declared id, allowed
argument multiset, symlink-resolved containment, regular executable file.

## Startup sequence

1. Start GDB and the DAP client (one reader, one writer).
2. Send `initialize` with adapter id `gdb`; store the adapter capabilities.
3. Wait for the DAP `initialized` event.
4. Write the `launch` request with the absolute program, allowed args, target
   cwd and `stopOnEntry`.
5. If `supportsConfigurationDoneRequest` is true, send `configurationDone`.
6. Await the `launch` response.
7. With `stop_on_entry=true`, wait for the first `stopped` event. With
   `stop_on_entry=false`, briefly wait for the first thread event and report the
   session as `running` (or `stopped`/`terminated` if the target settled first).

GDB does not answer `launch` until `configurationDone` arrives. It also
dispatches those two requests concurrently and can reject `configurationDone`
with "launch or attach not specified" before the launch state is installed. The
server writes `launch` first and retries only that transient response with a
bounded backoff; any other launch error aborts startup. This behavior is covered
by real GDB tests.

GDB 17.1 advertises `supportTerminateDebuggee=true`, but a real detach test
showed that releasing GDB did not reliably preserve the inferior. J6 therefore
exposes termination only: `debug_stop(terminate=false)` returns
`unsupported_capability`. Termination sends `disconnect(terminateDebuggee=true)`
and also terminates the GDB process group rather than relying on the adapter
capability alone.

## Session states

| State | Meaning |
|---|---|
| `starting` | GDB is being spawned and the DAP handshake is in progress. |
| `running` | A resume/pause request was accepted; the target may be executing. |
| `stopped` | GDB reported a `stopped` event. |
| `terminated` | The debuggee ended; an exit code may be present. |
| `detached` | Reserved for future adapters with a verified detach; not produced at J6. |
| `failed` | The transport or protocol failed, or the session was closed by the server. |

`stopped` is a debugger observation, not a correctness conclusion. A session can
stop at the process entry point (`_start`) in the dynamic loader; set a
breakpoint and continue to reach source.

`stop_generation` increments on every `stopped` event. Frame ids, scope
references and variable references are valid only for the generation that
produced them; a resume invalidates them. `debug_status` exposes the latest stop
event and generation without contacting GDB. Stack, scope and variable requests
accept an optional `stop_generation`; a stale value returns `stale_reference`.

### Execution actions

`debug_continue` returns `accepted=true` without waiting for a stop.
`debug_step` and `debug_pause` wait up to 5 seconds for the next stopped event;
if it does not arrive, they return `accepted=true` with state `running` and a
warning. Callers poll `debug_status` until `stop_generation` changes. A resume
request that is accepted but whose response times out leaves the reported state
as `running`: the request may already have reached GDB and cannot be recalled.

`debug_stop` is idempotent. It returns `already_stopped=true` for an already
terminated session. `terminate` defaults to true. J6 does not implement detach:
`terminate=false` returns `unsupported_capability` and leaves the session
running (call `debug_stop` with `terminate=true`).

## Tools

| Tool | Requires stopped | Notes |
|---|---|---|
| `debug_start` | — | Declared target, allowed args, `stop_on_entry` default true. |
| `debug_status` | — | Snapshot, no adapter request. |
| `debug_set_breakpoints` | yes | Workspace source only; up to 100 deduplicated lines. |
| `debug_continue` | yes | Asynchronous. |
| `debug_pause` | no (requires running) | Waits up to 5 s for the stop. |
| `debug_step` | yes | `next`, `in` or `out`; waits up to 5 s for the stop. |
| `debug_threads` | no | Bounded page of adapter threads. |
| `debug_stack_trace` | yes | At most 100 frames per page; optional `stop_generation`; `has_more`/`next_frame`. |
| `debug_scopes` | yes | Scope references for the current generation; optional `stop_generation`. |
| `debug_variables` | yes | At most 200 children and 2048 bytes per value; optional `stop_generation`; explicit truncation. |
| `debug_stop` | — | Terminate only at J6; always available for a session. |

All request contexts are bounded to 10 seconds. Session startup is bounded to 15
seconds, a stop-on-entry wait to 5 seconds and stop/disconnect to 5 seconds.

### Source paths

`debug_set_breakpoints.source_path` may be absolute or relative to the target
cwd. It is resolved with `EvalSymlinks`, must be a regular file inside a
configured workspace root and cannot escape through a symlink. Inputs outside the
roots return `source_denied`.

Stack and breakpoint sources inside a root are returned as slash-separated
relative paths. External sources expose only a display name and `external=true`;
absolute system paths, instruction pointers, module paths and raw memory
references are omitted.

Missing or stripped symbols are reported with a warning. The server never
fabricates a function or source location.

## Retention and cleanup

- At most `limits.max_sessions` live sessions (1–32, default 2).
- Live sessions idle for 15 minutes are closed by the reaper.
- Terminal/detached records are retained for 5 minutes for `debug_status` and
  `debug_stop`, then evicted. The session map is also capped.
- Server shutdown closes all sessions and waits for GDB processes.
- `debug_stop` releases the DAP client, terminates GDB and removes the private
  session directory.
- A debuggee that ends on its own triggers a short delayed release; its session
  record remains queryable until the retention expiry.

## Limitations

- GDB-only at J6. LLDB and its adapter discovery are J7.
- No attach, core dump, expression evaluation, data breakpoints, memory
  read/write or GDB command passthrough.
- Debuggee stdout/stderr are not surfaced; the inferior tty is `/dev/null`.
- GDB auto-load scripts (Python and GDB command scripts) and local `.gdbinit`
  are disabled for security. Language pretty-printers distributed as auto-load
  scripts are therefore unavailable; variables are shown with GDB's built-in
  formatting.
- `debug_continue` never waits for a stop; callers must poll `debug_status`.
  `debug_step` and `debug_pause` wait at most five seconds and may still return
  `running`, in which case polling is required.
- GDB can stop at `_start`; with stripped binaries, frames and breakpoints may
  have no source or symbols. The response marks this degradation but cannot
  restore missing debug information.
- A target that calls `setsid` may escape process-group cleanup; system-level
  isolation remains the only robust containment.
- Session retention is in memory and is lost on server restart; J6 does not
  persist debugger evidence.
- There is no maximum wall-clock session duration. A session kept active by
  status polling can run until `debug_stop` or server shutdown; the idle timeout
  only closes sessions without API activity. The live-session quota bounds the
  number of concurrent debuggees, not their total CPU or memory use.
- GDB's DAP implementation can differ across versions; the probe requires a
  successful `initialize`, and session capabilities are rechecked before use.
- Bounded GDB stderr excerpts and adapter error text may appear in an error
  message and can contain local absolute paths; they are not file contents, and
  stack/breakpoint source fields still normalize workspace paths and omit
  external ones.
- GDB 17.1 rejects `variables` start/count values above a child range, so the
  backend requests the child list and paginates locally. If one DAP response
  exceeds the 16 MiB frame limit, the call fails as a typed protocol error
  rather than returning silently truncated data.

## Tests

```sh
go test ./internal/debugger
go test -race ./internal/debugger
go test ./internal/capabilities
go test ./tests/e2e -run TestServeGDBDebugWorkflow
```

Real GDB tests require `gdb` and `cc` and skip with a documented reason when
either is missing. They cover:

- stop-on-entry, verified breakpoints, continue, stack, scopes and locals;
- step and a new stop generation;
- running start, pause, threads and stack;
- stop while running and process-group cleanup;
- stripped-symbol degraded evidence and no absolute source paths;
- MCP stdio end to end from `debug_start` to `debug_stop`.
