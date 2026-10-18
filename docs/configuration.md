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

`program` and `cwd` need not exist at validation time. `doctor` reports missing
or non-executable programs as warnings; a missing target does not prevent the
server from starting.

At execution time, requested arguments must be a multiset subset of
`allowed_args`: an unknown value is denied, and a value cannot be repeated more
often than it appears in the configuration. An empty `allowed_args` permits no
arguments. Arguments are always passed as a list, never through a shell.

### `backends`

```json
{"valgrind": true, "gdb": true, "lldb": false}
```

Enabling a backend allows probing and tool exposure. It never installs software.
Valgrind is functionally probed for Memcheck XML support; `analysis_start` is
published only while `backend.valgrind` is `available`. GDB and LLDB remain
discovery-only and `unknown` until their DAP probes land (J6/J7). The evidence
tools (`analysis_status`, `analysis_cancel`, `analysis_report`,
`analysis_artifact`) stay published so an existing run remains queryable.

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

`elevated-local` is accepted by validation but its features are **not
implemented** yet; `doctor` warns about it. `attach` and `evaluate` are never
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
- `max_sessions` is reserved for the debugger session manager (J6).

## Capability probing

At startup the server resolves enabled backend executables using the server
process `PATH` and records their absolute path and version. Relative `PATH`
entries are rejected. Probing is bounded (2 seconds per probe, concurrently),
uses a minimal fixed environment (`PATH`, `LC_ALL=C`) and never runs a user
program.

Probes run at startup and again every 30 seconds, independently of tool calls.
A capability change reconciles `tools/list` and emits
`notifications/tools/list_changed`. Probes never run a user program.

| Backend | Executable | State now |
|---|---|---|
| Valgrind | `valgrind` | `available` when `--version` and `--tool=memcheck --help` verify Memcheck XML support |
| GDB | `gdb` | `unknown` until the J6 DAP probe |
| LLDB | `lldb-dap` | `unknown` until the J7 DAP probe |

The Valgrind probe runs only `--version` and `--tool=memcheck --help` with a
short timeout and `LC_ALL=C`; it never runs a user program and never uses sudo.
A discovered GDB/LLDB binary is still reported as `unknown`, not `available`,
because a binary on `PATH` does not prove that DAP works. Every Memcheck run
revalidates the Valgrind binary. A missing binary at start is a typed
`backend_unavailable` error; a binary that disappears before `exec` produces a
`tool_error` report, never "no errors".

`lldb-dap` is resolved but not executed, because its `--version` behaviour is
not guaranteed to terminate. Versioned adapter names (`lldb-dap-XX`) are not
discovered yet; that arrives with J7.

Missing executables produce `unavailable`; disabled backends produce
`disabled`. Neither prevents the server from starting, and neither is ever
reported as "no errors".

## Example

[`config.example.json`](../config.example.json) is a complete document. Replace
the paths with directories that exist on your machine and run
`validate-config` first.
