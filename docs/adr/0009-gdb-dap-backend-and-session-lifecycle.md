# ADR 0009: GDB DAP backend and session lifecycle

- **Status:** accepted (J6)
- **Date:** 2026-10-18
- **Supersedes:** none
- **Related:** ADR 0008 (generic DAP transport), ADR 0002 (security model),
  ADR 0004 (capability probing), ADR 0005 (execution lifecycle)

## Context

J5 delivered a generic DAP client but no debugger. J6 must expose GDB debugging
without accepting arbitrary paths, PIDs, GDB flags, expressions, memory reads or
scripts. The implementation must handle GDB-specific protocol behavior while
keeping the MCP surface safe and bounded.

Three GDB behaviors shaped the design:

1. GDB does not answer `launch` until `configurationDone` has been received, but
   it dispatches requests concurrently and can transiently reject
   `configurationDone` before the launch state exists.
2. GDB does not reliably emit a `continued` event after a resume. Relying on the
   DAP client's state alone would leave a session reporting `stopped` while the
   debuggee runs.
3. `stopOnEntry` stops at the process entry point, often in the dynamic loader,
   not in `main`. Source and symbols may be unavailable there.

GDB 17.1 advertises `supportTerminateDebuggee=true`, but a real detach test
showed that releasing GDB did not reliably preserve the inferior. Detach is
therefore not exposed at J6; termination never depends on that capability.

## Decisions

### Fixed, hardened GDB command line

The server builds a fixed argument vector. Clients cannot add GDB flags, init
files, commands, Python scripts or expressions. GDB runs with:

- `--quiet --nx --interpreter=dap`;
- auto-load Python scripts, GDB command scripts and local `.gdbinit` disabled;
- debuginfod disabled (no network symbol fetch);
- `startup-with-shell off`;
- history saving off;
- a private `HOME`/`TMPDIR` and a `/dev/null` inferior tty;
- a minimal environment and a dedicated process group.

The inferior tty choice prevents the debuggee from reading or writing the DAP
pipe. Debuggee terminal I/O is deliberately not exposed at J6.

### Async request future and bounded start ordering

`internal/dap` gains `RequestAsync`, which writes a request and returns a future.
The GDB backend writes `launch`, sends `configurationDone`, then awaits the
launch response. Only the transient "launch or attach not specified" response
is retried, with a short bounded backoff. This is adapter-specific ordering, not
a general DAP assumption.

### Server-owned execution state

The backend maintains its own state and `stop_generation` instead of trusting the
DAP client's state transitions. A successful resume request pre-sets `running`;
a `stopped` event increments the generation and records the stop. This handles
GDB's missing `continued` events. A failed or timed-out resume restores
`stopped` only if no new stop generation arrived; a written request that cannot
be recalled leaves the session honestly `running`. `debug_step` and `debug_pause`
wait up to five seconds for the next stop; `debug_continue` never blocks.

References are scoped to one generation. A resume invalidates frame, scope and
variable references and moves to a new generation.

### Explicit termination and detach

`debug_stop(terminate=true)` is the default. It sends
`disconnect(terminateDebuggee=true)` and terminates the GDB process group
(TERM then KILL). GDB 17.1 puts the inferior in its own process group, so
debuggee termination relies on GDB's teardown/ptrace behavior; this is verified
with a fixture PID. `terminate=false` is refused with
`unsupported_capability`: although GDB 17.1 advertises support, a real detach
test did not reliably preserve the inferior. The session remains running so the
caller can terminate it explicitly. Stop is idempotent.

### Session manager with quotas and retention

A backend-neutral manager owns opaque `dbg_<32 hex>` ids, the live-session quota
(`limits.max_sessions`), a 15-minute idle timeout, a 5-minute terminal retention
and shutdown cleanup. Terminal sessions do not consume a live slot. The manager
runs a periodic reaper and also reaps on access.

### Capability-gated tools and retained management

`debug_start`, breakpoints, resume/pause/step, threads, stack, scopes and
variables are published only while `backend.gdb` is available; `debug_start` also
requires a declared target. `debug_status` and `debug_stop` may remain published
for a retained session even if the capability disappears, so cleanup is always
possible. Backend availability is revalidated when starting a session.

### Workspace-only sources and normalized output

Breakpoint sources use the existing policy resolver: relative to the target cwd,
symlink-resolved, regular-file and contained in a workspace root. Workspace
sources are returned relative to the root. External source paths, instruction
pointers, module paths and memory references are omitted; missing symbols are an
explicit warning rather than invented locations.

### Bounded output

Threads, frames and variables are paged. Frame count is capped at 100, variables
at 200 per page, values at 2048 bytes on a UTF-8 boundary, and scopes at 64 with
an explicit warning. Request, startup and stop timeouts are 10/15/5 seconds.

## Consequences

- The GDB surface is usable end to end and tested against real GDB 17.1.
- The tool list is conditional and remains safe if GDB disappears mid-session.
- GDB launch ordering is handled explicitly and is covered by tests.
- Debuggee output is unavailable at J6; agents must use breakpoints and variable
  inspection. This avoids mixing inferior I/O with the DAP protocol.
- Debug sessions are in-memory only; a server restart loses session records.
- At J6, attach, core dumps, evaluation, data breakpoints and LLDB remain later work; J7 adds LLDB in ADR 0010.
- GDB DAP differences across versions still require the capability probe and the
  per-session capability checks; the J6 behavior is verified only against GDB
  17.1 on Ubuntu 26.04.
