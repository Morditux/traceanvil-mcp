# ADR 0007 — Analysis API, pagination and bounded evidence access

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J4

## Context

J3 produced a Memcheck pipeline and a canonical `analysis.json`, but no MCP tool
could launch or inspect it. J4 must make the whole workflow drivable by an MCP
client: start a run, observe progress, cancel it, page through diagnostics,
compare counts, and read the raw evidence. It must do so without exposing a
filesystem API, without unbounded responses and without turning partial evidence
into a clean result.

The plan's request catalogue names `analysis_start`, `analysis_status`,
`analysis_cancel`, `analysis_report` and `analysis_artifact`, plus "summary
compacte + détails paginés, filtres, API errors nommées". It does not define the
filter shape, the pagination semantics, the cursor format, the artifact type
vocabulary or the per-record/per-page bounds. This ADR records those choices.

## Decision

### Layering

- `internal/analysis` owns the API-facing behaviour: request validation, named
  errors, backend gating, status/disk fallback, pagination, cursors, response
  bounds, artifact excerpts and status/report consistency checks.
- `internal/mcpserver/analysis.go` only registers tools and delegates to the
  service. There is no business logic in the handlers.
- `internal/analysis.Backend` and `analysis.Run` are small interfaces; the
  production adapter wraps `*valgrind.Service` and `*valgrind.Analysis`. Tests
  use fakes. No universal debugger/analyzer abstraction is introduced.
- `serve` constructs and owns the artifact store, policy resolver, job manager,
  Memcheck service and analysis service, and closes them in reverse order:
  analysis cache, Memcheck service (cancels and waits for analyses), job manager.

### Tool exposure and capability changes

- `analysis_start` requires `backend.valgrind = available`. The other four tools
  do not: they never launch a program, and they remain usable for an in-flight
  run or a run persisted by an earlier server process. When no such run exists
  they return `run_not_found`.
- Capabilities are probed at startup and every 30 seconds, independent of tool
  calls. `Server.SyncTools` reconciles the exposed set and emits
  `tools/list_changed`. A probe only runs fixed tool arguments; it never runs a
  user program.
- Every `analysis_start` revalidates configuration, capability state and the
  Valgrind binary. A backend that disappears after publication produces
  `backend_unavailable`, not a false result.

### Request contract

- `target_id` is a configuration identifier, never a path. The target's
  configured `cwd` is used; the plan's `cwd_id?` is not implemented because the
  configuration has no directory catalogue and adding one would widen policy.
- `tool` defaults to `memcheck`; any other value is `unknown_tool`.
- `args` remain a multiset subset of the target's `allowed_args`, enforced by
  `internal/policy` immediately before launch.
- `options` is a strict, typed subset of the Memcheck surface. There is no
  free-form Valgrind argument string. `trace_children=true` stays rejected.
- `timeout_ms` is optional and may only shorten `limits.timeout_seconds`. It is
  carried through `jobs.Spec.Timeout` to the runner. A longer value is
  `invalid_argument`.

### Named errors

All API errors implement `analysis.Error` with a stable code and a message. The
MCP text starts with `"<code>: "`. Codes cover invalid input, unknown/disabled
tools, unavailable backends, unknown targets, policy denial, quotas, unknown
runs, invalid cursors, unavailable/invalid/oversized reports, unavailable
artifacts, cancellation, timeouts, closed service and internal failures.
Underlying causes are preserved with `Unwrap` but are not printed by `Error()`,
which keeps filesystem paths out of client-visible text. MCP schema-validation
errors occur in the SDK before the handler and are documented separately.

### Status

- Live runs use the Memcheck service snapshot plus the job snapshot. A run no
  longer tracked (retention, restart) is read from `result.json` and
  `analysis.json`.
- Job state, analysis phase, business result and program outcome are separate
  fields. `phase=analyzing` covers parsing/report writing after the process ends.
- A persisted `state=running` or `queued` carries a warning that it comes from the
  manifest and may be stale. Missing evidence yields `state=unknown` and
  `phase=unknown`, never an invented completion.
- Before a terminal phase, metric zeros are documented as "not known yet" and a
  warning states that the analysis is incomplete.

### Report pagination

- A page contains the full report summary (independent of filters), report-wide
  artifact metadata, warnings, and at most
  `min(limits.max_response_items, 500)` diagnostics.
