# Changelog

All notable changes to this project are documented here. The project is not
released yet; milestones map to the development plan.

## Unreleased

### Added

- J1: MCP server over stdio (`serve`) using the official Go SDK v1.8.0.
- J1: strict JSON configuration with documented defaults and validation
  (`internal/config`, `docs/configuration.md`, `config.example.json`).
- J1: `validate-config` and read-only `doctor` commands.
- J1: capability registry with bounded, concurrent, panic-safe probes.
- J1: conditional tool publication and the `tools.listChanged` capability with
  list-changed notifications.
- J1: `system_capabilities` tool (`schema_version = 1`).
- J1: end-to-end test asserting that `serve` writes only JSON-RPC to stdout.
- J1: ADR 0004 on configuration and capability probing.
- J2: runtime policy resolution with symlink-aware root containment, executable
  checks and a multiset argument allowlist.
- J2: bounded runner with process groups, TERM/KILL grace, shared
  stdout/stderr budget, truncation flags and pipe-holding cleanup.
- J2: private run directories, opaque run IDs, `O_NOFOLLOW` file access, atomic
  writes and per-run byte/file quotas.
- J2: asynchronous job manager with concurrency quota, cancellation, states,
  `result.json` manifests and readable `stdout.log`/`stderr.log`.
- J2: tests for blocked programs, TERM-ignoring processes, process groups,
  orphan pipes, environment/stdin isolation, traversal, quotas and 100-run
  process leaks.
- J2: integration test covering policy → runner → artifacts → jobs.
- J2: ADR 0005 on execution, artifacts and job lifecycle.
- J3: canonical diagnostic model, path normalization and fingerprint v1
  (`internal/diagnostic`, `schema_version = 1`).
- J3: allowlisted Memcheck argument builder and strict option validation.
- J3: bounded streaming Valgrind XML parser (bytes, depth, diagnostics, frames,
  strings, suppressions) that keeps partial evidence and marks truncation.
- J3: Memcheck analysis service with private run directories, raw XML watchdog,
  quota adoption, exit-code classification and atomic `analysis.json`.
- J3: leak, still-reachable and suppression reporting without reclassifying
  suppressed errors as resolved.
- J3: functional Valgrind capability probe (`--version` plus
  `--tool=memcheck --help`), without running a user program.
- J3: `valgrind.suppressions` configuration profiles, confined to workspace
  roots and revalidated at invocation.
- J3: `RunDir.Adopt` artifact quota support and `jobs.Manager.StartInDir` for
  backends that must allocate files before launch.
- J3: C fixture corpus, static XML fixtures, parser fuzz seeds and real-Valgrind
  integration tests (invalid read/write, use-after-free, double free,
  uninitialised value, leaks, clean exit, SIGSEGV, timeout, cancellation,
  truncated XML, suppressions).
- J3: ADR 0006 and `docs/valgrind.md` on the pipeline, parser limits, report
  schema and limitations.
- J4: `analysis_start`, `analysis_status`, `analysis_cancel`, `analysis_report`
  and `analysis_artifact` MCP tools backed by the new `internal/analysis`
  package.
- J4: named API errors (`invalid_argument`, `backend_unavailable`,
  `run_not_found`, `invalid_cursor`, `report_invalid`, …) with stable codes.
- J4: compact report summary plus filtered, cursor-paginated diagnostics, with
  item and byte budgets, explicit oversize/unreadable accounting and no silent
  omissions.
- J4: bounded artifact excerpts for raw XML, normalized report, stdout, stderr
  and job manifest; logical artifact types only, no paths. Excerpt boundaries
  back off to complete UTF-8 runes when possible and always make progress on
  windows smaller than one rune.
- J4: per-request `timeout_ms` that can only shorten the configured maximum, and
  service wiring for `report_dir`, `max_report_bytes`, `timeout_seconds`,
  `max_jobs` and `max_response_items`.
- J4: status and reports for runs persisted by a previous server process, read
  from `result.json`/`analysis.json` with explicit staleness warnings.
- J4: periodic capability revalidation (30 s) with independent
  `tools/list_changed` notifications; `analysis_start` appears/disappears with
  `backend.valgrind` while evidence tools stay published.
- J4: bounded decoded-report cache (LRU, 16 MiB per report, 64 MiB total) so
  repeated pagination does not rescan multi-megabyte JSON.
