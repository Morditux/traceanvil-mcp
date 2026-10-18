# Core dumps and controlled attach (J11)

## Core dumps

Core analysis is profile-based. The administrator binds one executable and one
core file inside `workspace_roots`:

```json
{
  "backends": {"gdb": true},
  "cores": [
    {
      "id": "crash",
      "executable": "/workspace/build/fixture",
      "core": "/workspace/crashes/core.1234"
    }
  ]
}
```

The configured executable and core must be owned by the server user and must not
be group/world writable. Their identity (device/inode/owner/mode/size/mtime) is
rechecked after GDB runs; a change makes the report incomplete.

`core_analyze` accepts only `profile_id`. It runs GDB with fixed batch flags and
these fixed operations:

- `info threads`;
- `thread apply all bt 32`;
- the configured core and executable.

There is no client-provided debugger command, expression, path or flag. GDB
starts with `--nx`, pagination/confirmation/debuginfod and auto-load scripts
disabled. Output is bounded to 4 MiB during collection and 64 KiB in the MCP
response. The full 4 MiB-bounded GDB output is preserved as `core-gdb.log` and
referenced by `evidence`; `core_artifact` returns bounded excerpts. A
missing/mismatched core, a nonzero GDB exit, truncated output, degraded GDB
markers, no parsed threads or output that does not confirm a loaded core makes
the report `complete=false`. Raw paths are returned only from the declared
profile.

No `sysctl`, core-pattern change or sudo is attempted.

## Controlled attach

`debug_attach` currently uses the verified GDB attach path; LLDB attach is not
advertised. It requires all of the following:

1. `policy.profile = elevated-local`;
2. `policy.attach = true`;
3. the exact PID is in `policy.attach_allowed_pids`.

At invocation the server rechecks that the process:

- belongs to the server's UID;
- is not dead, zombie or already traced;
- has an executable and cwd inside `workspace_roots`;
- satisfies Yama `ptrace_scope`.

With `ptrace_scope=1` the process must be a descendant of the server and the
kernel must accept the adapter as tracer; a target may therefore need
`PR_SET_PTRACER`. The server verifies `TracerPid` after attach and fails closed
if no adapter tracer is present. Scopes 2/3 reject attach. The server never
changes Yama or uses sudo. An empty allowlist means attach is denied by default
and `debug_attach` is not published.

Attached sessions are normal debugger sessions. `debug_stop` defaults to
**detach** for an attached session and preserves the target. `terminate=true`
terminates it only when `policy.attach_terminate=true`; otherwise the kill
request is refused. Launched sessions keep their existing terminate
default and still refuse detach.

## Limitations

- A hostile process running as the same user is outside the isolation model.
- PID allowlists are ephemeral; a recycled PID is revalidated for ownership,
  executable containment and Yama before attach.
- Core analysis depends on GDB's ability to read the core and matching symbols.
  Missing/truncated evidence is never reported as a complete analysis.
- Attach detach behaviour is adapter and kernel dependent; the child fixture
  test verifies GDB preserves a directly owned target when Yama allows it.
