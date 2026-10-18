# Experimental perf and vgdb (J13)

These tools are opt-in and explicitly experimental. They are not required for
v1.0 and may be hidden on a host even when the binary is installed.

## Linux perf

Enable only with `"backends": {"perf": true}`. The capability registry is
revalidated periodically and the binary is rechecked at invocation; the full
permission probe is not re-executed on every request. `profile_perf` is published only
when:

- the `perf` binary is found at an absolute PATH entry;
- `perf_event_paranoid` and the effective capability set do not require
  privilege escalation;
- the fixed unprivileged permission probe succeeds;
- a declared target currently resolves under policy.

The command is fixed:

```text
perf stat -j -e task-clock,cycles,instructions -- <program> <args...>
```

The client cannot add events or perf flags. The server never changes
`perf_event_paranoid`, grants capabilities, uses sudo or retries with elevated
privileges. If the host is restricted (for example
`perf_event_paranoid=4` without `CAP_PERFMON`), the capability is unavailable
and `profile_perf` is hidden; `system_capabilities` reports the reason.

Results preserve bounded `stdout.log`/`stderr.log`, the fixed `-o` output
`perf-stat.json`, and a normalized `perf.json`. Unsupported or uncounted events
are warnings, never fabricated zeros. A multiplexed counter
(`pcnt-running < 100`) makes the report incomplete rather than presenting a
scaled estimate as exact. Hybrid CPUs that rename events such as
`cpu_core/cycles/` fail closed and are reported as incomplete.
`perf_artifact` provides bounded excerpts. Results marked `experimental=true`
are not a stable profiler contract and can be incomplete on counter failures.

## vgdb

Enable only with `"backends": {"vgdb": true}`. `vgdb_inspect` starts a declared
target under Valgrind with a private `--vgdb-prefix`, then sends exactly these
fixed read-only monitor commands:

1. `v.info gdbserver_status`
2. `v.info exectxt`
3. `v.info scheduler`

There is **no monitor-command field**, no free-form `vgdb` command and no GDB
console passthrough. Cleanup uses the fixed `v.kill` command and the job
manager's process-group cancellation. The private FIFOs are removed after the
run; raw Valgrind stdout/stderr and `vgdb.json` stay bounded in the run
directory. `vgdb_artifact` provides bounded excerpts.

Valgrind stops at startup (`--vgdb-error=0`), so the fixed commands inspect the
startup gdbserver state, not the target body's later execution. This is a
prototype, not a replacement for a debugger.

The tool is hidden unless both `backend.vgdb` and `backend.valgrind` are
available and a declared target resolves. It never changes Yama, ptrace or
Valgrind settings and never uses sudo.

## Current reference host

`perf` is installed but `kernel.perf_event_paranoid=4` and the process has no
`CAP_PERFMON`/`CAP_SYS_ADMIN`; therefore `backend.perf` is unavailable and
`profile_perf` is not exposed. No privilege escalation is attempted. `vgdb`
works through Valgrind 3.26 with the fixed commands above; the real test records
that scenario in `docs/compatibility.md`.
