# ADR 0017 — Release, retention and supply-chain artifacts

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J14

## Context

The project needs bounded persisted artifacts, an explicit purge operation,
reproducible release artifacts by hash, an SBOM and a CI matrix before v1.0.
The server must not silently delete evidence or claim an unavailable capability.

## Decision

- Add `storage.max_age_days`, `storage.max_runs` and `storage.max_bytes` with
  documented defaults. `storage.purge_on_start` is opt-in and runs before the
  server accepts requests.
- Add `purge --config ... [--dry-run]`. It removes only direct
  `run_<32 hex>` directories owned by the current user, never follows
  symlinks, never removes unknown entries and must be run while the server is
  stopped. The dry-run summary is JSON.
- Generate SBOMs from the Go build information embedded in a binary
  (`debug/buildinfo`/`debug.ReadBuildInfo`), so cross-compiled release binaries
  need not be executed. The format is minimal SPDX 2.3 with dependency purls and
  Go module `h1` values retained as labelled `go-module-sum` external
  references (they are directory hashes, not archive SHA-256 checksums).
  Licences remain `NOASSERTION` rather than invented. Document names include
  the artifact/architecture and creation time is supplied by the release
  process.
- Add `scripts/release.sh` to build Linux amd64/arm64 binaries with `-trimpath`,
  `-buildvcs=false` and stamped version/commit/date, emit per-binary SPDX SBOMs
  and sorted SHA256SUMS. The build date derives from `SOURCE_DATE_EPOCH` or the
  commit timestamp. GPG signing is optional and explicitly key-driven; the
  script never generates keys.
- Add a tagged release workflow that builds/upload unsigned artifacts. Signing
  is a separate human-controlled step unless a future ADR defines a protected
  signing environment.
- CI runs Ubuntu 22.04 and 24.04 with Go 1.25.x and stable, plus a bounded fuzz
  smoke job. `go mod tidy -diff` and `go mod verify` are part of the gate.

## Consequences

- Purge is a destructive operator action; dry-run and documented stop-the-server
  preconditions are mandatory. The same-user filesystem trust boundary remains.
- SBOM dependency names/versions come from the binary; absent licence metadata is
  not guessed.
- Release artifacts are not signed by default in CI. A release report must state
  whether checksums were signed and by which fingerprint.
- The large-log benchmark is informative only; no hardware-specific pass/fail
  threshold is encoded.
