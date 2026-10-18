# ADR 0016 — Experimental perf and vgdb surfaces

- **Status:** Accepted, experimental
- **Date:** 2026-10-18
- **Milestone:** J13

## Context

J13 explores Linux `perf` and Valgrind's `vgdb` gdbserver. These features depend
on host permissions and are not required for v1.0. The project must never
escalate privileges, change security sysctls, or expose a free monitor command
surface. Any result must be explicitly marked experimental.

## Decision

### perf

- Add an opt-in `backends.perf` switch (default false).
- `backend.perf` requires the perf binary, an absolute path, a successful
  unprivileged permission probe, and a target currently usable under policy.
  When `perf_event_paranoid >= 3` and neither `CAP_PERFMON` nor `CAP_SYS_ADMIN`
  is effective, the capability is unavailable with a documented reason.
- `profile_perf` executes only:
  `perf stat -j -e task-clock,cycles,instructions -- <target>`.
  The client cannot add events or flags. The server never changes
  `perf_event_paranoid`, uses sudo, grants capabilities or retries with
  elevation.
- Preserve bounded `stdout.log`/`stderr.log` and a normalized `perf.json`;
  unsupported counters are warnings, not invented values. `experimental=true`
  and incomplete evidence remain explicit.

### vgdb

- Add an opt-in `backends.vgdb` switch (default false).
- `vgdb_inspect` starts a declared target under Valgrind with
  `--vgdb=yes --vgdb-error=0` and a private run-directory prefix, then sends
  only these fixed read-only commands:
  `v.info gdbserver_status`, `v.info exectxt`, `v.info scheduler`.
- There is no request field for a monitor command, no free-form `vgdb` command
  and no GDB console. Cleanup uses fixed `v.kill` followed by the job manager's
  process-group cancellation; private FIFOs are removed.
- The tool requires both `backend.vgdb` and `backend.valgrind`, a usable
  declared target and the normal timeout/artifact bounds. It never changes
  Yama, ptrace or Valgrind settings and never uses sudo.

## Consequences

- On restricted hosts `profile_perf` is simply absent; the reason remains
  visible in `system_capabilities`. No test relies on privileged success.
- vgdb output is a bounded startup inspection, not a debugger replacement and
  not a stable v1.0 API. Its real fixture test is the demonstrated scenario.
- Both surfaces are opt-in and can be removed or promoted by a later ADR.
