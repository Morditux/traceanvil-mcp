# Diagnostic comparison (J8)

`diagnostics_diff` compares two persisted analysis reports by versioned
fingerprint. It never launches a program, never reads a client-selected path and
never turns incompatible metadata into an empty diff.

## Fingerprints

Two recipes are defined in `internal/diagnostic/fingerprint.go`:

| Algorithm | Recipe |
|---|---|
| `sha256-v1` | origin + kind + up to 16 non-runtime frames (function, path, **line**) |
| `sha256-v2` | length-prefixed origin + kind + up to 16 non-runtime frames (function, report-supplied path, **no line**) |

New reports use **v2**. The Valgrind producer normalizes workspace paths before
fingerprinting; `FingerprintV2` itself only trims fields and does not resolve
filesystem paths. v2 deliberately ignores source-line movement in an otherwise
identical stack; inlining, missing symbols and path remapping can still change a
digest. v1 remains readable so older persisted reports can be compared
with other v1 reports. A v1/v2 pair is incompatible; the server does not guess a
cross-recipe identity.

The fingerprint is a versioned heuristic, not proof of identity. Duplicate
fingerprints within one report make pairing ambiguous. Because v2 omits source
lines, two distinct errors in the same function/path/kind with the same selected
frames can collapse into one duplicate group; the server reports them as
`uncertain` rather than guessing or inventing a line.

## Compatibility

The comparison first checks structural compatibility. Incompatible reports
return the named error `incompatible_runs`; no new/resolved/persistent list is
returned.

Hard incompatibilities include:

- different tool names;
- different target ids (when both are present);
- different OS or architecture (when both are present);
- different or unknown fingerprint algorithms;
- a diagnostic whose fingerprint algorithm does not match the report;
- a `tool_error` or `policy_denied` run, which has no usable evidence.

Differences are split into **reported changes** and **downgrading
uncertainties**.

Reported in `compatibility.changes` without forcing every item to `uncertain`:

- tool path;
- program path or binary hash;
- program exit code, signal, timeout or cancellation.

These are expected in the primary before/after-fix workflow. A binary change is
visible in the response; the comparison still uses versioned fingerprint
matching. Clients must not treat `compatibility.changes` as proof that nothing
else changed.

Downgrading in `compatibility.changes` and `compatibility.uncertainties`; all
items are classified `uncertain` instead of `new`, `resolved`, `persistent` or
`changed`:

- tool version or parser version changes;
- workspace roots, analysis options or program arguments;
- incomplete or truncated reports, including unreadable/oversized records;
- differing suppressions;
- missing metadata such as a tool version, program hash or fingerprint algorithm.

The reason list is part of the response; clients must not infer a resolved bug
from an uncertain item.

## Classification

Within one fingerprint group:

| Change | Meaning |
|---|---|
| `new` | Present in the target report, absent from the base report. |
| `resolved` | Present in the base report, absent from the target report. |
| `persistent` | Present in both; severity, occurrence count and leak totals are equal. |
| `changed` | Present in both; occurrence count, severity, leaked bytes or leaked blocks differ. |
| `uncertain` | Fingerprints are duplicated, or compatibility made classification unsafe. |

A `changed` item contains both sides so the caller can see the observed
difference. A duplicate group contains only the first record from each side plus
`base_count`/`target_count` and an explicit reason; it is never paired as if the
duplicates were one diagnostic.

`summary` counts fingerprint groups, not raw records. `base_diagnostics` and
`target_diagnostics` count records; `base_occurrences` and
`target_occurrences` sum tool-reported occurrences.

## Request

```json
{
  "base_run_id": "run_<32 hex>",
  "target_run_id": "run_<32 hex>",
  "cursor": "optional opaque next_cursor",
  "limit": 50
}
```

The base is the reference/older run. The target is the candidate/newer run.
Both ids must exist and differ. The tool is read-only and does not require the
Valgrind backend; persisted runs remain comparable when the backend is missing.

## Response and pagination

The response is bounded by `limits.max_response_items`, the server page cap and
the response byte budget. It contains:

- `compatibility` — fingerprint algorithm, changes and uncertainties;
- `summary` — record/occurrence totals and item classifications;
- `page` — offset, limit, returned, matched, consumed, `has_more`,
  `next_cursor`, byte-budget flag and scan flags;
- `items` — compact evidence for each fingerprint group (no full stack);
- `warnings` — bounded report warnings and budget notices.

Continue with `page.next_cursor` only with the same pair of run ids. A cursor is
bound to both ids and to the diff rules; reusing it with another target is
`invalid_cursor`. If one item alone exceeds the response byte bound, the call
returns `diff_too_large` rather than silently omitting it.

## Errors

| Code | Meaning |
|---|---|
| `invalid_argument` | Same run ids, malformed cursor/limit or invalid request. |
| `run_not_found` | One run directory does not exist. |
| `report_unavailable` | A run exists but `analysis.json` is missing. |
| `report_invalid` | A stored report is malformed, has an unsupported schema version or violates its invariants. |
| `report_too_large` | A report exceeds the read bound. |
| `incompatible_runs` | Structural metadata makes an honest comparison impossible. |
| `invalid_cursor` | The cursor is malformed, scoped to another pair, or out of range. |
| `diff_too_large` | A diff item or the response envelope exceeds the response byte bound. |

No error is converted into "no new issues"; missing or incompatible evidence is
always explicit.

## Golden fixtures

`testdata/golden/diff/` contains deterministic base/target reports and the
expected `DiffResult` for new/resolved/persistent/changed classification. The
test can regenerate the expected snapshot with:

```sh
go test ./internal/diagnostic -run TestDiffGolden -update-diff-golden
```

Review the generated snapshot before committing it.

## Limits

- v2 stack depth: 16 non-runtime frames.
- Diff page: `min(limits.max_response_items, server page cap)` items and the
  configured response byte budget.
- Report read: the same 64 MiB / per-record / header bounds as
  `analysis_report`.
- No total wall-clock diff deadline; the request context and report read bounds
  apply.
