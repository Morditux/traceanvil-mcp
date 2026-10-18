# ADR 0010 — LLDB backend through lldb-dap

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J7

## Context

J6 delivered a GDB backend over the generic DAP client, a typed session manager
and a bounded MCP debug API. J7 must add LLDB with the same common surface while
preserving explicit evidence and failing closed when a capability is missing.
The reference machine provides `lldb-dap` 21.1.8 through both `/usr/bin/lldb-dap`
and `/usr/bin/lldb-dap-21`.

`lldb-dap` differs from GDB's DAP implementation in observable ways:

- it uses `seq:0` for its DAP messages;
- it accepts `launch` before sending `initialized`, and may answer launch first;
- thread ids are process ids and frame ids are large opaque integers;
- stop-on-entry is a SIGSTOP exception, not a `stopped` reason;
- its inferior uses a private PTY rather than `/dev/null`;
- its `terminated` event can contain a large `$__lldb_statistics` body;
- it supports `supportTerminateDebuggee`, but real detach behavior could not be
  trusted in J6.

Versioned adapter names must be discovered without executing a user program or
assuming that `lldb-dap --version` terminates on every version.

## Decision

### Discovery and capability

- Discover `lldb-dap` and `lldb-dap-<N>` only in absolute `PATH` directories.
  Prefer the unsuffixed name, then try versioned candidates.
- A candidate becomes `backend.lldb = available` only after a real DAP
  `initialize` handshake with the session's fixed arguments.
- `--version` is optional; the initialize handshake is the functional proof.
- The selected absolute path and version are recorded in the capability and
  revalidated immediately before each session.

### Shared session implementation

- Reuse the J6 DAP client, session manager, policy checks, pagination, error
  model and MCP tools.
- Keep adapter differences in a small backend specification rather than
  forking a second session implementation:
  - adapter id;
  - fixed process args;
  - initialized-before/after-launch order;
  - process-event thread seeding.
- LLDB startup: `initialize` → `launch` → `initialized` →
  `configurationDone` → await launch. GDB keeps its J6 order.

### Security

- Run `lldb-dap` with fixed `-c` settings disabling cwd `.lldbinit` loading and
  external/debuginfod symbol lookup.
- Use a private `0700` `HOME`/`TMPDIR` so user init files are unreachable; pass a
  minimal environment.
- Never accept an LLDB command, Python script, expression or init file from the
  client. The adapter process arguments are server-fixed.
- The inferior gets a private PTY, not the DAP pipe. Debuggee stdio remains
  unexposed over MCP.
- Detach remains unimplemented in J7. `terminate=false` returns
  `unsupported_capability`; both adapters advertise `supportTerminateDebuggee`,
  but no detach test proved inferior preservation, and the GDB J6 test failed.
  Termination uses `disconnect(terminateDebuggee=true)` plus process-group
  cleanup and a fixture PID check.

### Unsupported capabilities

- The common MCP surface does not request LLDB data breakpoints, evaluate,
  restart, memory or module requests.
- If a required common command is rejected by the adapter, it maps to a typed
  `unsupported_capability` error rather than a silent fallback.
- Backend-specific ordering, thread ids, stop reasons and terminal behavior are
  documented in `docs/debugger.md` and `docs/compatibility.md`.

## Consequences

- One MCP contract and one session state machine cover GDB and LLDB.
- LLDB sessions can be discovered and exercised without installing or probing
  unrelated tools.
- Backend differences remain explicit in status and documentation; no lowest
  common denominator hides them.
- Older LLDB versions may silently ignore an unknown pre-init setting because
  `lldb-dap` does not report it. This is documented; the verified reference is
  LLDB 21.1.8. A future adapter-level setting validation requires a safe
  introspection mechanism.
- Detach is still deferred; a future ADR must prove inferior preservation per
  adapter before exposing it.
- A second real adapter now exercises the DAP client's zero-sequence,
  early-launch-response and large-event paths.
