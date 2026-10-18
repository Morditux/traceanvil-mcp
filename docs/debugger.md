# GDB and LLDB backends and debug API (J6/J7)

J6 added the first real debugger backend: GDB over the Debug Adapter Protocol
(DAP). J7 adds LLDB through `lldb-dap` and keeps one MCP contract for both. The
generic DAP client comes from J5; the session manager, policy checks, pagination
and error model are shared.

The surface is deliberately narrower than a debugger CLI:

- no arbitrary PID attach;
- no expression evaluation;
- no raw memory read or write;
- no debugger command, Python script or init file supplied by a client;
- no GDB/MI fallback;
- no debuggee terminal I/O exposed over MCP;
- no detach in J6/J7; a session is terminated explicitly.

## Code map

```text
internal/debugger/
├── types.go      debug_* request/response types, states and bounds
├── errors.go     stable named error codes
├── backend.go    fixed GDB/LLDB adapter configuration and startup order
├── manager.go    session ids, concurrency quota, retention, shutdown
├── service.go    backend selection, target policy, source resolution, API validation
├── session.go    shared DAP session, startup handshake, tools and event loop
└── process.go    debugger process, fixed arguments, process-group cleanup
```

`internal/mcpserver/debug.go` only translates MCP calls to the service. The
generic framing/correlation client is `internal/dap`.

## Backend selection

