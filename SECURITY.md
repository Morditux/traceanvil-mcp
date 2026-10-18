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

## Current surface (J1)

- `serve` starts an MCP server over stdin/stdout. stdout carries only JSON-RPC
  messages; logs and errors go to stderr.
- `system_capabilities` is read-only. It reports build identity, policy, limits,
  backend state, exposed tools and warnings. It never launches a program.
- `doctor` and `validate-config` are local, read-only commands. `doctor` uses
  `stat` for path checks and never executes a target.
- Startup probes resolve enabled backend executables with `PATH` and run
  `--version` only, with a 2-second timeout, bounded output and a minimal
  environment. Relative `PATH` entries are rejected. `lldb-dap` is resolved but
  not executed. No user program is run.
- No MCP tool launches a target yet. J2 provides an internal runner
  (`policy` → `runner` → `artifacts` → `jobs`) that is exercised by tests but is
  not reachable through MCP until `analysis_start` is wired in J4.
- The internal runner revalidates targets with `EvalSymlinks`, confines program
  and working directory to resolved workspace roots, uses process groups with
  TERM/KILL cleanup, does not inherit the server environment or stdin, and
  bounds stdout/stderr by a shared byte budget.

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
   paths, addresses). They are bounded and access-controlled, not automatically
   redacted.
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
- Jobs get private artifact directories (`0700`) with cryptographically random
  names, opened with `O_NOFOLLOW`; writes are atomic and bounded by per-run byte
  and file-count quotas. Access is scoped to the owning session in later
  milestones.
- A program exit code is tracked separately from the job state. A timeout, kill
  or infrastructure failure never becomes a successful "no errors" result.
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
  J2 enforces per-run quotas only; global retention and disk quotas arrive
  later.
- Use a container or cgroup when the target may escape its process group
  (`setsid`) or must be isolated beyond a bounded subprocess.
- Do not expose the stdio transport through a network wrapper without adding
  authentication and authorization yourself.

## Out of scope

- Kernel debugging, embedded or GPU targets, remote debugging.
- Multi-tenant isolation and remote authentication.
- Proving the absence of bugs in analysed programs.
- Protecting the host from a deliberately malicious allowlisted binary.
