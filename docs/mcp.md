# MCP surface

The server speaks MCP over **stdio**. stdout carries only newline-delimited
JSON-RPC 2.0 messages; logs go to stderr. There is no HTTP transport, no
telemetry and no remote access.

## Tool discovery

`tools/list` is computed from the capability registry. A tool is published only
when all of the following hold:

1. the tool is implemented in this build;
2. configuration enables its backend;
3. local policy permits the operation;
4. the required capability is `available`.

`analysis_start` (Memcheck), `helgrind_run` and `drd_run` require their
respective `backend.valgrind*` capability. `asan_run`, `ubsan_run`, `lsan_run`
and `tsan_run` each require `backend.sanitizer.<tool>` and a target declaration
matching that sanitizer. The five evidence/report tools (`analysis_status`,
`analysis_cancel`, `analysis_report`, `analysis_artifact`, `diagnostics_diff`)
are always published when the analysis service is running: they never launch a
program and remain usable for an in-flight run or a run persisted by a previous
server process, even if the backend later disappears. They return
`run_not_found` when no such run exists.

`debug_start` requires at least one of `backend.gdb` or `backend.lldb` to be
`available` **and** at least one declared target that currently resolves under
policy. The backend defaults to GDB when usable and falls back to LLDB when
GDB is disabled or unavailable (exists, is executable and stays inside a workspace root). The
resume/pause/step, breakpoint, threads, stack, scopes and variables tools require
at least one debugger backend to be available. `debug_status` and `debug_stop`
are published when a debugger backend is available or when an existing session
is retained, so a session can always be inspected and released even if its
backend later disappears. `backend` accepts `gdb` (default) or `lldb`.

Capabilities are probed at startup and revalidated every 30 seconds,
independently of tool calls. The server advertises `tools.listChanged = true` and
emits `notifications/tools/list_changed` whenever the exposed set changes. A
client must refresh its tool list on that notification; clients that do not
support dynamic lists must reconnect.

| Tool | Launches a program | Requires | Read-only |
|---|---|---|---|
| `system_capabilities` | no | — | yes |
| `analysis_start` | **yes**, an allowlisted target under Memcheck | `backend.valgrind` | no |
| `helgrind_run` | **yes**, an allowlisted target under Helgrind | `backend.valgrind.helgrind` | no |
| `drd_run` | **yes**, an allowlisted target under DRD | `backend.valgrind.drd` | no |
| `asan_run` | **yes**, a target declared as built with ASan | `backend.sanitizer.asan` + target declaration | no |
| `ubsan_run` | **yes**, a target declared as built with UBSan | `backend.sanitizer.ubsan` + target declaration | no |
| `lsan_run` | **yes**, a target declared as built with LSan | `backend.sanitizer.lsan` + target declaration | no |
| `tsan_run` | **yes**, a target declared as built with TSan | `backend.sanitizer.tsan` + target declaration | no |
| `analysis_status` | no | — | yes |
| `analysis_cancel` | no (signals a run started by this server) | — | no |
| `analysis_report` | no | — | yes |
| `analysis_artifact` | no | — | yes |
| `diagnostics_diff` | no (compares two persisted reports) | — | yes |
| `core_analyze` | no (opens a configured core with fixed GDB batch commands) | `backend.gdb` + configured core profile | yes |
| `core_artifact` | no | — | yes |
| `profile_callgrind` | **yes**, declared target under Callgrind | `backend.valgrind.callgrind` + usable target | no |
| `profile_massif` | **yes**, declared target under Massif | `backend.valgrind.massif` + usable target | no |
| `profile_compare` | no (reads persisted profiles) | — | yes |
| `profile_artifact` | no | — | yes |
| `profile_perf` | **yes, experimental**, declared target under fixed perf stat | `backend.perf` + usable target + successful permission probe | no |
| `perf_artifact` | no | — | yes |
| `vgdb_inspect` | **yes, experimental**, fixed read-only vgdb inspection | `backend.vgdb` + `backend.valgrind` + usable target | no |
| `vgdb_artifact` | no | — | yes |
| `ctest_discover` | no (runs `ctest --show-only`, no test execution) | `backend.ctest` + usable project | yes |
| `ctest_run` | **yes**, exact allowlisted CTest tests | `backend.ctest` + usable project | no |
| `ctest_status` | no | — | yes |
| `ctest_cancel` | no (signals a run started by this server) | — | no |
| `ctest_report` | no | — | yes |
| `ctest_artifact` | no | — | yes |
| `debug_start` | **yes**, an allowlisted target under GDB or LLDB | one debugger backend and a usable target | no |
| `debug_attach` | **yes**, an explicitly allowlisted PID | `backend.gdb` + `attach=true` + PID allowlist + Yama checks | no |
| `debug_status` | no | a debugger backend or a retained session | yes |
| `debug_set_breakpoints` | no | at least one debugger backend | no |
| `debug_continue` | no (resumes a process) | at least one debugger backend | no |
| `debug_pause` | no (stops a process) | at least one debugger backend | no |
| `debug_step` | no (resumes a process) | at least one debugger backend | no |
| `debug_threads` | no | at least one debugger backend | yes |
| `debug_stack_trace` | no | at least one debugger backend | yes |
| `debug_scopes` | no | at least one debugger backend | yes |
| `debug_variables` | no | at least one debugger backend | yes |
| `debug_stop` | no (terminates launched sessions; detaches attached sessions) | a debugger backend or a retained session | no |

