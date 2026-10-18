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

### Changed

- J1: `version` now marks binaries built from a modified checkout with `-dirty`.
- J1: `cli.Run` takes a context and explicit stdin/stdout/stderr for testability.
- J2: no new MCP tool or CLI command; execution is internal until J4.

### Security

- J1: missing backends hide tools instead of failing startup.
- J1: backend discovery reports `unknown`, never `available`, until functional
  probes are implemented.
- J2: executed targets never inherit the server environment or stdin; artifacts
  are private, symlink-safe and quota-bounded.

## 0.0.0 — 2026-10-18

- J0: Go module, version command, project documentation, ADRs, CI quality gate,
  read-only preflight script, Apache-2.0 license.
