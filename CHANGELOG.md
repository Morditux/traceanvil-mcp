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

### Changed

- `version` now marks binaries built from a modified checkout with `-dirty`.
- `cli.Run` takes a context and explicit stdin/stdout/stderr for testability.

### Security

- Missing backends hide tools instead of failing startup.
- Backend discovery reports `unknown`, never `available`, until functional
  probes are implemented.

## 0.0.0 — 2026-10-18

- J0: Go module, version command, project documentation, ADRs, CI quality gate,
  read-only preflight script, Apache-2.0 license.
