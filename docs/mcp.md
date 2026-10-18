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

`analysis_start` additionally requires `backend.valgrind = available` because it
launches a target. The four evidence tools (`analysis_status`, `analysis_cancel`,
`analysis_report`, `analysis_artifact`) are always published when the analysis
service is running: they never launch a program and remain usable for an
in-flight run or a run persisted by a previous server process, even if the
backend later disappears. They return `run_not_found` when no such run exists.

Capabilities are probed at startup and revalidated every 30 seconds,
independently of tool calls. The server advertises `tools.listChanged = true` and
emits `notifications/tools/list_changed` whenever the exposed set changes. A
client must refresh its tool list on that notification; clients that do not
support dynamic lists must reconnect.

| Tool | Launches a program | Requires `backend.valgrind` | Read-only |
|---|---|---|---|
| `system_capabilities` | no | no | yes |
| `analysis_start` | **yes**, an allowlisted target under Valgrind | yes | no |
| `analysis_status` | no | no | yes |
| `analysis_cancel` | no (signals a run started by this server) | no | no |
| `analysis_report` | no | no | yes |
| `analysis_artifact` | no | no | yes |

## Shared conventions

- Every payload has a `schema_version` (`1` at J4). It changes only for an
  incompatible shape change; additive fields keep the version.
- `run_id` is an opaque `run_<32 lowercase hex>` identifier. It is not a path.
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

Returns a compact summary plus one bounded page of diagnostics.

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

## `analysis_artifact`

Returns a bounded UTF-8 excerpt of one allowlisted artifact. **No filesystem
path is returned and no arbitrary file can be requested.** Input fields:
`run_id` (required), `artifact_type` (required), `offset` (default `0`),
`max_bytes` (default 64 KiB, hard cap 1 MiB; larger values are clamped).

| `artifact_type` | File | Contents |
|---|---|---|
| `raw_xml` | `memcheck.xml` | Raw Valgrind XML evidence. |
| `analysis_report` | `analysis.json` | Canonical normalized report. |
| `stdout` | `stdout.log` | Bounded target/Valgrind stdout. |
| `stderr` | `stderr.log` | Bounded target/Valgrind stderr. |
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
`offset + returned_bytes`. Excerpt boundaries may split a UTF-8 sequence; invalid
bytes are replaced with U+FFFD and `invalid_utf8_replaced=true`. Control
characters in captured output are preserved as evidence: treat excerpt text as
untrusted data.

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
    {"name": "analysis_artifact", "state": "available", "exposed": true, "reason": "implemented and enabled"}
  ],
  "warnings": []
}
```

The `tools` array in the real payload lists every registered tool, exposed or
not. `max_page_items` is the effective page cap
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
are verified. GDB and LLDB remain discovery-only and therefore `unknown` when
their executable exists: a binary on `PATH` does not prove that a DAP session
works.

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
| `tool_disabled` | Disabled by configuration. |
| `backend_unavailable` | Backend missing, unusable or not functionally verified. |
| `target_unknown` | `target_id` is not declared in configuration. |
| `policy_denied` | Target or arguments denied by local policy. |
| `quota_exceeded` | Concurrency, artifact or collection quota exhausted. |
| `run_not_found` | Unknown run id or missing run directory. |
| `invalid_cursor` | Malformed cursor, or reused with other filters or another run. |
| `report_unavailable` | Run exists but its normalized report is missing or not yet written. |
| `report_invalid` | Stored report violates its schema or internal invariants. |
| `report_too_large` | Stored report exceeds the API read bound (bytes or record count). |
| `artifact_unavailable` | Requested artifact is missing or rejected by the store. |
| `canceled` | The caller's context was canceled. |
| `timeout` | The caller's context deadline expired. |
| `server_closed` | The analysis service is closed. |
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

The cache only accelerates repeated pagination. It is keyed by the opaque run id
and invalidated when the stored report size or modification time changes. Entries
whose estimated memory cost exceeds the cache bound are served by the streaming
reader instead.

## Error behaviour

Unknown tools and invalid arguments are rejected by the MCP layer. A backend that
disappears after startup does not crash the server: `analysis_start` is removed
from `tools/list` at the next capability refresh, and invocation revalidates the
backend and returns `backend_unavailable` rather than a false "no errors".

The server never reports "no errors" when a backend is missing, a probe is
incomplete, XML is truncated, parsing fails, output is truncated or a response
bound omitted diagnostics. Those cases produce `incomplete`/`tool_error` or an
explicit warning, and `analysis_complete=false` where applicable.
