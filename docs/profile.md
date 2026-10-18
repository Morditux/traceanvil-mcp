# Callgrind and Massif profiling (J12)

`profile_callgrind` and `profile_massif` run a **declared target** under the
configured Valgrind binary. Calls are synchronous in the MCP request, but are
bounded by the context, the configured timeout and process-group cancellation. They accept only `target_id`, allowlisted `args` and
an optional `timeout_ms`; they never accept a path, a Valgrind flag, a shell
fragment or a perf event.

## Fixed invocation

Callgrind:

```text
valgrind --tool=callgrind --callgrind-out-file=<run>/callgrind.out
         --collect-jumps=no --cache-sim=no --branch-sim=no
         --instr-atstart=yes --collect-atstart=yes -- <program> <args...>
```

Massif:

```text
valgrind --tool=massif --massif-out-file=<run>/massif.out
         --time-unit=i --detailed-freq=1 --max-snapshots=100 --depth=30
         --pages-as-heap=no --stacks=no -- <program> <args...>
```

The target arguments follow `--`, so a target argument cannot be reinterpreted
as a Valgrind option. The raw native file is preserved as evidence, bounded by
the run quota and the profile raw limit, and a `profile.json` normalized report
is written atomically. `profile_artifact` returns bounded excerpts of
`profile.json`, `callgrind.out` or `massif.out`.

## Normalized results

Callgrind reports:

- native `events` and their totals;
- `metrics` with `event_<name>`, `total` (primary event) and `functions`;
- up to 32 top functions by **self** cost for the primary event;
- `metric_unit="events"` and the primary metric name;
- the target `program_sha256` used for like-for-like comparison; a target
  replaced during the run makes the report incomplete.

Massif reports:

- `time_unit` (missing time units are surfaced as warnings and make the report
  incomplete), peak heap bytes, peak extra heap bytes, peak stack bytes;
- total snapshot count and up to 64 bounded snapshot samples;
- `metrics` with `heap_peak_bytes`, `heap_extra_peak_bytes`,
  `stack_peak_bytes` and `snapshots`;
- `metric_unit="bytes"`.

Heap-tree text is preserved in the raw file but is not interpreted as a leak
or ownership claim.

`complete` is false when the target did not start or exited nonzero, was
signaled, timed out/canceled, the raw file was truncated, the native parser
failed, or no function/snapshot evidence was available. Partial evidence is
never presented as a clean profile.

## Comparison

`profile_compare` reads two persisted runs by `run_id`. It rejects:

- a different tool;
- different normalized metric units;
- different Callgrind event sets/order;
- different Massif `time_unit`;
- incomplete profiles.

Returned deltas and percentages are descriptive. They do not establish
statistical significance, wall-clock completion time or a regression; the
binary, inputs and fixed tool configuration must also be equivalent.

## Limits and trust

- The target must already be declared in `allowed_targets`; its arguments are
  checked as a multiset subset of `allowed_args`.
- The Valgrind executable is resolved by the capability registry and
  revalidated at invocation.
- Output is bounded. A watcher cancels a profile that exceeds the raw limit
  while running; Callgrind/Massif often write at exit, so the final `Adopt`
  truncation is the authoritative bound. The target is stopped on timeout or
  caller cancellation.
- A `.valgrindrc` in the target working directory would add options outside the
  fixed command line; the profile service refuses such a target. `VALGRIND_OPTS`
  and `HOME` are absent from the minimal environment. No sysctl, sudo or shell
  is used.