## Shared conventions

- Every payload has a `schema_version` (`1` at J4–J10). It changes only for an
  incompatible shape change; additive fields keep the version.
- `run_id` is an opaque `run_<32 lowercase hex>` identifier. It is not a path.
- `session_id` is an opaque `dbg_<32 lowercase hex>` identifier. It is not a
  path and never exposes a PID.
- `stop_generation` increments on every debugger `stopped` event. Frame, scope
  and variable references are valid only for the generation that produced them.
- `analysis_status.tool` is the requested analysis tool (`memcheck`);
  `analysis_report.tool` is the canonical report's engine (`valgrind`).
- The **job state** (`state`) is distinct from the **business result**
  (`result`) and from the **pipelines phase** (`phase`). A completed job can
  carry `result=incomplete` when evidence was partial.
- `analysis_complete=false` means the evidence is partial. `total=0` with
  `analysis_complete=false` never means "no bugs". A complete analysis with zero
  errors is `result=no_errors_observed`.
- Diagnostics are tool-reported (`confidence=tool_reported`). The server does not
  infer root causes.
- Responses are bounded by items and bytes. Data omitted because of a bound is
  reported explicitly (`page.omitted_oversized`, `page.unreadable_diagnostics`,
  warnings) and never silently treated as absent.
- Tool errors are MCP tool errors (`isError=true`) whose text starts with a
  named `code`. Schema validation failures are produced by the MCP SDK before the
  handler runs and do not carry one of these codes.

## `analysis_start`

Starts one bounded Memcheck analysis. It returns as soon as the process has been
launched.

**Input**

| Field | Type | Required | Rules |
|---|---|---|---|
| `target_id` | string | yes | An id declared in `allowed_targets`; never a path. |
| `tool` | string | no | Only `memcheck`; omitted means `memcheck`. |
| `args` | array of strings | no | Multiset subset of the target's `allowed_args`. |
| `timeout_ms` | integer | no | `1..limits.timeout_seconds * 1000`; only shortens the configured limit. |
| `options` | object | no | Strict Memcheck options; unknown fields are rejected. |

`options` fields:

| Field | Default | Accepted values |
|---|---|---|
| `leak_check` | `full` | `none`, `summary`, `full` |
| `show_leak_kinds` | `all` | `all`, `definite`, `possible`, `reachable`, `definite,possible` |
| `track_origins` | `true` | boolean |
| `num_callers` | `12` | `1..50` |
| `suppressions_profile` | none | key of `valgrind.suppressions` |
| `trace_children` | `false` | only `false`; `true` is rejected |

There is **no** `cwd_id` field: the target's configured `cwd` is used. There is no
free-form Valgrind flag string, no shell and no path supplied by the client.

**Rejections**

| Situation | Code |
|---|---|
| unknown `target_id` | `target_unknown` |
| target or arguments denied by policy | `policy_denied` |
| `backend.valgrind` disabled | `tool_disabled` |
| Valgrind missing or not functionally verified | `backend_unavailable` |
| concurrent job quota reached | `quota_exceeded` |
| invalid tool, option or timeout | `invalid_argument` / `unknown_tool` |

**Output (illustrative, immediately after launch)**

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "tool": "memcheck",
  "target_id": "fixture",
  "state": "queued",
  "phase": "queued",
  "analysis_complete": false,
  "progress": {"elapsed_ms": 0},
  "metrics": {
    "diagnostics": 0,
    "occurrences": 0,
    "suppressed": 0,
    "stdout_bytes": 0,
    "stderr_bytes": 0,
    "stdout_truncated": false,
    "stderr_truncated": false,
    "output_incomplete": false
  },
  "artifacts": [],
  "report_available": false,
  "warnings": ["analysis is not complete; an empty result does not mean no errors"]
}
```

## `analysis_status`

Reports one run without side effects. Input is `{"run_id": "..."}`.

Runs still tracked by this server are read from memory. Runs no longer tracked
(after retention, or after a server restart) are read from `result.json` and
`analysis.json`. A persisted `state=running` is reported with a warning that it
comes from the manifest and may be stale; the server does not claim a live
process it no longer owns.

**Output (illustrative, completed)**

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "tool": "memcheck",
  "target_id": "fixture",
  "state": "completed",
  "phase": "completed",
  "result": "errors_found",
  "analysis_complete": true,
  "program": {
    "target_id": "fixture",
    "path": "/workspace/build/fixture",
    "sha256": "sha256:…",
    "args": [],
    "exit_code": 42,
    "timed_out": false,
    "canceled": false,
    "duration_ms": 210
  },
  "started_at": "2026-10-18T12:00:00Z",
  "finished_at": "2026-10-18T12:00:00.210Z",
  "progress": {"elapsed_ms": 210},
  "metrics": {
    "diagnostics": 1,
    "occurrences": 1,
    "suppressed": 0,
    "stdout_bytes": 0,
    "stderr_bytes": 0,
    "stdout_truncated": false,
    "stderr_truncated": false,
    "output_incomplete": false,
    "raw_xml_bytes": 2637,
    "report_bytes": 4180
  },
  "artifacts": [
    {"name": "memcheck.xml", "kind": "raw_xml", "bytes": 2637, "sha256": "sha256:…", "truncated": false}
  ],
  "report_available": true,
  "warnings": []
}
```