- J4: tests for 20 000-diagnostic pagination, filters, cursor scoping, report
  corruption, artifact bounds, FIFO/symlink rejection, cancellation, per-request
  timeout and the full stdio MCP workflow.
- J4: ADR 0007 and updated `docs/mcp.md`, `docs/valgrind.md`,
  `docs/artifacts.md`, `docs/configuration.md` and README.
- J5: internal generic DAP client (`internal/dap`) with strict
  `Content-Length` framing, UTF-8 byte lengths, bounded header/frame sizes and
  typed protocol errors.
- J5: request sequence allocation, pending response futures, out-of-order
  correlation, late-response accounting, per-request contexts and a pending
  request quota.
- J5: single-owner reader/writer goroutines, bounded write queue, event channel
  with explicit backpressure, write deadlines when the transport supports them,
  idempotent shutdown and EOF handling.
- J5: session state machine (`created` … `closed`), explicit
  `BeginLaunch`/`ConfirmRunning`, stop-on-entry and terminal event transitions,
  plus backend-driven `cancelled`/`timed_out` transitions.
- J5: capability negotiation with a typed subset, raw `Supports` queries and
  `initialized` response/event separation.
- J5: adapter-initiated reverse requests with bounded concurrency, optional
  handler, panic recovery and explicit rejection by default.
- J5: reusable scripted in-memory adapter (`internal/dap/daptest`) with
  independent framing.
- J5: DAP tests for fragmented/malformed/oversized frames, UTF-8 lengths,
  out-of-order and late responses, EOF/Close, pending and backpressure limits,
  blocked transports, capabilities, reverse requests, state transitions and a
  frame-decoder fuzz target.
- J5: ADR 0008 and `docs/dap.md`; README/SECURITY updated.
- J6: GDB DAP backend (`internal/debugger`) with fixed hardened GDB arguments,
  private `HOME`/`TMPDIR`, a `/dev/null` inferior tty, GDB process-group cleanup
  and verified debuggee termination.
- J6: `debug_start`, `debug_status`, `debug_set_breakpoints`, `debug_continue`,
  `debug_pause`, `debug_step`, `debug_threads`, `debug_stack_trace`,
  `debug_scopes`, `debug_variables` and `debug_stop` MCP tools.
- J6: backend-neutral session manager with opaque `dbg_` ids, live-session
  quota, idle expiry, terminal retention and orderly shutdown.
- J6: GDB DAP functional capability probe (initialize handshake plus the same
  hardening commands as a session, without loading a target); `backend.gdb` is
  `available` only after both succeed.
- J6: async DAP request futures and bounded GDB `launch`/`configurationDone`
  ordering.
- J6: workspace-confined source resolution for breakpoints, normalized
  workspace-relative stack sources, explicit stripped-symbol degradation and
  bounded threads/frames/scopes/variables.
- J6: real GDB integration and stdio MCP tests, including stop-on-entry,
  breakpoints, continue, pause, step, locals, stripped binaries, stop while
  running, debuggee PID termination and process cleanup.
- J6: ADR 0009 and `docs/debugger.md`; README, SECURITY, configuration, MCP and
  compatibility documentation updated.
- J7: LLDB DAP backend through `lldb-dap`, sharing the J6 DAP client, session
  manager, policy, pagination and MCP tools.
- J7: versioned `lldb-dap` discovery (`lldb-dap`, `lldb-dap-<N>`) in absolute
  PATH directories, with a functional `initialize` handshake before
  `backend.lldb` becomes available.
- J7: adapter-specific startup ordering for lldb-dap (`launch` before
  `initialized`), zero DAP sequence acceptance, process-event thread seeding and
  SIGSTOP stop-on-entry handling.
- J7: real LLDB integration and stdio MCP tests covering stop-on-entry,
  breakpoints, continue, pause, step, threads, stack/scopes/locals, stripped
  symbols, explicit detach refusal, debuggee PID termination and cleanup.
- J7: hardening settings for LLDB (private HOME, cwd `.lldbinit` and external
  symbol lookup disabled, private PTY) and a documented GDB/LLDB difference
  matrix.
- J7: ADR 0010, `docs/debugger.md` and compatibility documentation updated;
  `backend.lldb` is available on the reference machine after the DAP handshake.
- J8: fingerprint v2 with length-prefixed fields, normalized paths and no
  source-line dependency; v1 remains readable for matching persisted reports.
