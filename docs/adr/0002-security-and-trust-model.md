# ADR 0002 — Security and trust model

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J0 (binding for all milestones)

## Context

The server launches native programs, collects their memory-related diagnostics and
may drive debuggers. Both the agent and the analysed binaries can be untrusted.
The MCP transport is local stdio and does not authenticate the caller. Path
restriction prevents mistakes and traversal but cannot contain code execution.

## Decision

### Trust profiles

| Profile | Allowed |
|---|---|
| `safe-default` | Declared targets under `workspace_roots`; no attach, no expression evaluation, no privileged profiler, no server-side network. |
| `trusted-local` | Validated argument profiles, CTest on an approved build directory, optional core-dump access. |
| `elevated-local` (v0.3+) | Explicit opt-in for attach, raw memory investigation, evaluation, `perf`, `vgdb`; journaled and confirmed. |

The MVP implements the `safe-default` subset. Elevated features are not merely
hidden: they are rejected by policy.

### Execution

- `os/exec` with argument arrays; no shell, no interpolation, no client-supplied
  command string or executable path.
- Requests reference `target_id` values declared in configuration. Program
  arguments are constrained by per-target allowed profiles or bounded validation.
- Child processes run in their own process group; cancellation sends TERM then
  KILL after a grace period. Detached children may survive process-group cleanup;
  this limitation must be documented rather than hidden.
- Global timeout, bounded concurrent jobs, bounded stdout/stderr with disk spool
  and explicit truncation. No unbounded in-memory buffers.
- Artifact directories are created atomically with `0700` permissions under a
  fixed root and cryptographically random opaque names.

### Data

- Raw artifacts are preserved and referenced; normalized diagnostics are derived
  data. `tool_reported` and inference are never mixed.
- Artifacts are scoped to their owning session/project; cross-session access is
  rejected even when a caller guesses an identifier.
- Logs go to stderr via `slog`, with secret and environment filtering.
- No telemetry, upload or network access without an explicit future decision.

### Privileges

The server never runs `sudo`, never changes `kernel.yama.ptrace_scope`,
`perf_event_paranoid`, AppArmor, seccomp or cgroups to make a feature work. When a
capability is denied by the kernel or policy, the corresponding tool is
`unavailable` with a reason. CI does not install privileges or weaken controls.

### Honesty of results

`analysis_complete=false` on timeout, kill, parse failure, missing artifact,
unavailable tool, unexplained exit code or quota breach. A zero diagnostic count
with an incomplete analysis is never reported as "no errors". Failed symbolization
is reported as such and never converted into a source defect.

## Consequences

- Some features are unavailable on default Ubuntu configurations (for example
  `perf` with `perf_event_paranoid >= 3`). This is expected and documented.
- Tests must cover policy denials, path traversal, symlink escape, artifact
  isolation, truncation, process cleanup and cancellation.
- Users who need stronger isolation must provide a container or VM; the server
  does not claim to be one.
