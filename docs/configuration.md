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
| `report_dir` | string | yes | Absolute path for future artifacts. |
| `allowed_targets` | array of objects | no | At most 128 targets. |
| `backends` | object | no | Missing booleans default to `false`. |
| `policy` | object | no | Defaults to `safe-default`, no elevated permission. |
| `limits` | object | no | Missing limits are replaced by defaults. |

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

Enabling a backend allows probing and, in later milestones, tool exposure. It
never installs software. At J1, backend records are discovery-only and remain
`unknown` until their functional probe lands.

### `policy`

| Field | Type | Default | Rules |
|---|---|---|---|
| `profile` | string | `safe-default` | `safe-default`, `trusted-local` or `elevated-local`. |
| `attach` | boolean | `false` | Requires `elevated-local`. |
| `evaluate` | boolean | `false` | Requires `elevated-local`. |

`elevated-local` is accepted by validation but its features are **not
implemented** at J1; `doctor` warns about it. `attach` and `evaluate` are never
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
the maximum are rejected. Limits are enforced by the runner and report layers
introduced in later milestones.

## Capability probing

At startup the server resolves enabled backend executables using the server
process `PATH` and records their absolute path and version. Relative `PATH`
entries are rejected. Probing is bounded (2 seconds per probe, concurrently),
uses a minimal `LC_ALL=C` environment and never runs a user program.

A discovered binary is reported as `unknown`, not `available`, until its
functional probe is implemented:

| Backend | Executable | Functional probe |
|---|---|---|
| Valgrind | `valgrind` | J3 (Memcheck XML) |
| GDB | `gdb` | J6 (DAP negotiation) |
| LLDB | `lldb-dap` | J7 (DAP negotiation) |

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
