# Security policy

## Supported versions

The project has not published a release yet. Security fixes are applied to the
`main` branch; no version is currently supported for production use.

## Reporting a vulnerability

Please do **not** open a public issue for a suspected vulnerability.

Use GitHub's private vulnerability reporting on this repository
("Security" tab → "Report a vulnerability"). Include:

- affected commit or version;
- a minimal reproduction (configuration, target, request);
- the observed impact and the expected behaviour;
- whether the issue requires a malicious MCP client, a malicious target binary,
  or only a local misconfiguration.

You will receive an acknowledgement and a remediation plan. Please allow time for
a fix before public disclosure.

## Current surface (J4/J5)

- `serve` starts an MCP server over stdin/stdout. stdout carries only JSON-RPC
  messages; logs and errors go to stderr.
- `system_capabilities` is read-only. It reports build identity, policy, limits,
  backend state, exposed tools and warnings. It never launches a program.
- `analysis_start` launches one allowlisted target under Valgrind. The client
  sends a configured `target_id`, never a path or shell string. Only the
  allowlisted Memcheck option surface is accepted; `trace_children=true` is
  rejected.
- `analysis_status`, `analysis_report` and `analysis_artifact` are read-only and
  never launch a program. `analysis_cancel` only signals a run that this server
  process owns or acknowledges that a persisted run is not active.
- `doctor` and `validate-config` are local, read-only commands. `doctor` uses
  `stat` for path checks and never executes a target.
- Capability probes run at startup and every 30 seconds. They resolve enabled
  backend executables with `PATH`, run `--version`, and for Valgrind additionally
  run `--tool=memcheck --help` to verify Memcheck/XML support. They are bounded
  (2-second timeout, bounded output, minimal environment), never run a user
  program and never use sudo. Relative `PATH` entries are rejected. `lldb-dap`
  is resolved but not executed.
- `analysis_start` is published only while `backend.valgrind` is `available`.
  Backend availability is revalidated at invocation and the Valgrind binary is
  revalidated immediately before each run. The evidence tools stay published so
  an in-flight or persisted run remains queryable if the backend disappears.
- The runner revalidates targets with `EvalSymlinks`, confines program and
  working directory to resolved workspace roots, uses process groups with
  TERM/KILL cleanup, does not inherit the server environment or stdin, and
  bounds stdout/stderr by a shared byte budget. A per-request timeout can only
  shorten the configured maximum.
- Valgrind XML is untrusted: it is parsed token by token with bounds on bytes,
  depth, diagnostics, frames and string lengths. Truncation, malformed input or
  quota breaches produce `incomplete`/`tool_error`, never a clean result. Raw XML
  is adopted with `O_NOFOLLOW`, checked as a regular file, truncated to the run
  quota and chmodded `0600`.
- Persisted `analysis.json` is treated as untrusted input. The API validates its
  schema version, run id, diagnostics, fingerprints, severities and summary
  consistency; a mismatch is `report_invalid`, never an empty clean result.
- `analysis_report` pages are bounded by items and bytes. Oversized diagnostics
  are explicitly omitted and unreadable ones explicitly counted; the summary
  remains report-wide. A decoded-report cache is bounded to 16 MiB per report
  and 64 MiB total.
- `analysis_artifact` accepts only logical artifact types. Run ids must match
  `^run_[0-9a-f]{32}$`; the run directory must be a real directory; files are
  opened with `O_NOFOLLOW|O_NONBLOCK`, must be regular files, and are read as
  bounded excerpts. No absolute path is returned and arbitrary files cannot be
  read. FIFO and symlink plants are rejected without blocking.
- Suppression profiles are configuration-defined, confined to workspace roots,
  capped at 1 MiB and revalidated with `EvalSymlinks`/`O_NOFOLLOW` at invocation.
  Suppressed errors are reported as counts and are never called resolved.
- `analysis_cancel` never turns partial evidence into a successful "no errors"
  result; cancellation, timeout and parse failure all keep
  `analysis_complete=false`.
- J5 adds an internal generic DAP client (`internal/dap`) for J6/J7. It launches
  no debugger and adds no MCP tool. Framing is bounded before allocation
  (64 KiB header, 16 MiB frame), `Content-Length` is a byte count, and malformed
  or truncated frames are typed protocol failures.
- The DAP client has exactly one reader and one bounded writer queue. Pending
  request futures, events and reverse handlers are explicitly bounded; a slow
  event consumer applies backpressure. Late responses are ignored and counted,
  and EOF/Close unblock pending requests.
- Adapter-initiated reverse requests are rejected by default. A configured
  handler runs with bounded concurrency, is panic-safe and receives a context
  canceled by Close; it does not grant filesystem, process or network access by
  itself.
- The J5 mock adapter (`internal/dap/daptest`) is test-only and implements its
  own framing to exercise a second decoder.

## Threat model

The server is a **local stdio MCP server**. It does not authenticate its caller:
the security boundary is the trust placed in the agent/host and the permissions of
the operating-system user that runs the server.

