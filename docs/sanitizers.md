# Sanitizer runs (J9)

`asan_run`, `ubsan_run`, `lsan_run` and `tsan_run` execute an allowlisted target
that the configuration declares as built with the matching sanitizer runtime:

Enable the backend in configuration:

```json
{"backends": {"sanitizers": true}}
```

Declare each instrumented target:

```json
{
  "id": "fixture-asan",
  "program": "/workspace/build/fixture",
  "cwd": "/workspace",
  "sanitizers": ["asan"]
}
```

The server **does not compile, link or instrument** binaries. A declaration is an
administrator trust statement: if the binary is not actually instrumented, the
run can produce no evidence. The service marks a no-error run as
`no_errors_observed` with an explicit warning; it never presents the absence of
sanitizer output as proof that no bug exists. For concurrency tools, a clean run
is especially non-deterministic.

## Supported tools

| Tool | Runtime output | Typical exit |
|---|---|---|
| `asan` | AddressSanitizer error blocks and stacks | nonzero on error |
| `ubsan` | `path:line:col: runtime error: ...` lines and optional stacks | often zero |
| `lsan` | LeakSanitizer direct/indirect/possibly leak blocks | 23 on leaks |
| `tsan` | ThreadSanitizer data-race/lock-order blocks and stacks | 66 on races |

The target's `sanitizers` array must contain the requested tool. A missing
declaration is a typed `backend_unavailable` error before execution. The
sanitizer backend itself must be enabled in `backends.sanitizers`. Each
`backend.sanitizer.<tool>` capability is configuration-only because the server
cannot prove a target binary is instrumented without running it. It is available
only when at least one declared target is currently usable and declares that
tool (executable program and working directory present).

## Environment

The target receives the standard minimal environment (`PATH`, `LANG`, `LC_ALL`,
and a private `TMPDIR`) plus fixed runtime options:

- `ASAN_OPTIONS=detect_leaks=0:abort_on_error=0`
- `UBSAN_OPTIONS=print_stacktrace=1:halt_on_error=0`
- `LSAN_OPTIONS=report_objects=0`
- `TSAN_OPTIONS=halt_on_error=0`
- every runtime also receives `log_path=<private run directory>/sanitizer-log`, so
  target stderr is not trusted as sanitizer evidence

No client-controlled environment is accepted. The target runs without a shell
and without inherited stdin.

## Parsing and evidence

- Sanitizer runtime output is captured in `sanitizer.log` (artifact type
  `raw_log`) via the fixed `log_path` option. Target stderr remains a separate
  `stderr` artifact and is never parsed as sanitizer evidence.
- The parser is line-oriented and bounded by bytes, lines, issues, frames and
  string length. Truncated or unrecognized output makes the report incomplete.
- Each diagnostic keeps `origin=sanitizer-<tool>`, a tool-specific kind,
  severity, message, location and symbolized stack. Fingerprints use v2.
- Addresses, PIDs and thread ids are not part of the fingerprint.
- A non-empty log with no recognized diagnostic is `incomplete`, never a clean
  result. A nonzero process exit with no diagnostic is also `incomplete`.
- A clean run with exit zero is `no_errors_observed` but carries the warning that
  absence is not proof. `analysis_complete` means the evidence and invocation
  were complete, not that the program is race-free or leak-free.

## Limits and reports

Sanitizer runs use the shared job manager, timeout, process-group cleanup,
artifact quota and retention. `analysis_status`, `analysis_report`,
`analysis_artifact` and `diagnostics_diff` work on sanitizer run ids like any
other analysis. The report exposes `analysis_tool` and the raw log artifact.
Page and value limits are identical to Memcheck analysis.

## Known limitations

- Instrumentation trust is operator-provided; the server does not inspect ELF
  sections or run a calibration program.
- UBSan may exit zero despite diagnostics; parsing, not exit status, is
  authoritative for errors. A nonzero exit without a recognized diagnostic is
  incomplete, not a clean result.
- TSan may be unable to run under constrained ASLR/container settings; the
  failure is reported as incomplete/tool_error, not as a clean run.
- `log_path` separates sanitizer runtime output from target stderr, but a
  hostile target runs as the same user and can write into the report tree.
  Provenance protects against accidental noise, not a malicious same-user
  process; use system isolation for hostile targets.
- Symbolization depends on target debug information and runtime symbolizers.
- Concurrency reports vary between runs; use repeated runs and the non-determinism
  warning rather than treating one clean run as proof.
