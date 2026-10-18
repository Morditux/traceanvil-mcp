# ADR 0015 — Callgrind and Massif profiling

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J12

## Context

J12 adds native Callgrind and Massif profiles. The server must not expose
free-form Valgrind options, arbitrary program paths or a shell. Results must
preserve native evidence, normalize units/events and compare cautiously.

## Decision

- Add `backend.valgrind.callgrind` and `backend.valgrind.massif` capability
  probes based on the fixed `--tool=<name> --help` markers. The base Valgrind
  binary is still revalidated at invocation.
- Add `profile_callgrind` and `profile_massif`, accepting only `target_id`,
  allowlisted `args` and `timeout_ms`. The programs are launched through
  `os/exec` argument vectors with fixed Valgrind flags; target arguments follow
  `--`.
- Preserve the native `callgrind.out`/`massif.out` and write normalized
  `profile.json`. Bound raw files through a watcher plus the artifact quota;
  bound the report and response separately. `profile_artifact` exposes bounded
  excerpts only.
- Implement native parsers rather than depending on `callgrind_annotate` or
  `ms_print`:
  - Callgrind: events, summary/totals, self-cost function aggregation, top 32;
  - Massif: time unit, peak heap/extra/stack bytes, snapshot count and bounded
    samples. Heap-tree text is not interpreted.
- `profile_compare` accepts persisted run ids and refuses different tools,
  normalized units, Callgrind event sets/order, Massif time units or incomplete
  profiles with `incompatible_profiles`. Deltas are descriptive only.
- Profile calls are synchronous in the MCP request but always use
  `context.Context`, the job manager timeout, cancellation and process-group
  cleanup. No sysctl, sudo or shell is used.
- `complete=false` is mandatory for timeout, cancellation, nonzero/signal exit,
  raw truncation, parse failure or missing evidence.

## Consequences

- Callgrind instruction/event counts are deterministic only for the same
  binary, inputs and fixed tool configuration; they are not wall-clock time.
- Massif reports heap snapshots, not a leak verdict. Heap-tree evidence stays in
  the raw artifact.
- Comparison is intentionally strict and descriptive rather than a statistical
  regression engine.