Explicit assumptions and limits:

1. **Allowlisted directories are not a sandbox.** Path validation prevents
   accidental escape and path traversal, not code execution by a malicious binary.
2. **An allowlisted target is still untrusted code.** Run untrusted programs in a
   separate container, VM or user account. Docker may require extra ptrace
   permissions; `--privileged` is never a recommended default.
3. **The MCP client is trusted.** A compromised agent can request every operation
   that the local policy allows.
4. **Raw artifacts may contain sensitive data** (memory strings, process arguments,
   paths, addresses). They are bounded and their access is limited to the opaque
   run id, but they are not automatically redacted. The stdio transport has no
   caller authentication and the opaque run id is the only capability for
   persisted evidence.
5. **No network service exists.** The MVP serves MCP over stdio only; there is no
   HTTP transport, telemetry or upload.
6. **Policy checks are not atomic with execution.** A target program or working
   directory that the server's user can replace inside a writable workspace root
   may be swapped between path validation and launch. Keep workspace roots
   writable only by trusted users, or isolate execution at the system level.

## Design commitments

These are non-negotiable for every milestone:

- Programs are started with `os/exec` and argument arrays. There is no free-form
  shell (`sh -c`, `bash -c`) and no client-supplied command string.
- Requests reference targets declared in configuration by identifier; clients do
  not submit arbitrary executable paths. Targets are revalidated at execution
  time, including symlink resolution and resolved-root containment.
- The target environment is constructed from an allowlist; the server environment
  and stdin are never inherited.
- Child processes run in their own process group. Timeouts and cancellation send
  TERM, then KILL after a grace period; remaining group members are killed after
  the direct child is reaped. Detached children that call `setsid` can still
  escape this cleanup, and a hard server crash may leave children behind.
- No arbitrary PID attach, no raw memory read, no generic GDB `evaluate`, and no
  debugger command passthrough in the default profile.
- A tool appears in `tools/list` only when it is implemented, enabled by
  configuration, permitted by policy and usable on the machine. Availability is
  revalidated at invocation time.
- Long operations honour `context.Context`, deadlines, cancellation and process
  cleanup. Outputs and artifacts are bounded and explicitly marked when truncated.
- DAP frames are validated and bounded before allocation; a response for an
  unknown or expired request future is dropped, and a cancelled request never
  becomes a successful response.
- Jobs get private artifact directories (`0700`) with cryptographically random
  names, opened with `O_NOFOLLOW`; writes are atomic and bounded by per-run byte
  and file-count quotas. Reads revalidate the run id, reject symlinked run
  directories and non-regular files, and use `O_NOFOLLOW|O_NONBLOCK`. Access is
  scoped to the owning session in later milestones.
- A program exit code is tracked separately from the job state. A timeout, kill
  or infrastructure failure never becomes a successful "no errors" result. The
  `--error-exitcode=42` marker is never treated as proof on its own; XML is
  authoritative.
- Analysis completeness is explicit. A timeout, kill, parse failure or quota
  breach sets `analysis_complete=false`; `total=0` with an incomplete analysis
  never means "no bug".
- stdout is reserved for the MCP protocol when serving; diagnostics and logs go
  to stderr.
- The server never runs `sudo`, never changes `kernel.yama.ptrace_scope`,
  `perf_event_paranoid`, AppArmor or other security controls, and never escalates
  privileges. Missing capabilities are reported as such.
- No automatic code modification, no automatic build of arbitrary repositories,
  no downloaded-and-executed scripts.

## Hardening recommendations

- Run the server as an unprivileged user dedicated to development work.
- Run it with a trusted `PATH`: startup probes execute the first **absolute**
  `gdb`/`valgrind` found there. Relative `PATH` entries are rejected, but a
  writable absolute directory still lets a planted binary run during probing.
- Keep `workspace_roots` as narrow as possible and avoid home directories.
- Prefer the `safe-default` profile; opt in to elevated features explicitly.
- Treat `perf`/ptrace restrictions as expected conditions, not bugs to bypass.
- Review the raw artifacts retention policy before collecting large reports.
  J2–J5 enforce per-run quotas, bounded API responses and bounded DAP buffers
  only; global retention and disk quotas arrive later.
- A target runs as the same OS user as the server and can inspect or modify its
  run directory, including the raw XML, between writer and parser. The pipeline
  detects symlink replacement and bounds the file, but it cannot prove evidence
  integrity against the target. Use a separate account or sandbox for untrusted
  code.
- Use a container or cgroup when the target may escape its process group
  (`setsid`) or must be isolated beyond a bounded subprocess.
- Do not expose the stdio transport through a network wrapper without adding
  authentication and authorization yourself.

## Out of scope

- Kernel debugging, embedded or GPU targets, remote debugging.
- Multi-tenant isolation and remote authentication.
- Proving the absence of bugs in analysed programs.
- Protecting the host from a deliberately malicious allowlisted binary.