`phase` values: `queued`, `running`, `analyzing`, `completed`, `failed`,
`cancelled`, `timed_out`, `unknown`. `analyzing` means the process has ended and
parsing/writing is still in progress. `started_at`/`finished_at` are the
instrumented process times; when `phase=analyzing`, `finished_at` is set while
post-processing is still running. `state` values are the job states
(`queued`, `running`, `completed`, `cancelled`, `failed`, `timed_out`) plus
`unknown` for a persisted run the current server cannot vouch for. Before a
terminal phase, metric zeros mean "not known yet", not "no diagnostics".

## `analysis_cancel`

Input is `{"run_id": "..."}`. Cancellation is cooperative: the process group
receives `SIGTERM`, then `SIGKILL` after the runner grace period. The call
returns an acknowledgement; poll `analysis_status` for the final state.

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "cancel_requested": true,
  "state": "running",
  "phase": "running",
  "message": "cancellation requested; the process group receives SIGTERM then SIGKILL"
}
```

For an already terminal run, `cancel_requested=false` with its final state and
`"run already finished; nothing was canceled"`. For a run persisted by another
server process, `cancel_requested=false` and a message that nothing was canceled.
Cancellation never changes partial evidence into a clean result.

## `analysis_report`

Returns a compact summary plus one bounded page of diagnostics. The response
carries `analysis_tool` (`memcheck`, `helgrind`, `drd`, `asan`, …), while
`tool` remains the engine identity (`valgrind`, `asan`, …).

**Input**

| Field | Type | Rules |
|---|---|---|
| `run_id` | string | Required. |
| `cursor` | string | Opaque `next_cursor` from a previous page. Tied to the run and filters. |
| `limit` | integer | Page size; `0` selects 50. Clamped to `min(limits.max_response_items, 500)`. |
| `kinds` | array of strings | OR within the list, AND with other filters. At most 32. |
| `severities` | array of strings | `error`, `warning`, `info`; at most 3. |
| `path_prefix` | string | Matches any frame path with this prefix. |
| `function` | string | Matches any frame with this exact function name. |
| `fingerprint` | string | Exact `sha256:<64 hex>` fingerprint. |
| `min_occurrences` | integer | At least this many tool-reported occurrences. |

`path_prefix` and `function` must match the **same** frame. Frames considered
are the primary stack, context stacks (for example allocation sites) and the
origin stack. Filters are stable across pages: reusing a cursor with different
filters or another run returns `invalid_cursor`.

**Output**

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "tool": {"name": "valgrind", "path": "/usr/bin/valgrind", "version": "valgrind-3.26.0"},
  "result": "errors_found",
  "analysis_complete": true,
  "program": {"target_id": "fixture", "path": "/workspace/build/fixture", "args": [], "exit_code": 42, "timed_out": false, "canceled": false, "duration_ms": 210},
  "summary": {"total": 1, "diagnostic_records": 1, "by_kind": {"InvalidRead": 1}, "by_severity": {"error": 1}, "suppressed": 0, "truncated": false},
  "artifacts": [{"name": "memcheck.xml", "kind": "raw_xml", "bytes": 2637, "sha256": "sha256:…", "truncated": false}],
  "page": {
    "offset": 0,
    "limit": 50,
    "returned": 1,
    "matched": 1,
    "consumed": 1,
    "has_more": false,
    "byte_budget_reached": false,
    "omitted_oversized": 0,
    "unreadable_diagnostics": 0,
    "scan_incomplete": false
  },
  "diagnostics": [
    {
      "id": "diag_0123456789abcdef0123456789abcdef_0",
      "fingerprint": "sha256:…",
      "origin": "valgrind-memcheck",
      "kind": "InvalidRead",
      "severity": "error",
      "message": "Invalid read of size 4",
      "location": {"function": "main", "path": "invalid_read.c", "line": 8},
      "stack": [{"function": "main", "path": "invalid_read.c", "line": 8}],
      "occurrences": 1,
      "evidence": {"artifact": "memcheck.xml", "record_id": "0x0", "record_index": 0, "raw_kind": "InvalidRead"},
      "confidence": "tool_reported"
    }
  ],
  "warnings": []
}
```

