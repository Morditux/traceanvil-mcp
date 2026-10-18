# ADR 0005 — Process execution, artifacts and job lifecycle

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J2

## Context

J2 adds the internal execution foundation that will back `analysis_start` in J4.
It must run an allowlisted native program with bounded resources, terminate a
blocked program reliably, preserve evidence in a private artifact directory and
expose a consistent job state. No MCP tool launches a program yet.

## Decision

### Policy resolution

- Configuration validation stays lexical. `policy.Resolver.Resolve` revalidates
  immediately before launch: workspace roots, program and working directory are
  resolved with `EvalSymlinks`, and both resolved paths must remain inside a
  resolved root.
- The program must be an absolute regular file with an execute bit; the working
  directory must be an absolute directory.
- Requested arguments are a multiset subset of the target's `allowed_args`.
  Unknown, repeated-beyond-declared-count, NUL-containing or oversized arguments
  are denied. Empty `allowed_args` permits no arguments.
- Denials use a typed `policy.DeniedError`; they never fall back to a different
  target.
- The target environment is built from a fixed allowlist (`PATH`, `LANG`,
  `LC_ALL`, `TMPDIR`). `os.Environ` is never inherited, so server secrets do not
  reach the target.
- Symlink swapping between resolution and `exec` remains possible; the runner has
  no cgroup/container isolation. This is documented as a local-trust limitation,
  not a sandbox.

### Runner

- `exec.Command` with an argument array; never a shell, command string or
  client-supplied path.
- Executable and working directory must be absolute. Arguments containing NUL
  are rejected.
- Each child gets its own process group (`Setpgid`). `Pdeathsig` is not used
  because Go documents that it fires on parent-thread termination, which may
  happen while the server process is still alive. Process-group cleanup covers
  normal exits and cancellations; a hard server crash may leave detached
  children (documented limitation).
- The server never connects its stdin to the child (`Stdin=nil`, `/dev/null`).
- Cancellation and timeout send `SIGTERM` to the group, then `SIGKILL` after a
  grace period. After `Wait`, any remaining group members are killed as well.
- `WaitDelay` bounds the wait when a grandchild keeps the stdout/stderr pipes
  open; this sets `OutputIncomplete` instead of hanging forever.
- stdout and stderr share one byte budget. Excess bytes are discarded, the
  corresponding stream is marked truncated, and the child is never blocked by a
  full pipe. Output goes to caller-provided writers (artifact files in jobs).
- A non-zero exit code or signal is a program result. Start, capture or wait
  failures are infrastructure errors. Timeouts and cancellations are returned as
  result flags, not errors.
- Remaining limitations: a child that calls `setsid` can escape the process
  group; a process in uninterruptible sleep cannot be killed; cgroups v2 are not
  used yet.

### Artifacts

- The store root comes from configuration. Each run gets an opaque
  `run_<32 hex>` identifier from `crypto/rand` and an atomically created `0700`
  directory.
- File names match a restricted pattern; separators, `.`/`..`, absolute names
  and control characters are rejected.
- Files are opened with `O_NOFOLLOW` and `O_EXCL` where appropriate. Atomic
  writes use a random temporary name, `fsync`, and `rename` in the same
  directory.
- Per-run quotas: bytes (default 10 MiB) and file count (default 64). A quota
  breach is a typed error, not silent truncation for non-streaming writes.
- stdout/stderr are streamed through the byte quota; the job manager reserves
  64 KiB below the artifact quota for `result.json`.
- There is no global retention or total-disk quota yet; that is J14 work. The
  job map retains completed jobs for status/report access.

### Jobs

- States: `queued`, `running`, `completed`, `cancelled`, `failed`, `timed_out`.
- `Manager.Start` enforces `max_jobs`, creates the artifact directory, and runs
  the job asynchronously. If the caller did not set `TMPDIR`, the manager points
  it at the private run directory. The concurrency slot is released before the
  job's `done` channel closes, so `Wait` returning implies a free slot.
- `result.json` contains `schema_version`, `run_id`, `target_id`, state,
  timestamps, program result (exit code, signal, timeout/cancellation, output
  counters/truncation) and infrastructure error. It is written atomically before
  the job is marked finished.
- A program exit code is stored separately from the job state: a program that
  exits non-zero yields `completed` with that exit code; only runner failures,
  cancellation and timeout change the job state.
- Output logs are `stdout.log` and `stderr.log`. They are readable after success
  and after infrastructure failure.

## Consequences

- J2 exposes no new MCP tool; the runner is only reachable from internal tests
  until J4 wires `analysis_start` and target resolution.
- Shell execution, environment inheritance and unbounded output are now blocked
  by construction and covered by tests.
- Process leaks, blocked programs, pipe-holding grandchildren, symlink escapes
  and artifact traversal each have a negative test.
- cgroup isolation, global retention and multi-session artifact authorization
  remain future work.
