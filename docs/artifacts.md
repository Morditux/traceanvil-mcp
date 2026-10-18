# Run artifacts

J2 writes one private directory per job under the configured `report_dir`. J3
Memcheck runs add the raw Valgrind XML and the canonical report:

```text
<report_dir>/
└── run_<32 hex>/
    ├── result.json      job manifest (J2)
    ├── analysis.json    canonical diagnostic report (J3)
    ├── memcheck.xml     raw Valgrind XML evidence (J3)
    ├── stdout.log
    └── stderr.log
```

`analysis.json` and `memcheck.xml` are produced only by the internal Memcheck
pipeline. Since J4 they are readable through `analysis_artifact` as bounded
excerpts, and `analysis_report` pages the normalized diagnostics. `analysis.json`
is not a substitute for the raw XML: it is a normalized, lossy-for-unknown-fields
view.

`run_<32 hex>` comes from `crypto/rand`. The directory is created with mode
`0700`; files are created with mode `0600`, `O_NOFOLLOW` where applicable and
atomic temporary-file rename for `result.json`.

## MCP artifact access (J4)

`analysis_artifact` accepts a logical type, never a path:

| Type | File |
|---|---|
| `raw_xml` | `memcheck.xml` |
| `analysis_report` | `analysis.json` |
| `stdout` | `stdout.log` |
| `stderr` | `stderr.log` |
| `job_manifest` | `result.json` |

The run id must match `^run_[0-9a-f]{32}$` and name an existing run directory of
the configured store. The directory is rejected if it is a symbolic link; the
file is opened with `O_NOFOLLOW|O_NONBLOCK`, must be a regular file, and the file
name is chosen from the allowlist. Excerpts default to 64 KiB, are hard-capped at
1 MiB, and continue with `next_offset`. When an excerpt stops before EOF, a
trailing partial UTF-8 sequence is moved back to the next rune boundary unless
that would return zero bytes; otherwise invalid bytes are replaced with U+FFFD
and flagged. Absolute filesystem paths are never returned. See
[mcp.md](mcp.md#analysis_artifact).

The normalized report format and its invariants are documented in
[valgrind.md](valgrind.md#canonical-report).

## `result.json`

The manifest is written after every execution path, including infrastructure
failures:

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "target_id": "fixture",
  "state": "completed",
  "created_at": "2026-10-18T12:00:00Z",
  "started_at": "2026-10-18T12:00:00.010Z",
  "finished_at": "2026-10-18T12:00:00.050Z",
  "program_result": {
    "started": true,
    "exit_code": 7,
    "timed_out": false,
    "canceled": false,
    "duration_ms": 40,
    "stdout_bytes": 6,
    "stderr_bytes": 0,
    "stdout_truncated": false,
    "stderr_truncated": false,
    "output_incomplete": false
  }
}
```

| Field | Meaning |
|---|---|
| `state` | `queued`, `running`, `completed`, `cancelled`, `failed` or `timed_out`. |
| `program_result.started` | Whether a process was actually launched. |
| `exit_code` | Program exit code; `-1` when terminated by a signal. |
| `signal` | Signal number when the program was signaled (omitted otherwise). |
| `timed_out`, `canceled` | Runner termination reason. |
| `stdout_bytes`, `stderr_bytes` | Bytes actually captured (shared budget). |
| `stdout_truncated`, `stderr_truncated` | Output exceeded the budget. |
| `output_incomplete` | A grandchild kept pipes open past `WaitDelay`. |
| `error` | Infrastructure error, if any. |

A non-zero program exit code with `state=completed` is a normal program result.
Only runner, cancellation, timeout and artifact failures change the job state.

## `analysis.json` (J3)

The canonical report uses `schema_version = 1` and records:

- the tool name, resolved path and version, `result`, `analysis_complete` and
  the program outcome;
- the target id, absolute program path, SHA-256, arguments and duration;
- start/end timestamps, OS/arch, workspace roots, parser and fingerprint
  versions;
- the allowlisted Valgrind options;
- a summary (`total` occurrences vs `diagnostic_records`, `by_kind`,
  `by_severity`, leaked bytes/blocks, suppressed count, truncation);
- diagnostics with kind, severity, message, normalized location/stack, context
  and origin stacks, occurrences, fingerprint and provenance
  (`artifact`, `record_id`, `record_index`);
- suppression counts, artifact digests and warnings.

If the artifact quota cannot hold the full report, a compact report is stored
with `result=incomplete`, `analysis_complete=false`, empty diagnostics,
`summary.truncated=true` and an explicit warning. The raw XML is still kept (up
to its own quota). See [valgrind.md](valgrind.md).

## Limits

| Limit | Default | Enforcement |
|---|---|---|
| Bytes per run | 10 MiB | `artifacts.Store` / `QuotaWriter` / `RunDir.Adopt` |
| Files per run | 64 | `artifacts.RunDir` |
| Captured stdout + stderr | 10 MiB minus 64 KiB manifest reserve, or explicit manager option | `runner` shared budget |
| Raw Valgrind XML | 8 MiB default (`MaxRawXMLBytes`), minus a 128 KiB report reserve | size watchdog, then adoption truncation |
| Concurrent jobs | Manager option (configuration `max_jobs`) | `jobs.Manager` |
| Execution timeout | `limits.timeout_seconds`, or a shorter request `timeout_ms` | `runner` / job manager |
| TERM → KILL grace | 1 s | `runner` |
| Diagnostic page | 1 MiB, `min(max_response_items, 500)` items | `analysis.Service` |
| Artifact excerpt | 64 KiB default, 1 MiB hard cap | `analysis.Artifact` |

Output beyond the byte budget is discarded and marked truncated; the child is
not blocked. A non-streaming artifact write that exceeds the quota returns
`ErrQuota` / `ErrFileQuota`.

Persisted runs are bounded **when retention is applied** by
`storage.max_age_days`, `storage.max_runs` and `storage.max_bytes`. With
`purge_on_start=false` (the default), disk use is not automatically bounded;
run the explicit `purge` command or opt into startup purge. The command applies
the policy, with
`--dry-run` for inspection; `storage.purge_on_start` is an opt-in startup pass.
Purge only removes direct `run_<32 hex>` directories owned by the current user,
never follows symlinks or removes unknown entries, and must be run while the
server is stopped. J4 keeps up to 256 live analysis handles in memory; after
eviction, evidence tools read the persisted manifest and report, so a run id
remains usable while its directory exists. There is no cross-session access
control because the server is a single local stdio process and the opaque run id
is the capability. The raw XML bound is enforced by polling and post-run
truncation, not by a kernel quota, and a target can create untracked files in
its `TMPDIR` (the run directory); purge accounts for all regular files in a run
directory. This is a documented local-trust limitation.
