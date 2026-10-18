# ADR 0003 — Project baseline, identity and repository layout

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J0

## Context

J0 must fix the Go module path, license, minimum toolchain and repository layout
before any package is written. The repository previously contained a nested
`mcp-native-diagnostics/` folder duplicating the specification documents, which the
root `.gitignore` implicitly ignored because of a binary-name pattern. The existing
`LICENSE` was MIT while the specification proposed Apache-2.0 "to confirm before
first publication".

## Decision

1. **Module path:** `github.com/Morditux/traceanvil-mcp`, matching the actual
   remote repository so `go install` and imports resolve.
2. **Binary name:** `mcp-native-diagnostics`. The command directory is
   `cmd/mcp-native-diagnostics`; the module path does not need to match the binary
   name.
3. **License:** Apache License 2.0. It replaces the MIT license and provides an
   explicit patent grant suitable for a developer tool. The copyright holder
   remains the repository owner.
4. **Go baseline:** `go 1.25.0` in `go.mod` (the minimum required by the MCP SDK).
   CI tests `1.25.x` and the current stable release on `ubuntu-24.04`. No
   `toolchain` line is pinned, so local newer toolchains work without forcing
   downloads.
5. **No external dependency in J0.** The MCP SDK is added and pinned in J1, where
   it is first imported. An unused dependency would be removed by `go mod tidy`
   and would hide the real import graph.
6. **Layout:** the repository root is the Go module root. Analysis packages are
   created by their milestones, not pre-created empty. New packages need a
   testability justification.
7. **Local specification documents** (`AGENTS.md`, `PLAN.md`,
   `INSTALL-UBUNTU.md`) are intentionally excluded from version control and listed
   in `.gitignore`. The nested duplicate folder was merged into `README.md` and
   removed.
8. **Scripts:** `scripts/check.sh` is the single quality gate used locally and in
   CI. `scripts/preflight.sh` is a read-only inventory; it never installs,
   executes user programs or edits kernel settings.
9. **CLI contract:** exit code `0` success, `1` runtime failure (reserved),
   `2` invalid usage. `version` writes to stdout; usage errors write to stderr.
10. **CI actions are pinned to commit SHAs**, with a version comment, and run with
    `contents: read` and `persist-credentials: false`.

## Consequences

- The nested `mcp-native-diagnostics/` directory no longer exists; its conceptual
  README content lives in the root `README.md`.
- Import paths use `github.com/Morditux/traceanvil-mcp/...` in all packages.
- Relicensing from MIT to Apache-2.0 is a deliberate, maintainer-approved change.
  Future contributors are bound by the Apache-2.0 terms.
- The module cannot use language features newer than Go 1.25 until the baseline is
  raised through a new ADR.
