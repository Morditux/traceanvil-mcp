# Run artifacts

J2 writes one private directory per job under the configured `report_dir`:

```text
<report_dir>/
└── run_<32 hex>/
    ├── result.json
    ├── stdout.log
    └── stderr.log
```

`run_<32 hex>` comes from `crypto/rand`. The directory is created with mode
`0700`; files are created with mode `0600`, `O_NOFOLLOW` where applicable and
atomic temporary-file rename for `result.json`.

Artifacts are not exposed through MCP yet. `analysis_artifact` and access
control arrive with J4.

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

## Limits

| Limit | Default | Enforcement |
|---|---|---|
| Bytes per run | 10 MiB | `artifacts.Store` / `QuotaWriter` |
| Files per run | 64 | `artifacts.RunDir` |
| Captured stdout + stderr | 10 MiB minus 64 KiB manifest reserve, or explicit manager option | `runner` shared budget |
| Concurrent jobs | Manager option (configuration `max_jobs` in J4) | `jobs.Manager` |
| Execution timeout | 120 s | `runner` / job manager |
| TERM → KILL grace | 1 s | `runner` |

Output beyond the byte budget is discarded and marked truncated; the child is
not blocked. A non-streaming artifact write that exceeds the quota returns
`ErrQuota` / `ErrFileQuota`.

There is no global retention or total-disk quota yet. Completed jobs remain in
the manager map for status/report access; retention and cross-session access
control arrive with J4/J14.
