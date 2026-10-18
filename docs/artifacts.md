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
pipeline. They are not exposed through MCP until J4's `analysis_artifact`, and
`analysis.json` is not a substitute for the raw XML: it is a normalized,
lossy-for-unknown-fields view.

`run_<32 hex>` comes from `crypto/rand`. The directory is created with mode
`0700`; files are created with mode `0600`, `O_NOFOLLOW` where applicable and
atomic temporary-file rename for `result.json`.

Artifacts are not exposed through MCP yet. `analysis_artifact` and access
control arrive with J4. The Memcheck report format and its invariants are
documented in [valgrind.md](valgrind.md#canonical-report).

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
| Concurrent jobs | Manager option (configuration `max_jobs` in J4) | `jobs.Manager` |
| Execution timeout | 120 s | `runner` / job manager |
| TERM → KILL grace | 1 s | `runner` |

Output beyond the byte budget is discarded and marked truncated; the child is
not blocked. A non-streaming artifact write that exceeds the quota returns
`ErrQuota` / `ErrFileQuota`.

There is no global retention or total-disk quota yet. Completed jobs remain in
the manager map for status/report access; retention and cross-session access
control arrive with J4/J14. The raw XML bound is enforced by polling and
post-run truncation, not by a kernel quota, and a target can create untracked
files in its `TMPDIR` (the run directory). This is a documented local-trust
limitation.