`debug_start.backend` accepts `gdb` (default) or `lldb`. Both backends must be
enabled in configuration and must have a capability record in state `available`;
the binary is revalidated immediately before every start. The two backends
expose exactly the same MCP tool names. See
[Backend differences](#backend-differences) for the adapter details that leak
into observations (thread ids, stop reason, terminal behavior).

When `backend` is omitted, the server prefers GDB if it is enabled and
available, then falls back to LLDB so a published `debug_start` remains usable
in an LLDB-only configuration. An explicit backend is never silently replaced:
`backend: "gdb"` returns a typed error if GDB is disabled or unavailable.

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

## Launching LLDB

The server discovers `lldb-dap` and versioned `lldb-dap-<N>` executables in
absolute `PATH` directories. The unsuffixed name is preferred; if it is missing
or cannot complete the handshake, the versioned candidates are tried. A binary
is only usable after a real DAP `initialize` succeeds. The selected absolute path
and version are recorded in `backend.lldb`.

LLDB is started with fixed pre-init settings and a private `HOME`/`TMPDIR`:

```sh
lldb-dap \
  -c 'settings set target.load-cwd-lldbinit false' \
  -c 'settings set symbols.enable-external-lookup false'
```

- `target.load-cwd-lldbinit false` prevents a target directory `.lldbinit` from
  being loaded.
- `symbols.enable-external-lookup false` disables debuginfod/external symbol
  lookup, so symbolization does not perform network requests.
- `HOME`/`TMPDIR` point to a private `0700` directory removed on teardown, so a
  user `~/.lldbinit` is unreachable. No client-supplied LLDB command, Python
  script, init file or expression is accepted.
- `lldb-dap` gives the inferior a private PTY (`/dev/pts/N`), not the DAP pipe.
  Debuggee stdio is not exposed over MCP. A debuggee that reads stdin may block
  on the PTY; it must be stopped or terminated normally.
- `lldb-dap` runs in a dedicated process group. Termination sends
  `disconnect(terminateDebuggee=true)` and then group cleanup with TERM/KILL.
  The debuggee's own process group may differ; a fixture PID verifies that it is
  gone after `debug_stop`.

The verified reference adapter is LLDB 21.1.8. The adapter's `--version` probe is
optional because older `lldb-dap` versions do not guarantee terminating
behavior; the DAP handshake is the functional proof. Fixed pre-init settings are
not reported back by `lldb-dap` when unknown, so compatibility with older LLDB
versions is a documented limitation rather than a validated capability.

## Startup sequence

Common steps:

1. Start the adapter and the DAP client (one reader, one writer).
2. Send `initialize` with adapter id `gdb` or `lldb`; store capabilities.
3. Send `launch` with the absolute program, allowed args, target cwd and
   `stopOnEntry`.
4. Send `configurationDone` when the adapter supports it.
5. Await the launch response and, with `stop_on_entry=true`, the first
   `stopped` event.

The DAP ordering differs:

- **GDB** sends `initialized` before `launch`. GDB does not answer `launch` until
  `configurationDone` arrives. It also dispatches those two requests
  concurrently and can reject `configurationDone` with "launch or attach not
  specified" before the launch state is installed. The server waits for
  `initialized`, writes `launch`, retries only that transient response with a
  bounded backoff, then awaits the launch response.
- **LLDB** accepts `launch` first and sends `initialized` only afterwards. The
  server writes `launch`, waits for `initialized`, sends `configurationDone` and
  awaits the launch response. `lldb-dap` answers `launch` early; the response is
  still checked.

With `stop_on_entry=false`, the server briefly waits for the first thread or
process event and reports `running` (or `stopped`/`terminated` if the target
settled first). LLDB's process event supplies the process id, which is also its
thread id and seeds `debug_pause`.

## Backend differences

| Aspect | GDB 17.1 | LLDB 21.1.8 (`lldb-dap`) |
|---|---|---|
| Discovery | `gdb` on `PATH` | `lldb-dap` or versioned `lldb-dap-<N>`; handshake required |
| DAP sequence numbers | starts positive | all observed messages use `seq:0`; the client accepts non-negative values |
| `initialized` ordering | before `launch` | after `launch` |
| Launch response | after `configurationDone` | may arrive before `initialized` |
| Thread ids | small adapter ids | process ids |
| Frame ids | small adapter ids | large opaque integers |
| Stop-on-entry | `reason:"stopped"` | `reason:"exception"`, description `signal SIGSTOP` |
| Inferior tty | `/dev/null` | private PTY |
| Detach advertisement | `supportTerminateDebuggee=true` | `supportTerminateDebuggee=true` |
| Detach exposed | no | no |
| `supportsTerminateRequest` | true | false (not used) |
| Hardening verification | rejected `-ex` command fails startup | fixed settings; not reported back by older adapters |
| Event volume | moderate | module/capabilities/output and a large `terminated` statistics event |

Both backends report `stopped`, `continued`, `exited` and `terminated` events
through the same state machine. LLDB's `capabilities` event and module events are
drained and ignored. A stopped event is a debugger observation, not a
correctness conclusion. LLDB's stop-on-entry is a SIGSTOP exception, not a
source-level stop; set a breakpoint before continuing to reach source.

## Session states

| State | Meaning |
|---|---|
| `starting` | The adapter is being spawned and the DAP handshake is in progress. |
| `running` | A resume/pause request was accepted; the target may be executing. |
| `stopped` | The adapter reported a `stopped` event. |
| `terminated` | The debuggee ended; an exit code may be present. |
| `detached` | Reserved for a future verified detach; not produced at J6/J7. |
| `failed` | The transport or protocol failed, or the session was closed by the server. |

`stop_generation` increments on every `stopped` event. Frame ids, scope
references and variable references are valid only for the generation that
produced them; a resume invalidates them. `debug_status` exposes the latest stop
event and generation without contacting the adapter. Stack, scope and variable
requests accept an optional `stop_generation`; a stale value returns
`stale_reference`.

### Execution actions

`debug_continue` returns `accepted=true` without waiting for a stop.
`debug_step` and `debug_pause` wait up to 5 seconds for the next stopped event;
if it does not arrive, they return `accepted=true` with state `running` and a
warning. Callers poll `debug_status` until `stop_generation` changes. A resume
request that is accepted but whose response times out leaves the reported state
as `running`: the request may already have reached the adapter and cannot be
recalled.

`debug_stop` is idempotent. It returns `already_stopped=true` for an already
terminated session. `terminate` defaults to true. Neither backend exposes detach:
`terminate=false` returns `unsupported_capability` and leaves the session
running. Both adapters advertise `supportTerminateDebuggee`, but no detach path
has been proven to preserve the inferior (the GDB J6 test failed), so the server
fails closed for both.

## Tools

| Tool | Requires stopped | Notes |
|---|---|---|
| `debug_start` | — | Declared target, `backend` (`gdb` default or `lldb`), allowed args, `stop_on_entry` default true. |
| `debug_status` | — | Snapshot, no adapter request. |
| `debug_set_breakpoints` | yes | Workspace source only; up to 100 deduplicated lines. |
| `debug_continue` | yes | Asynchronous. |
| `debug_pause` | no (requires running) | Waits up to 5 s for the stop. |
| `debug_step` | yes | `next`, `in` or `out`; waits up to 5 s for the stop. |
| `debug_threads` | no | Bounded page of adapter threads. |
| `debug_stack_trace` | yes | At most 100 frames per page; optional `stop_generation`; `has_more`/`next_frame`. |
| `debug_scopes` | yes | Scope references for the current generation; optional `stop_generation`. |
| `debug_variables` | yes | At most 200 children and 2048 bytes per value; optional `stop_generation`; explicit truncation. |
| `debug_stop` | — | Terminate only; always available for a session. |

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
references are omitted. LLDB may report synthetic loader frames with unusual
source names; they are treated as external when outside the roots.

Missing or stripped symbols are reported with a warning. The server never
fabricates a function or source location.

## Retention and cleanup

- At most `limits.max_sessions` live sessions (1–32, default 2).
- Live sessions idle for 15 minutes are closed by the reaper.
- Terminal/detached records are retained for 5 minutes for `debug_status` and
  `debug_stop`, then evicted. The session map is also capped.
- Server shutdown closes all sessions and waits for debugger processes.
- `debug_stop`, idle eviction, server shutdown and startup-failure cleanup all
  send a best-effort `disconnect(terminateDebuggee=true)` before closing the DAP
  client and terminating the debugger process group. `debug_stop` reports the
  disconnect error; cleanup paths only rely on the subsequent group/PID checks.
- `debug_stop` releases the DAP client, terminates the debugger and removes the
  private session directory.
- A debuggee that ends on its own triggers a short delayed release; its session
  record remains queryable until the retention expiry.

## Limitations

- No attach, core dump, expression evaluation, data breakpoints, memory
  read/write or debugger command passthrough on either backend.
- Detach is not implemented in J6/J7. `terminate=false` is refused even though
  both adapters advertise `supportTerminateDebuggee`.
- Debuggee stdout/stderr are not surfaced. GDB uses `/dev/null`; LLDB uses a
  private PTY, which can block a debuggee that reads stdin. Neither shares the
  DAP pipe.
- GDB auto-load scripts (Python and GDB command scripts) and local `.gdbinit`
  are disabled for security. Language pretty-printers distributed as auto-load
  scripts are therefore unavailable; variables are shown with GDB's built-in
  formatting.
- LLDB pre-init hardening is expressed with fixed settings. `lldb-dap` does not
  reliably report an unknown setting, so older LLDB versions may silently lack a
  setting. LLDB 21.1.8 is the verified reference. The probe still requires a
  functional DAP handshake.
- `debug_continue` never waits for a stop; callers must poll `debug_status`.
  `debug_step` and `debug_pause` wait at most five seconds and may still return
  `running`, in which case polling is required.
- GDB can stop at `_start`; with stripped binaries, frames and breakpoints may
  have no source or symbols. The response marks this degradation but cannot
  restore missing debug information. LLDB's stop-on-entry is a SIGSTOP
  exception, not a source breakpoint.
- A target that calls `setsid` may escape process-group cleanup; system-level
  isolation remains the only robust containment.
- Session retention is in memory and is lost on server restart; J6/J7 do not
  persist debugger evidence.
- There is no maximum wall-clock session duration. A session kept active by
  status polling can run until `debug_stop` or server shutdown; the idle timeout
  only closes sessions without API activity. The live-session quota bounds the
  number of concurrent debuggees, not their total CPU or memory use.
- Adapter DAP implementations can differ across versions; the probe requires a
  successful `initialize`, and session capabilities are rechecked before use.
- Bounded adapter stderr excerpts and adapter error text may appear in an error
  message and can contain local absolute paths; they are not file contents, and
  stack/breakpoint source fields still normalize workspace paths and omit
  external ones.
- GDB 17.1 rejects `variables` start/count values above a child range, so the
  backend requests the child list and paginates locally. If one DAP response
  exceeds the 16 MiB frame limit, the call fails as a typed protocol error
  rather than returning silently truncated data. LLDB variables are handled by
  the same local pagination.

## Tests

```sh
go test ./internal/debugger
go test -race ./internal/debugger
go test ./internal/capabilities
go test ./tests/e2e -run 'TestServe(GDB|LLDB)DebugWorkflow'
```

Real GDB and LLDB tests require `gdb`, `lldb-dap` and `cc` and skip with a
documented reason when one is missing. They cover:

- stop-on-entry, verified breakpoints, continue, stack, scopes and locals for
  both backends;
- step and a new stop generation;
- running start, pause, threads and stack;
- stop while running and debuggee PID termination;
- stripped-symbol degraded evidence and no absolute source paths;
- explicit `unsupported_capability` for detach;
- MCP stdio end to end from `debug_start` to `debug_stop`;
- LLDB's SIGSTOP entry reason, process-id thread ids, versioned discovery, a
  cwd `.lldbinit` that must not execute, and stdin isolation (the inferior
  descriptor is not the DAP pipe).