**Pagination and bounds**

- `page.offset` counts matching diagnostics skipped before this page.
  `page.consumed` counts matching diagnostics covered by this page (returned plus
  omitted). `page.matched` counts matching readable diagnostics in the whole
  report. `page.has_more` and `next_cursor` follow from these counts.
- `page.byte_budget_reached=true` means the page stopped before the item limit to
  respect the 1 MiB diagnostics budget. Request the next page with
  `next_cursor`.
- A single matching diagnostic larger than the page budget is omitted,
  `omitted_oversized` is incremented, the cursor advances past it and the page
  carries a warning pointing to `analysis_artifact`. The raw evidence is never
  destroyed.
- A diagnostic record larger than the per-record read bound cannot be examined:
  `unreadable_diagnostics` (report-wide) is incremented, `scan_incomplete=true`,
  and matching counts are explicitly incomplete. `omitted_oversized` is
  page-local.
- The `summary` is always the full report summary, independent of filters and
  pagination.

## Concurrency analysis: `helgrind_run` and `drd_run`

Both tools launch the declared target through Valgrind and return a normal
`analysis_status`-shaped run handle. Their input is `target_id`, `args[]` and
`timeout_ms`; they accept no path, shell command or free-form Valgrind flags.
`helgrind_run` and `drd_run` are gated on separate capabilities. A clean run is
not proof that no race exists; concurrency scheduling varies between runs.

## Sanitizer tools: `asan_run`, `ubsan_run`, `lsan_run`, `tsan_run`

Each launches a target that configuration explicitly declares as built with the
same sanitizer. The server does not instrument binaries. `analysis_complete`
describes the evidence/invocation, not a claim that the program is race-free or
leak-free. See [sanitizers.md](sanitizers.md).

Input: `target_id`, `args[]`, `timeout_ms`. The target's `sanitizers`
configuration must include the requested value. The raw evidence is
`sanitizer.log`, exposed as artifact type `raw_log`; target stderr is a separate
`stderr` artifact and is not parsed as sanitizer evidence. Each tool is gated by
its own `backend.sanitizer.<tool>` capability.

## CTest tools

CTest integration is opt-in through trusted `ctest.projects`. The server never
configures or builds a project and never accepts a project path, test regex or
arbitrary CTest flag from the client. `ctest_discover` runs
`ctest --show-only=json-v1` without executing tests and filters names through
`allowed_tests`. `ctest_run` selects exact allowed tests or a configured profile,
runs them asynchronously and returns a run id for `ctest_status`/`ctest_report`.
Per-test CTest timeout is bounded by `ctest.test_timeout_seconds`; the overall
timeout by `limits.timeout_seconds`.

`ctest_report` is bounded and reports `passed`, `failed`, `timeout`, `skipped`
and `not_run`; missing/unparsable JUnit evidence and skipped selected tests
yield `incomplete`, never a pass. Artifacts are `junit` (`ctest.xml`), `stdout`, `stderr` and `report`
(`ctest.json`). See [ctest.md](ctest.md).

## `diagnostics_diff`

Compares two persisted reports by versioned fingerprint and returns bounded
`new`, `resolved`, `persistent`, `changed` and `uncertain` items. It never
launches a program. New reports use `sha256-v2`; v1 reports remain comparable
only with v1. See [diff.md](diff.md) for the full semantics, compatibility
rules and fingerprint recipes.

**Input**

| Field | Type | Required | Rules |
|---|---|---|---|
| `base_run_id` | string | yes | Reference/older run id. |
| `target_run_id` | string | yes | Candidate/newer run id; must differ from the base. |
| `cursor` | string | no | Opaque `page.next_cursor` from the same pair. |
| `limit` | integer | no | Page size, bounded by the server cap. |

**Output**

```json
{
  "schema_version": 1,
  "base_run_id": "run_0123456789abcdef0123456789abcdef",
  "target_run_id": "run_fedcba9876543210fedcba9876543210",
  "compatibility": {"fingerprint_algorithm": "sha256-v2"},
  "summary": {
    "base_diagnostics": 2, "target_diagnostics": 3,
    "base_occurrences": 2, "target_occurrences": 4,
    "new": 1, "resolved": 0, "persistent": 1,
    "changed": 0, "uncertain": 0
  },
  "page": {
    "offset": 0, "limit": 50, "returned": 2, "matched": 2,
    "consumed": 2, "has_more": false,
    "byte_budget_reached": false
  },
  "items": [],
  "warnings": []
}
```