- J8: diagnostic diff core with new/resolved/persistent/changed/uncertain
  classification, duplicate-fingerprint uncertainty and conservative
  configuration/evidence compatibility checks.
- J8: `diagnostics_diff` read-only MCP tool, bounded pages, pair-bound cursors,
  `incompatible_runs` and `diff_too_large` named errors.
- J8: deterministic golden base/target/expected snapshots in
  `testdata/golden/diff/`.
- J8: ADR 0011 and `docs/diff.md`; README, SECURITY, MCP and compatibility
  documentation updated.
- J9: Helgrind and DRD adapters on the Valgrind XML pipeline, with tool-specific
  origins, severity policies, raw XML names and `helgrind_run`/`drd_run` tools.
- J9: per-tool Valgrind capabilities (`backend.valgrind.helgrind`,
  `backend.valgrind.drd`) verified with fixed `--tool=<tool> --help` probes.
- J9: bounded ASan, UBSan, LSan and TSan log parsers with explicit incomplete
  evidence on truncation, unknown output, signals or unexplained nonzero exits.
- J9: `asan_run`, `ubsan_run`, `lsan_run` and `tsan_run`, target `sanitizers`
  declarations and per-tool configuration-only `backend.sanitizer.<tool>`
  capabilities; sanitizer evidence is stored as `sanitizer.log`/`raw_log`.
- J9: multi-backend analysis service, `analysis_tool` report field and
  `raw_log` artifacts; diff refuses different analysis tools.
- J9: concurrency/sanitizer C fixtures, real Helgrind/DRD and real
  ASan/UBSan/LSan/TSan integration tests, and ADR 0012/`docs/sanitizers.md`.

### Changed

- J1: `version` now marks binaries built from a modified checkout with `-dirty`.
- J1: `cli.Run` takes a context and explicit stdin/stdout/stderr for testability.
- J2: no new MCP tool or CLI command; execution is internal until J4.
- J3: `backend.valgrind` becomes `available` when the functional Memcheck/XML
  probe succeeds; GDB and LLDB remain discovery-only.
- J3: no new MCP tool or CLI command; Memcheck is internal until J4.
- J3: `system_capabilities` warnings and documentation now describe the J3
  state; the compatibility inventory was refreshed (Valgrind 3.26.0, LLDB/Clang
  21.1.8).
- J4: `serve` now constructs the artifact store, job manager, policy resolver,
  Memcheck service and analysis service, and closes them in order on shutdown.
- J4: `analysis_start` is gated on `backend.valgrind`; evidence tools remain
  available for existing runs. Backend availability is revalidated at every
  invocation.
- J4: `system_capabilities` reports the effective page and artifact bounds and
  the J4 limitation warnings.
- J4: `jobs.Spec.Timeout` and `valgrind.Request.Timeout` carry a per-run timeout;
  the manager clamps values above its own limit.
- J5: no MCP tool or CLI command is added. The DAP client is internal until J6;
  GDB and LLDB capability records remain `unknown`.
- J6: `backend.gdb` becomes `available` after the DAP initialize probe; the GDB
  `debug_start` is published while it is available and a target currently resolves
  under policy; the other debug action tools require the available capability.
  `debug_status`/`debug_stop` remain for retained sessions.
- J6: `serve` constructs and closes the debugger service and session manager
  with the other runtime components. No new CLI command or configuration field
  is added; `limits.max_sessions` now bounds debugger sessions.
- J7: debug action tools are published while either `backend.gdb` or
  `backend.lldb` is available; `debug_start` accepts `backend: "lldb"` in
  addition to the default `"gdb"`. `debug_status` and `debug_stop` remain
  available for a retained session of either backend.
- J7: the LLDB capability is no longer discovery-only. The LLDB probe launches
  the adapter with fixed init settings and verifies the DAP handshake without
  loading a target.
- J7: the session implementation is backend-neutral (`backend.go`, `session.go`,
  `process.go`) instead of a GDB-only file.
- J8: new analysis reports use `sha256-v2`; report readers accept v1 and v2, but
  a diff requires both runs to use the same algorithm. Report compatibility
  metadata is now parsed for comparisons without changing `analysis_report`.
- J8: `diagnostics_diff` is always published with the analysis service, even
  when `backend.valgrind` is unavailable. It never launches a program.
