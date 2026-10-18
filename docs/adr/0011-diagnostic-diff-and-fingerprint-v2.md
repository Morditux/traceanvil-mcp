# ADR 0011 — Versioned fingerprints and diagnostic comparison

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J8

## Context

J4 exposed bounded analysis reports, but comparing two runs was left to the
client. A useful comparison must identify new, resolved, persistent and changed
diagnostics without treating line movement, configuration changes or incomplete
evidence as proof of a bug fix. Fingerprints are heuristics over tool-reported
stacks, not stable object identities.

The v1 fingerprint includes the source line of every selected frame. A small
edit that moves a line but leaves the same function and path changes the digest,
creating false new/resolved pairs. The canonical report already carries
`fingerprint_algorithm`, tool, parser, options, roots, program hash/arguments and
suppressions, so compatibility can be checked without inventing metadata.

## Decision

### Fingerprint v2

New reports use `sha256-v2`. The recipe is:

- length-prefixed recipe marker, origin and kind;
- up to 16 non-runtime frames, each with length-prefixed function and normalized
  path;
- no source line;
- a deterministic no-stack marker.

v1 remains implemented so persisted reports can be compared with reports using
the same recipe. A v1/v2 pair is incompatible. Neither recipe is treated as
proof of identity; inlining, missing symbols and path remapping can still change
a digest.

### Pairing and classification

- Group diagnostics by fingerprint within each report.
- A fingerprint appearing more than once on either side is `uncertain`; it is
  never arbitrarily paired.
- One record on both sides is `persistent` when severity, occurrences and leak
  totals match, otherwise `changed`.
- A record only in the target is `new`; only in the base is `resolved`.
- The diff core returns deterministic items sorted by fingerprint.

### Compatibility

Structural mismatches fail with `incompatible_runs`:

- tool name, target id, OS, architecture or fingerprint algorithm;
- an unsupported report schema is rejected earlier as `report_invalid`;
- diagnostic/report algorithm mismatch;
- `tool_error` or `policy_denied` evidence.

Observed differences are reported in `compatibility.changes`. A subset is
downgrading and also appears in `compatibility.uncertainties`, making all items
`uncertain`: tool/parser version, roots, options, program arguments, incomplete
or truncated reports, differing suppressions and missing metadata.
Program path/binary hash/outcome changes are reported but do not by themselves
discard the fingerprint comparison; this is the expected before/after-fix
workflow. The client sees the binary/outcome change in `changes`.
`analysis_complete=false` always downgrades. This is deliberately conservative
where evidence semantics are unclear.

### API and limits

`diagnostics_diff` is a read-only MCP tool backed by
`analysis.Service.Diff`. It loads both canonical reports through the existing
bounded streaming reader, returns a bounded page, and binds cursors to both run
ids and the diff rules. One oversized item returns `diff_too_large`; it is never
silently dropped.

Golden snapshots live in `testdata/golden/diff/` and are generated with
`go test ./internal/diagnostic -run TestDiffGolden -update-diff-golden`.

## Consequences

- Line-only movement no longer creates a new fingerprint in new reports.
- Old v1 reports remain readable but require a matching v1 partner.
- Configuration changes produce explicit uncertainty, not false regressions.
- Duplicate fingerprints and incomplete evidence remain visible. V2 can collapse
  two distinct errors in the same function/path/kind; duplicate groups are
  uncertain, never arbitrarily paired.
- The diff core has no clock, filesystem or process dependencies and is
  deterministic for golden tests.
- A future fingerprint v3 requires a new constant, validation, compatibility
  rule and golden fixtures; it must not reinterpret existing digests.