`compatibility.changes` reports observed differences (including binary, path and
program outcome changes). `compatibility.uncertainties` is the downgrading set
(tool/parser versions, options, roots, arguments, suppressions, incomplete
evidence or missing metadata); when it is non-empty every item is `uncertain`
rather than a false new/resolved/persistent. A structurally incompatible pair
returns `incompatible_runs` and no item list. Duplicate fingerprints are reported with
`base_count`/`target_count` and a reason.

## `analysis_artifact`

Returns a bounded UTF-8 excerpt of one allowlisted artifact. **No filesystem
path is returned and no arbitrary file can be requested.** Input fields:
`run_id` (required), `artifact_type` (required), `offset` (default `0`),
`max_bytes` (default 64 KiB, hard cap 1 MiB; larger values are clamped).

| `artifact_type` | File | Contents |
|---|---|---|
| `raw_xml` | `memcheck.xml`, `helgrind.xml`, `drd.xml` | Raw Valgrind XML evidence. |
| `raw_log` | `sanitizer.log` | Sanitizer-owned runtime log. |
| `analysis_report` | `analysis.json` | Canonical normalized report. |
| `stdout` | `stdout.log` | Bounded target/Valgrind stdout. |
| `stderr` | `stderr.log` | Bounded target/Valgrind stderr; not parsed as sanitizer evidence. |
| `job_manifest` | `result.json` | Execution manifest. |

