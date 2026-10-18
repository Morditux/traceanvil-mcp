# ADR 0014 — Core dumps and controlled attach

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J11

## Context

J11 adds two elevated-risk capabilities: opening arbitrary core dumps and
attaching to live processes. Both must fail closed by default, must not change
kernel ptrace policy or use sudo, and must preserve local process ownership and
cleanup. Debugger launch already has a fixed GDB/LLDB DAP path; J11 adds only
the minimum new surfaces.

## Decision

### Core dumps

- Add configured `cores[]` profiles binding an exact executable and core file.
  Both paths are absolute, inside `workspace_roots`, and revalidated at
  invocation. The client supplies only `profile_id`.
- `core_analyze` runs GDB with `--quiet --nx --nh --batch`, early `-iex`
  hardening settings and the fixed operations `info threads` and
  `thread apply all bt 32`.
  `-c <core>` and the executable are passed as separate argv elements.
- Bound collection at 4 MiB and the MCP output at 64 KiB. Nonzero exit,
  timeout/cancel, truncated output, no parsed threads or output that does not
  confirm a loaded core makes `complete=false`.
- No sysctl, core-pattern, sudo or build operation is performed. Raw core
  analysis passes only through the GDB command; no arbitrary debugger commands
  are exposed.

### Controlled attach

- Add `policy.attach_allowed_pids`, requiring `policy.attach=true` and
  `elevated-local`. Empty list means every attach is denied and the tool is not
  published.
- Revalidate the exact PID and process state before starting DAP: same
  real/effective/saved UID, live non-zombie, not already traced, executable and
  cwd inside roots.
- Respect Yama: scope 1 requires a descendant of the server; scope 2/3 is
  refused; scope 0 allows the explicit PID. The server never changes
  `ptrace_scope`.
- Extend the shared DAP session to send `attach` with the validated PID and
  executable. J11 advertises only the verified GDB attach path; LLDB attach
  remains unadvertised. Attached sessions set `attached=true` in status. `debug_stop`
  defaults to detach for attached sessions (disconnect `terminateDebuggee=false`)
  and preserves the target; explicit `terminate=true` may kill it only when
  `policy.attach_terminate=true`. Launched
  sessions retain their terminate default and continue to refuse unproven
  detach.
- Attach sessions use the same session manager, quotas, source/bp/stack/variable
  tools and cleanup paths as launched sessions.

## Consequences

- Core and attach are explicit administrator trust decisions, never inferred
  from a filename or client path.
- Default refusal and Yama behavior are testable and unchanged by the server.
- PID allowlists are explicit and ephemeral; PID reuse is revalidated before
  every attach.
- A hostile same-user process is outside the isolation model. Detach semantics
  depend on the adapter/kernel but are tested for the GDB reference path.