- Filters are `kinds`, `severities`, `path_prefix`, `function`, `fingerprint` and
  `min_occurrences`. Lists are ORed internally; all active filters are ANDed.
  `path_prefix` and `function` must match the same frame among the primary,
  context and origin stacks.
- Pagination is over **matching readable diagnostics**. The cursor is an opaque
  base64url JSON value containing the run id, the matching offset and a SHA-256
  of the normalized filter. Reusing a cursor with another run or filter returns
  `invalid_cursor`. The cursor is not a capability: it cannot expose another
  run's data, and forging an offset only changes where the same report is read.
- One page's serialized diagnostics are bounded by 1 MiB. A single matching
  diagnostic larger than that is omitted, counted in `omitted_oversized`, the
  cursor advances past it, and a warning points to `analysis_artifact`. The raw
  evidence is preserved.
- A diagnostic record larger than the 4 MiB per-record read bound cannot be
  filtered safely: it is counted in `unreadable_diagnostics`, `scan_incomplete`
  is set, and the page states that matching counts are incomplete.
- `analysis.json` is read with `encoding/json` token streaming. Diagnostics are
  decoded one at a time. Header fields are bounded; report schema version, run
  id, tool, result, diagnostics, fingerprints, severities and evidence are
  validated. If all records are readable, the sum of occurrences must equal
  `summary.total` unless the summary is marked truncated.
- Repeated pagination must not be quadratic. A bounded LRU caches decoded reports
  (16 MiB per report, 64 MiB total, 16 entries), keyed by run id and invalidated
  when the file size or modification time changes. Larger reports use the
  streaming reader directly. At most two reports are decoded concurrently; a
  request that finds both slots busy streams its page instead of waiting. The
  cache only accelerates reads; it does not change validation or bounds.

### Artifact access

- `analysis_artifact` accepts logical types only: `raw_xml`, `analysis_report`,
  `stdout`, `stderr`, `job_manifest`. The mapping to file names is server-owned.
- Run ids must match `^run_[0-9a-f]{32}$`; the run directory must exist, be a
  real directory and belong to the configured store. The file is opened with
  `O_NOFOLLOW|O_NONBLOCK`, checked as a regular file and read as a bounded
  section. This rejects symlink and FIFO plants without blocking.
- Excerpts default to 64 KiB and are hard-capped at 1 MiB. `returned_bytes` and
  `next_offset` allow continuation. Invalid UTF-8 is replaced with U+FFFD and
  flagged. No filesystem path is returned.

### Bounds

| Bound | Value |
|---|---|
| Page items | `min(max_response_items, 500)`, default 50 |
| Page diagnostics bytes | 1 MiB |
| One diagnostic record | 4 MiB |
| Diagnostic records scanned per read | 50,000 (`report_too_large` beyond) |
| Artifact excerpt | 64 KiB default, 1 MiB hard cap |
| Report read | `min(max_report_bytes, 64 MiB)` |
| Decoded report cache | 16 MiB estimated memory per entry (report JSON plus a per-record allowance), 64 MiB total, 16 entries |
| Concurrent full-report loads | 2; further requests stream a page instead of waiting |
| Warnings | 32 entries, 4 KiB each |

## Consequences

- The complete J4 workflow is covered by a real stdio MCP end-to-end test and by
  a 20 000-diagnostic pagination test that asserts item and byte bounds.
- Oversized and unreadable diagnostics are visible as explicit counters and
  warnings. A client cannot mistake a bounded page for an exhaustive report.
- The API can read a run after the live handle has been evicted or the server has
  restarted, as long as its directory exists. There is no global retention or
  purge yet (J14) and no caller authentication beyond the local trust model; the
  opaque run id is the only evidence capability.
- The `cwd_id` input from the plan is intentionally absent until a directory
  catalogue has its own policy and tests.
- Filters over `path_prefix`/`function` require one frame to satisfy both; this
  is documented and tested. A future filter language may need an explicit
  same-frame/any-frame option.
- Diagnostics omitted because of the per-record or page bound cannot be retrieved
  through `analysis_report`; the raw artifact remains the source of truth. If
  this becomes common, a per-diagnostic detail tool with its own bound is the
  natural extension rather than raising the global page limit.
- The periodic probe interval is a constant, not configuration. Making it
  configurable is deferred until there is an operational need; the probes are
  cheap and bounded.
