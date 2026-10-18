# ADR 0012 — Helgrind, DRD and sanitizer adapters

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J9

## Context

J3/J4 built one Valgrind XML pipeline and one Memcheck report. J9 must recognize
instrumented concurrency errors from Helgrind, DRD, ASan, UBSan, LSan and TSan,
while preserving the rule that a clean concurrency run does not prove absence.
The server does not build targets, and sanitizer binaries require compile-time
instrumentation.

## Decision

### Helgrind and DRD

- Reuse the bounded Valgrind XML parser, job manager and artifact store.
- Add an explicit Valgrind `Tool` (`memcheck`, `helgrind`, `drd`) with its own:
  - raw XML artifact name (`memcheck.xml`, `helgrind.xml`, `drd.xml`);
  - diagnostic origin (`valgrind-*`);
  - severity policy;
  - capability ID `backend.valgrind.<tool>`.
- Keep Memcheck options tool-specific; Helgrind/DRD receive only the common
  `--num-callers` and suppression controls.
- Publish `helgrind_run` and `drd_run` separately; `analysis_start` remains
  Memcheck-only. Reports record `options.tool` and the shared status/report
  tools work for all three.
- Concurrency results are explicitly non-deterministic; suppressed reports are
  never treated as resolved.

### Sanitizers

- Targets declare instrumentation explicitly with a `sanitizers` array
  (`asan`, `ubsan`, `lsan`, `tsan`). The server never infers instrumentation
  from a name and never compiles or links a target.
- Add a bounded line-oriented parser for each runtime. Unknown or truncated
  output is incomplete, never clean. UBSan exit code zero is not considered
  authoritative; parsed diagnostics are.
- Add a sanitizer execution service on the existing jobs/runner/artifact
  foundation. It captures the sanitizer-owned `sanitizer.log` via a fixed
  `log_path` as the raw evidence, never parses target stderr as sanitizer
  evidence, and writes the same `analysis.json` schema.
- Add `asan_run`, `ubsan_run`, `lsan_run` and `tsan_run` plus per-tool
  `backend.sanitizer.<tool>` capabilities. Each capability is
  configuration-only: it becomes available when the backend is enabled and at
  least one usable target declares that sanitizer. The declaration is an
  administrator trust statement; the capability detail records that limitation.
- A clean sanitizer run is `no_errors_observed` with an explicit warning that
  absence is not proof. A nonempty log with no recognized diagnostic, a nonzero
  exit without a diagnostic, truncation or a signal makes the report incomplete.
- Every sanitizer's parser, environment options and bounds are fixed; no
  client-controlled environment or free-form sanitizer flags are accepted.

### Shared analysis API

- `analysis.Service` now selects a backend by tool from a validated map.
- `helgrind_run`, `drd_run` and the sanitizer tools reuse `analysis_status`,
  `analysis_report`, `analysis_artifact` and `diagnostics_diff`.
- `analysis_report` exposes `analysis_tool`; raw sanitizer evidence uses
  artifact type `raw_log`.
- Diff refuses reports from different analysis tools (Memcheck vs Helgrind,
  ASan vs TSan, etc.). Program binary/outcome changes remain reported changes;
  parser/options/arguments/suppression differences downgrade to uncertain.

## Consequences

- One runner, DAP-independent artifact model and one report schema cover all J9
  tools.
- The server can recognize instrumented concurrency errors without claiming
  exhaustiveness.
- Sanitizer trust is explicit in configuration; an undeclared target is
  rejected before execution.
- ASan/UBSan/LSan/TSan output formats and exit codes differ; their parsers and
  limitations are documented and tested independently.
- A clean concurrency run remains a bounded observation, never a proof of
  absence.