**Output**

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "artifact_type": "raw_xml",
  "file_name": "memcheck.xml",
  "bytes": 2637,
  "offset": 0,
  "returned_bytes": 500,
  "max_bytes": 500,
  "has_more": true,
  "next_offset": 500,
  "content": "<?xml version=\"1.0\"?>\n<valgrindoutput>…",
  "invalid_utf8_replaced": false
}
```

`returned_bytes` is the number of artifact bytes consumed; `next_offset` is
`offset + returned_bytes`. When an excerpt stops before EOF, a trailing partial
UTF-8 sequence is moved back to the next rune boundary, so the continuation
starts on a complete rune. If that would return zero bytes (a window smaller than
the rune), the raw bytes are returned unchanged to guarantee forward progress,
invalid bytes are replaced with U+FFFD and `invalid_utf8_replaced=true`. Control
characters in captured output are preserved as evidence: treat excerpt text as
untrusted data.


## Debug tools (J6/J7)

The debug API launches one declared target under GDB or LLDB over DAP. It never
accepts an executable path, PID, debugger flag, expression, memory address,
debugger script or shell command. GDB runs with `--quiet --nx
--interpreter=dap`, auto-loaded Python/GDB scripts and local `.gdbinit` disabled,
debuginfod disabled, startup-with-shell disabled and a private `HOME`; its
inferior tty is `/dev/null`. LLDB is discovered as `lldb-dap` or a versioned
`lldb-dap-<N>` and runs with cwd/user init loading and external symbol lookup
disabled and a private `HOME`; its inferior uses a private PTY. Neither inferior
shares the DAP pipe. Backend-specific ordering and id differences are documented
in [debugger.md](debugger.md#backend-differences).

### `core_analyze`

Accepts only `profile_id`. Opens the configured executable and core with fixed
GDB batch commands (`info threads`, `thread apply all bt 32`) and returns a
bounded thread list, raw GDB output, `complete` and an optional `evidence`
reference. The full bounded GDB output is preserved in `core-gdb.log` and can be
paged through `core_artifact`. No path, debugger command or shell fragment is
accepted. A missing/mismatched core or truncated/uncertain
output is incomplete, never a clean report. See
[core-and-attach.md](core-and-attach.md).

## `debug_attach`

Accepts `pid`, optional `backend` (currently GDB only) and `stop_on_entry`. Requires
`elevated-local`, `policy.attach=true` and the exact PID in
`policy.attach_allowed_pids`. The server revalidates same-UID ownership,
workspace-contained executable/cwd, not-already-traced state and Yama
`ptrace_scope`; it never changes sysctls or uses sudo. With Yama scope 1 the
target must be a descendant of the server. `debug_stop` detaches by default for
attached sessions; `terminate=true` kills the target only when
`policy.attach_terminate=true`. No attach tool is
published with an empty allowlist.

## `profile_callgrind` / `profile_massif`

Run a declared target with fixed, non-client-controlled Valgrind flags. The raw
native file (`callgrind.out` or `massif.out`) and a bounded `profile.json` are
preserved. Callgrind returns native event totals and up to 32 top functions by
self cost; Massif returns peak heap/extra/stack bytes, snapshot count and
bounded samples. `complete=false` on timeout, cancellation, nonzero target exit,
truncation or parser failure. See [profile.md](profile.md).

## `profile_compare`

Compares two persisted profiles by run id. Different tools, normalized units,
Callgrind event sets/order or Massif time units are refused with
`incompatible_profiles`; incomplete profiles are refused. Deltas are
descriptive and never claim statistical significance or a regression.

## `profile_artifact`

Returns a bounded UTF-8 excerpt of `profile.json`, `callgrind.out` or
`massif.out` for a persisted profile run. Only artifacts referenced by that
run's normalized report are accepted; no path is accepted or returned.

## Experimental `profile_perf`

Runs only the fixed command
`perf stat -j -e task-clock,cycles,instructions -- <target>`.
The tool is hidden unless `backend.perf` is available after an unprivileged
permission probe, and a declared target currently resolves. It never changes
`perf_event_paranoid`, uses sudo or grants capabilities. Results are marked
`experimental=true`; unsupported/uncounted events are warnings and never
fabricated zeros. `perf_artifact` exposes bounded `perf.json`, `stdout.log` and
`stderr.log` excerpts. See [experimental.md](experimental.md).

## Experimental `vgdb_inspect`

Starts a declared target under Valgrind with vgdb enabled and sends only
`v.info gdbserver_status`, `v.info exectxt` and `v.info scheduler`. There is no
monitor-command request field or GDB console. Cleanup uses fixed `v.kill` and
job process-group cancellation; private FIFOs are removed. The tool is hidden
unless both `backend.vgdb` and `backend.valgrind` are available and a target is
usable. `vgdb_artifact` exposes bounded `vgdb.json`, `stdout.log` and
`stderr.log` excerpts.

## `debug_start`

**Input**

| Field | Type | Required | Rules |
|---|---|---|---|
| `target_id` | string | yes | An id declared in `allowed_targets`; never a path. |
| `backend` | string | no | `gdb` (preferred default) or `lldb`; omitted selects gdb when usable, then falls back to lldb. An explicit backend must be available. |
| `args` | array of strings | no | Multiset subset of the target's `allowed_args`. |
| `stop_on_entry` | boolean | no | Default `true`. GDB stops at the entry point; LLDB reports the entry stop as a SIGSTOP exception. |

The response is a `debug_status`-shaped `Status` with a new `session_id`. It
returns after the DAP `initialize`/`launch`/`configurationDone` handshake and,
with `stop_on_entry=true`, after the first `stopped` event. GDB reports
`reason=stopped`; LLDB reports `reason=exception` with a SIGSTOP description. Set a
breakpoint before continuing to reach source.

### `debug_status`

Reports `state` (`starting`, `running`, `stopped`, `terminated`, `detached`,
`failed`), `stop_generation`, the latest `stop` event (`reason`, `thread_id`,
`all_threads_stopped`, `hit_breakpoints`), `exit.exit_code` when reported,
capability flags, timestamps, bounded warnings and the last error. It never
contacts the adapter and remains available after the debuggee exits.

`running` after a resume means the adapter accepted the request, not that the
process has started executing. Poll until `stop_generation` changes.

### Breakpoints and inspection

| Tool | Input (besides `session_id`) | Behaviour |
|---|---|---|
| `debug_set_breakpoints` | `source_path`, `lines[]` | Requires a stopped session. `source_path` is absolute or relative to the target cwd, must resolve inside a workspace root, must be a regular file and must not escape through a symlink. At most 100 lines; duplicates are removed. Returns the adapter-reported `verified` state; unverified breakpoints are explicit. |
| `debug_threads` | `start?`, `count?` | One bounded page of adapter-reported threads. |
| `debug_stack_trace` | `thread_id?`, `start_frame?`, `levels?`, `stop_generation?` | Requires stopped. At most 100 frames per call; `has_more`/`next_frame` when more exist. |
| `debug_scopes` | `frame_id`, `stop_generation?` | Requires stopped. Returns scopes and `variables_reference` values for the current generation. |
| `debug_variables` | `variables_reference`, `start?`, `count?`, `stop_generation?` | Requires stopped. At most 200 children per page and 2048 bytes per value; child expansion is a separate request. |

Sources under a workspace root are returned as slash-separated relative paths.
External source paths are omitted and marked `external=true`; instruction
pointers, module paths, raw memory references and debuggee terminal output are
not exposed. A stripped or missing-symbol stack is returned with an explicit
warning instead of invented source locations.

### Resume, pause and stop

`debug_continue` is acknowledged asynchronously. `debug_step`
(`kind=next|in|out`) and `debug_pause` also return `accepted=true`, but wait up
to 5 seconds for the next stopped event so a normal step/pause can inspect the
new frame immediately. All three return the current state and
`stop_generation`; if the state is still `running`, poll `debug_status` for the
next stop. The stopped thread is used when `thread_id` is omitted;
`debug_pause` defaults to the first thread reported by the adapter (or the
process event for lldb-dap). If that thread has exited, pass an explicit
`thread_id` from `debug_threads`; thread ids are opaque and backend-specific. A resume that times
out leaves the reported state `running`, because a request already written to
the adapter cannot be recalled.

`debug_stop` terminates launched debuggees by default (`terminate` omitted or
`true`). For an attached session it detaches by default and leaves the target
running; `terminate=true` explicitly kills the attached target. For launched
sessions `terminate=false` returns `unsupported_capability` and leaves the
session running, so call `debug_stop` with `terminate=true`. Both adapters
advertise `supportTerminateDebuggee`; the launch path still fails closed rather
than claiming an unproven detach. The call is idempotent: stopping an already
terminal session returns `already_stopped=true` and its final status.

### State and references

`stopped` means the adapter reported a stop, not that any analysis succeeded. A
`stopped` event increments `stop_generation`; frame ids, scope references and
variable references from an older generation may be rejected by the adapter and must not
be reused after a resume. `debug_stack_trace`, `debug_scopes` and
`debug_variables` accept an optional `stop_generation`; a mismatching value is
rejected with `stale_reference` instead of returning evidence from another
stop. Contexts, idle retention and server shutdown terminate
sessions. Detach is not exposed in J6/J7; a terminated session is the final state.

### Debug limits

| Limit | Value |
|---|---|
| Concurrent live sessions | `limits.max_sessions` (1–32, default 2) |
| Idle live session | closed after 15 minutes without an API operation |
| Retained terminal session | 5 minutes |
| Breakpoint lines per request | 100 |
| Stack frames per page | 100 |
| Threads per page | 500 (also capped by `limits.max_response_items`) |
| Variables per page | 200 (also capped by `limits.max_response_items`) |
| One variable value | 2048 bytes, UTF-8 boundary, `value_truncated` |
| DAP request timeout | 10 seconds |
| Session startup timeout | 15 seconds |
| Stop-on-entry wait | 5 seconds |
| Session stop/disconnect timeout | 5 seconds |

## `system_capabilities`

**Input:** an empty object. **Output (`structuredContent`):**

```json
{
  "schema_version": 1,
  "server": {
    "name": "mcp-native-diagnostics",
    "version": "0.0.0-dev",
    "commit": "0da022a…",
    "date": "2026-10-18T00:42:04Z",
    "go_version": "go1.27.0",
    "modified": false
  },
  "policy": {"profile": "safe-default", "attach": false, "evaluate": false},
  "limits": {
    "timeout_seconds": 120,
    "max_jobs": 2,
    "max_sessions": 2,
    "max_report_bytes": 10485760,
    "max_response_items": 100,
    "max_page_items": 100,
    "default_artifact_bytes": 65536,
    "max_artifact_bytes": 1048576
  },
  "capabilities": [
    {
      "id": "backend.valgrind",
      "kind": "backend",
      "backend": "valgrind",
      "state": "available",
      "path": "/usr/bin/valgrind",
      "version": "valgrind-3.26.0",
      "features": ["memcheck", "xml-output", "suppressions"],
      "reason": "binary and requested feature verified (Memcheck tool and XML options verified from help output; every run revalidates the binary)",
      "last_checked": "2026-10-18T12:00:00Z",
      "probe": {"attempted": true, "success": true, "duration_ms": 18}
    }
  ],
  "tools": [
    {"name": "system_capabilities", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "analysis_start", "state": "available", "required_capability": "backend.valgrind", "exposed": true, "reason": "binary and requested feature verified"},
    {"name": "analysis_status", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "analysis_cancel", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "analysis_report", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "analysis_artifact", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "diagnostics_diff", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "debug_start", "state": "available", "required_capabilities": ["backend.gdb", "backend.lldb"], "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_status", "state": "available", "exposed": true, "reason": "implemented and enabled"},
    {"name": "debug_set_breakpoints", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_continue", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_pause", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_step", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_threads", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_stack_trace", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_scopes", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_variables", "state": "available", "exposed": true, "reason": "one debugger backend available"},
    {"name": "debug_stop", "state": "available", "exposed": true, "reason": "implemented and enabled"}
  ],
  "warnings": []
}
```

The `tools` array in the real payload lists every registered tool, exposed or
not. A debug tool reports both accepted backends in `required_capabilities`;
`required_capability` remains the single-capability form used by implementations
such as Valgrind. `max_page_items` is the effective page cap
(`min(500, limits.max_response_items)`); `default_artifact_bytes` is the excerpt
size used when `max_bytes` is omitted and `max_artifact_bytes` is the per-call
hard cap.

### Capability states

| State | Meaning |
|---|---|
| `available` | Functionally verified and usable. |
| `unavailable` | Required executable or resource is missing. |
| `disabled` | Explicitly disabled by configuration. |
| `unsupported` | Backend present but the feature is not supported. |
| `unknown` | Could not be verified, including binary-only discovery. |

Valgrind becomes `available` only after the binary and its Memcheck XML options
are verified. GDB and LLDB become `available` only after a real DAP `initialize`
handshake succeeds; merely finding `gdb` or `lldb-dap` on `PATH` is not
sufficient. LLDB additionally requires discovery of `lldb-dap` or a versioned
`lldb-dap-<N>` in an absolute PATH directory. See [dap.md](dap.md) for the
transport client and [debugger.md](debugger.md) for both backends, the
difference matrix, session lifecycle and limits.

### Probe fields

| Field | Meaning |
|---|---|
| `probe.attempted` | A probe was started. |
| `probe.success` | The probe mechanism completed (not that the capability is ready). |
| `probe.duration_ms` | Probe duration in milliseconds. |
| `probe.detail` | Optional diagnostic detail; no raw tool output is included. |

## Named error codes

| Code | Meaning |
|---|---|
| `invalid_argument` | Malformed or out-of-range request field, option or offset. |
| `unknown_tool` | Tool not implemented by this build. |
| `tool_disabled` | Analysis tool disabled by configuration. |
| `backend_disabled` | Debugger/backend disabled by configuration. |
| `backend_unavailable` | Backend missing, unusable, not functionally verified, or sanitizer target declaration absent. |
| `target_unknown` | `target_id` is not declared in configuration. |
| `policy_denied` | Target or arguments denied by local policy. |
| `quota_exceeded` | Concurrency, artifact or collection quota exhausted. |
| `run_not_found` | Unknown run id or missing run directory. |
| `invalid_cursor` | Malformed cursor, or reused with other filters or another run. |
| `report_unavailable` | Run exists but its normalized report is missing or not yet written. |
| `report_invalid` | Stored report violates its schema or internal invariants. |
| `report_too_large` | Stored report exceeds the API read bound (bytes or record count). |
| `incompatible_runs` | Two reports cannot be compared honestly (tool/target/OS/architecture/fingerprint algorithm or unusable evidence). |
| `diff_too_large` | A diff item or the response envelope exceeds the response byte bound. |
| `artifact_unavailable` | Requested artifact is missing or rejected by the store. |
| `canceled` | The caller's context was canceled. |
| `timeout` | The caller's context deadline expired. |
| `server_closed` | The analysis or debugger service is closed. |
| `source_denied` | A debugger source path is unavailable inside workspace roots (missing, non-regular, outside roots, or a symlink escape). The response is deliberately generic; the detailed reason is local stderr. |
| `session_not_found` | Unknown or expired debugger session id. |
| `not_stopped` | The debug operation requires a stopped session. |
| `not_running` | `debug_pause` requires a running session. |
| `session_terminated` | The debuggee has already terminated or detached. |
| `stale_reference` | A frame/scope/variable request carried a stop_generation that is no longer current. |
| `unsupported_capability` | The adapter does not support the requested operation. |
| `start_failed` | The debugger or the DAP handshake could not start the session. |
| `protocol_error` | A DAP request failed without a usable adapter result. |
| `internal` | Unexpected server failure. Never a diagnostic. |

## Limits

| Limit | Value | Enforcement |
|---|---|---|
| Page items | `min(limits.max_response_items, 500)`; default 50 | `analysis.Service.pageLimit` |
| Page diagnostics bytes | 1 MiB | `pageAccumulator` |
| One diagnostic record | 4 MiB | streaming reader |
| Diagnostic records scanned per read | 50,000 (`report_too_large` beyond) | streaming reader |
| Artifact excerpt | 64 KiB default, 1 MiB hard cap | `analysis.Artifact` |
| Report read | `min(limits.max_report_bytes, 64 MiB)` | streaming reader |
| Decoded report cache | 16 MiB estimated memory per entry (report JSON plus a per-record allowance), 64 MiB total, 16 entries | `reportCache` |
| Concurrent full-report loads | 2 (further requests stream a page instead of waiting) | `loadSlots` |
| Status/report warnings | 32 entries, 4 KiB each | `normalizeWarnings` |
| Debug session page | 100 frames, 500 threads, 200 variables | `debugger` service |
| Debug session values | 2048 bytes per variable value | `debugger` session |

The cache only accelerates repeated pagination. It is keyed by the opaque run id
and invalidated when the stored report size or modification time changes. Entries
whose estimated memory cost exceeds the cache bound are served by the streaming
reader instead.

## Error behaviour

Unknown tools and invalid arguments are rejected by the MCP layer. A backend that
disappears after startup does not crash the server: `analysis_start` or
`debug_start` and the execution actions are removed from `tools/list` at the next
capability refresh, and invocation revalidates the backend and returns
`backend_unavailable` rather than a false "no errors". `debug_status` and
`debug_stop` remain available for a retained session and still return its
state/cleanup result.

The server never reports "no errors" when a backend is missing, a probe is
incomplete, XML is truncated, parsing fails, output is truncated or a response
bound omitted diagnostics. Those cases produce `incomplete`/`tool_error` or an
explicit warning, and `analysis_complete=false` where applicable.
