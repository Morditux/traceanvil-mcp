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

## 0.0.0 — 2026-10-18

- J0: Go module, version command, project documentation, ADRs, CI quality gate,
  read-only preflight script, Apache-2.0 license.
