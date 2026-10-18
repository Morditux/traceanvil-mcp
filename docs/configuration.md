# Configuration reference

The server reads one strict JSON document. Unknown fields, trailing values and
an empty file are rejected. The file must be a regular file of at most
**1 MiB**. `schema_version` must be `1`.

```sh
mcp-native-diagnostics validate-config --config config.local.json
mcp-native-diagnostics doctor --config config.local.json
mcp-native-diagnostics serve --config config.local.json
```

`validate-config` checks the document only. `doctor` additionally stats
configured paths and probes backends, but **never executes a user program** and
never modifies the machine.

## Root object

| Field | Type | Required | Rules |
|---|---|---|---|
| `schema_version` | integer | yes | Must be `1`. |
| `workspace_roots` | array of strings | yes | 1 to 32 absolute paths. |
| `report_dir` | string | yes | Absolute directory for per-run artifacts. |
| `allowed_targets` | array of objects | no | At most 128 targets. |
| `backends` | object | no | Missing booleans default to `false`. |
| `policy` | object | no | Defaults to `safe-default`, no elevated permission. |
| `limits` | object | no | Missing limits are replaced by defaults. |
| `valgrind` | object | no | Optional suppression profiles for Memcheck. |
| `ctest` | object | no | Trusted CTest projects and per-test timeout. |
| `cores` | array of objects | no | Trusted core+executable profiles. |

### Path rules

`workspace_roots`, `report_dir`, `program` and `cwd` are normalized with
`filepath.Clean` and must be:

- absolute;
- at most 4096 bytes and free of NUL;
- different from `/`;
- outside `/proc`, `/sys` and `/dev`.

`program` and `cwd` must resolve **lexically** inside one of the configured
roots. Duplicate roots are rejected. Symlinks are not resolved at configuration
time; runtime components must revalidate the resolved path before execution.
These checks constrain mistakes and traversal, but they are **not a sandbox**.

### `allowed_targets[]`

| Field | Type | Required | Rules |
|---|---|---|---|
| `id` | string | yes | `^[a-z0-9][a-z0-9._-]{0,63}$`, unique. |
| `program` | string | yes | Absolute path inside a workspace root. |
| `cwd` | string | yes | Absolute directory inside a workspace root. |
| `allowed_args` | array of strings | no | At most 64 arguments, each at most 4096 bytes, no NUL. |
| `sanitizers` | array of strings | no | At most 4 unique values among `asan`, `ubsan`, `lsan`, `tsan`; declares how the binary was instrumented. |

`program` and `cwd` need not exist at validation time. `doctor` reports missing
or non-executable programs as warnings; a missing target does not prevent the
server from starting.

At execution time, requested arguments must be a multiset subset of
`allowed_args`: an unknown value is denied, and a value cannot be repeated more
often than it appears in the configuration. An empty `allowed_args` permits no
arguments. Arguments are always passed as a list, never through a shell.

### `backends`

```json
{"valgrind": true, "gdb": true, "lldb": false, "sanitizers": false, "ctest": false}
```

Enabling a backend allows probing and tool exposure. It never installs software.
Valgrind is functionally probed for Memcheck, Helgrind and DRD XML support;
`analysis_start`, `helgrind_run` and `drd_run` are published only while their
respective capabilities are `available`. A target may declare sanitizer builds
with a `sanitizers` array (`asan`, `ubsan`, `lsan`, `tsan`). Enabling the
sanitizer backend does not build or instrument binaries. Each of `asan_run`,
`ubsan_run`, `lsan_run` and `tsan_run` is published only when the backend is
enabled and at least one usable target declares that sanitizer. The declaration
is an administrator trust statement; the capability requires the declared
target's program and cwd to be currently usable. CTest is enabled separately;
its discovery and run tools require `backend.ctest` and a usable trusted
`ctest.projects[]` entry. GDB is
functionally
probed by launching `gdb --quiet --nx --interpreter=dap` and completing the DAP
`initialize` handshake without loading a target. LLDB is probed by discovering
`lldb-dap` or a versioned `lldb-dap-<N>` in absolute PATH directories and
completing the same handshake with fixed init settings. `debug_start` is published
while at least one of `backend.gdb` or `backend.lldb` is `available` and at least
one declared target currently resolves under policy. An omitted `backend`
prefers GDB, then falls back to LLDB if GDB is disabled or unavailable.
Execution actions require one available debugger backend. The analysis evidence
tools (`analysis_status`, `analysis_cancel`, `analysis_report`,
`analysis_artifact`, `diagnostics_diff`) stay published so an existing run
remains queryable; `debug_status` and `debug_stop` stay published
for an existing session so it can always be released.