- J9: `analysis_start` remains Memcheck-only; `helgrind_run`, `drd_run` and the
  sanitizer tools select their tool explicitly. `analysis_status` and
  `analysis_report` expose the selected analysis tool.
- J9: sanitizer targets must declare their instrumentation; enabling the
  sanitizer backend alone does not expose a `<tool>_run` tool without a matching
  declaration.

### Security

- J1: missing backends hide tools instead of failing startup.
- J1: backend discovery reports `unknown`, never `available`, until functional
  probes are implemented.
- J2: executed targets never inherit the server environment or stdin; artifacts
  are private, symlink-safe and quota-bounded.
- J3: Valgrind XML is treated as untrusted input with byte/depth/record/string
  bounds; truncation and parse failures can never produce "no errors".
- J3: raw XML is adopted with `O_NOFOLLOW`, truncated to the quota and mode
  `0600`; `analysis.json` is written atomically.
- J3: suppression profiles are configuration-only, workspace-confined and
  revalidated at invocation; suppressed errors are never "resolved".
- J3: `trace_children=true` is rejected until child XML documents are collected
  and attributed.
- J4: artifact reads use logical type allowlists, `O_NOFOLLOW|O_NONBLOCK`,
  regular-file checks and `run_<32 hex>` validation; FIFO and symlink plants are
  rejected without blocking or exposing filesystem paths.
- J4: persisted reports are treated as untrusted: schema, run id, diagnostics,
  fingerprints and summary totals are validated; inconsistency is
  `report_invalid`, never an empty clean result.
- J4: every analysis response is bounded by items and bytes; cache size is
  bounded; oversized or unreadable diagnostics are reported explicitly.
- J4: capability refreshes only execute fixed tool probes, never user programs.
- J5: DAP frames are bounded in header and payload size before allocation;
  malformed, truncated or oversized frames become typed protocol failures, not
  partial success.
- J5: exactly one reader and one writer own the DAP stream; writes are serialized
  and bounded, and a slow event consumer applies backpressure instead of growing
  memory.
- J5: request futures are bounded, late responses are ignored and counted, and
  Close/EOF unblock pending requests without leaking goroutines.
- J5: reverse requests are rejected by default; a configured handler is bounded,
  panic-safe and receives a context canceled by Close.
- J5: the DAP client launches no debugger and exposes no new MCP surface.
- J6: GDB runs with `--nx`, auto-load and debuginfod disabled,
  startup-with-shell off, a private HOME and no client-supplied flags, commands
  or scripts; the debuggee cannot read/write the DAP pipe.
- J6: source paths are symlink-resolved and workspace-confined; external source
  paths, instruction pointers, module paths and raw memory references are
  omitted.
- J6: sessions are bounded by quota/idle/retention; `debug_stop` terminates by
  default and detach is not exposed at J6. No attach, expression evaluation or
  raw memory access is exposed.
- J6: debugger pages and values are bounded; missing symbols are reported
  explicitly.
- J7: lldb-dap runs with cwd/user init loading and external/debuginfod symbol
  lookup disabled, a private HOME and a private PTY; no client-supplied LLDB
  command, script or init file is accepted.
- J7: LLDB's stop-on-entry is surfaced as a SIGSTOP exception warning, thread
  and frame ids are treated as opaque, and large adapter events are drained and
  bounded.
- J7: detach remains refused for both backends even though each advertises
  `supportTerminateDebuggee`; termination and teardown (stop, close, startup
  failure) use a best-effort disconnect plus group cleanup, with fixture PID
  checks.
- J8: diff output never turns incompatible metadata into an empty/resolved
  result. Downgrading configuration/evidence changes mark every item uncertain;
  binary/path/outcome changes are reported without discarding classification.
  Hard incompatibilities fail with `incompatible_runs`.
- J8: diff items are compact (no full stacks), pages are item/byte bounded, and
  cursors are bound to both run ids. Duplicate fingerprints and truncation are
  explicit.
- J9: sanitizer logs are untrusted, byte/line/issue/frame bounded evidence. A
  clean concurrency run is never presented as proof of absence; unknown or
  truncated sanitizer output is incomplete.
- J9: sanitizer instrumentation is an explicit target declaration, not inferred
  from a filename; no client environment or sanitizer flags are accepted.

## 0.0.0 — 2026-10-18

- J0: Go module, version command, project documentation, ADRs, CI quality gate,
  read-only preflight script, Apache-2.0 license.