### `valgrind`

```json
{"suppressions": {"project-known": "/home/user/work/native-project/suppressions/known.supp"}}
```

| Field | Type | Rules |
|---|---|---|
| `suppressions` | object | At most 32 entries. Keys match `^[a-z0-9][a-z0-9._-]{0,63}$`. |

A suppression profile id may be selected by an analysis request
(`options.suppressions_profile`). Values are
absolute paths inside `workspace_roots`, at most 1 MiB. They need not exist at
validation time; at invocation the path is resolved with `EvalSymlinks`,
revalidated inside the roots, checked as a regular file, opened with
`O_NOFOLLOW` and capped at 1 MiB. An unknown profile or an unsafe path denies
the request before Valgrind starts.

Suppressed errors are reportable as tool-reported counts but are never treated
as resolved. See [valgrind.md](valgrind.md#suppressions).

### `policy`

| Field | Type | Default | Rules |
|---|---|---|---|
| `profile` | string | `safe-default` | `safe-default`, `trusted-local` or `elevated-local`. |
| `attach` | boolean | `false` | Requires `elevated-local`. |
| `evaluate` | boolean | `false` | Requires `elevated-local`. |
| `attach_allowed_pids` | array of integers | empty | Requires `attach=true` and `elevated-local`; explicit, unique PIDs > 1. |
| `attach_terminate` | boolean | `false` | Allows `debug_stop(terminate=true)` to kill an attached PID; requires attach and a non-empty PID allowlist. |

`elevated-local` is accepted by validation. `attach` is implemented only
through the explicit PID allowlist; `evaluate` remains unsupported and is never
exposed by the default profile.

### `limits`

| Field | Default | Min | Max |
|---|---|---|---|
| `timeout_seconds` | `120` | `1` | `86400` |
| `max_jobs` | `2` | `1` | `32` |
| `max_sessions` | `2` | `1` | `32` |
| `max_report_bytes` | `10485760` | `1024` | `1073741824` |
| `max_response_items` | `100` | `1` | `10000` |

Zero or an omitted limit selects the default. Negative values and values above
the maximum are rejected.

- `timeout_seconds` is both the default and the maximum job timeout. An
  `analysis_start.timeout_ms` request may only shorten it; a longer value is
  rejected with `invalid_argument`.
- `max_jobs` bounds concurrent analyses; the next request is rejected with
  `quota_exceeded`.
- `max_report_bytes` is the per-run artifact budget (raw XML, logs, manifest and
  normalized report). The API read bound is `min(max_report_bytes, 64 MiB)`.
- `max_response_items` bounds list/response cardinality. Diagnostic pages use
  `min(max_response_items, 500)` items and default to 50; the effective cap is
  reported as `limits.max_page_items` in `system_capabilities`.
- `max_sessions` bounds concurrent live debugger sessions. Terminal sessions are
  retained briefly for `debug_status`/`debug_stop` but do not consume a live
  slot. Idle sessions are closed after 15 minutes; terminal records are retained
  for 5 minutes. These retention bounds are internal defaults at J6/J7.

## Capability probing

At startup the server resolves enabled backend executables using the server
process `PATH` and records their absolute path and version. Relative `PATH`
entries are rejected. Probing is bounded (5 seconds per probe, concurrently),
uses a minimal fixed environment (`PATH`, `LC_ALL=C`, a private `HOME`) and never
loads or runs a user program.

Probes run at startup and again every 30 seconds, independently of tool calls.
A capability change reconciles `tools/list` and emits
`notifications/tools/list_changed`. Probes never run a user program.

| Backend | Executable | State now |
|---|---|---|
| Valgrind | `valgrind` | `available` when `--version` and `--tool=memcheck --help` verify Memcheck XML support |
| Valgrind Helgrind | `valgrind` | `backend.valgrind.helgrind` available when `--tool=helgrind --help` verifies the tool |
| Valgrind DRD | `valgrind` | `backend.valgrind.drd` available when `--tool=drd --help` verifies the tool |
| Sanitizers | target declarations | `backend.sanitizer.<tool>` available when enabled and at least one usable target declares that tool; runtime instrumentation is trusted, not probed |
| CTest | `ctest` | `backend.ctest` available when `ctest --help` exposes the fixed discovery, JUnit, selection, timeout, output and fixture-exclusion options used by the server; CTest tools additionally require a usable project |
| GDB | `gdb` | `available` when the DAP `initialize` handshake succeeds; no target is loaded |
| LLDB | `lldb-dap`, `lldb-dap-<N>` | `available` when a versioned-discovery candidate completes the DAP `initialize` handshake; no target is loaded |

The Valgrind probes run only `--version` and fixed `--tool=<tool> --help`
invocations with a short timeout and `LC_ALL=C`; they never run a user program
and never use sudo. The sanitizer probe is configuration-only and records that
the target declaration is trusted.
The GDB probe runs `--version`, then starts GDB with `--quiet --nx
--interpreter=dap` plus the same hardening `-ex` commands used by sessions and a
private `HOME`, sends one DAP `initialize` request and closes the process. It
never loads a target, and a rejected hardening command makes the capability
`unsupported`. The LLDB probe scans absolute PATH directories for `lldb-dap` and
`lldb-dap-<N>`, tries the unsuffixed name first and then versioned candidates in
descending numeric order, caps the list at two candidates, starts each with
fixed `-c` init settings and a private `HOME`, and verifies `initialize`. The
capability detail records that `lldb-dap` does not report whether the fixed
hardening settings were understood, so J7 cannot independently validate them. Its `--version` probe is
optional because older adapters do not guarantee terminating behavior; the
initialize handshake is the functional proof. The optional version command gets
a short independent timeout so a hanging `--version` cannot consume the whole
probe budget. A discovered GDB/LLDB binary is not by itself sufficient: the
backend stays `unknown` or `unsupported` until the handshake succeeds. Every Memcheck run and debugger start revalidates the backend
binary. A missing binary at start is a typed `backend_unavailable` error; a
binary that disappears before `exec` produces a `tool_error` or `start_failed`
error, never "no errors".

Only one lldb-dap candidate needs to succeed; the selected absolute path and
version are recorded in the capability. A broken unsuffixed symlink does not
prevent a working `lldb-dap-<N>` from being selected.

Missing executables produce `unavailable`; disabled backends produce
`disabled`. Neither prevents the server from starting, and neither is ever
reported as "no errors".

## Example

[`config.example.json`](../config.example.json) is a complete document. Replace
the paths with directories that exist on your machine and run
`validate-config` first.

### `ctest`

```json
{
  "ctest": {
    "test_timeout_seconds": 30,
    "projects": [
      {
        "id": "solver-tests",
        "test_dir": "/home/user/work/native-project/build",
        "allowed_tests": ["Core.basic", "Core.io"],
        "profiles": {"quick": ["Core.basic"]}
      }
    ]
  }
}
```

`test_dir` is the directory containing `CTestTestfile.cmake`; it must be an
absolute directory inside `workspace_roots`. The server never configures or
builds the project. `allowed_tests` is the complete exact-name allowlist: CTest
discovery is filtered to these names, and `ctest_run` rejects an unlisted test,
a duplicate, or a client-supplied regex. Profiles are named subsets of
`allowed_tests`.

`test_timeout_seconds` is the default per-test CTest `--timeout` and also the
maximum a request may shorten. The overall run timeout is bounded by
`limits.timeout_seconds`. CTest may apply a test-specific `TIMEOUT` property;
after parsing, the server checks a passed test's JUnit duration against the
requested per-test limit and downgrades an over-limit test to `timeout`. It
never turns missing or unparsable evidence into a passed run.

`ctest_discover` and `ctest_run` are published only when `backend.ctest` is
available and at least one declared project has a usable test directory. The
other CTest tools (`ctest_status`, `ctest_cancel`, `ctest_report`,
`ctest_artifact`) stay published for persisted runs.

### `cores`

```json
{
  "cores": [
    {
      "id": "crash",
      "executable": "/home/user/work/native-project/build/fixture",
      "core": "/home/user/work/native-project/build/core.1234"
    }
  ]
}
```

Core profiles bind one reviewed executable to one core file. Both paths are
absolute, must be inside `workspace_roots` and are revalidated at invocation.
`core_analyze` accepts only the profile id; it never accepts a core/executable
path or a debugger command. The tool uses fixed GDB batch flags and commands,
with a bounded timeout and response. It is published only while `backend.gdb`
is available and at least one profile is declared.

### `policy.attach_allowed_pids`

`debug_attach` is refused by default. To enable it, use `elevated-local` with
`policy.attach=true` and an explicit `attach_allowed_pids` list (at most 32
unique PIDs greater than 1). The process must also:

- share the server's real/effective/saved UID;
- have a workspace-contained executable and working directory;
- not already be traced or dead;
- satisfy the current Yama `ptrace_scope` (scope 1 requires it to be a
  descendant of the server; scope 2/3 is refused).

The server never changes a sysctl or uses sudo. Attached sessions detach on
`debug_stop` by default. `terminate=true` kills the target only when
`policy.attach_terminate=true`; otherwise the request is refused. PIDs are
explicit and ephemeral, not process-name patterns.
