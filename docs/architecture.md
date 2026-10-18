# Architecture

## Process model

```text
MCP client
  │ stdio JSON-RPC
  ▼
internal/mcpserver  ── capability-filtered tools
  │
  ├── internal/analysis ── Valgrind Memcheck/Helgrind/DRD XML
  ├── internal/sanitizer ── ASan/UBSan/LSan/TSan logs
  ├── internal/ctest ── trusted CTest discovery/JUnit
  ├── internal/profile ── Callgrind/Massif native formats
  ├── internal/core ── configured core + GDB batch
  ├── internal/debugger ── GDB/LLDB DAP sessions
  ├── internal/perf ── experimental fixed perf stat
  └── internal/vgdb ── experimental fixed vgdb inspection
        │
        ▼
internal/policy → internal/jobs → internal/runner
        │                    │
        └── internal/artifacts (private run directories)
```

`runner` starts programs with an argv vector and process group; there is no
shell. `jobs` owns asynchronous lifecycle, quotas, timeouts and `result.json`.
`artifacts` owns opaque run ids, atomic bounded writes, symlink-safe reads and
retention.

## Trust boundaries

- `workspace_roots`, `allowed_targets`, CTest projects, core profiles and
  suppression files are administrator declarations.
- MCP clients choose declared ids, allowlisted arguments and bounded options;
  they never choose arbitrary paths, debugger flags, Valgrind flags or monitor
  commands.
- The same user can read and modify `report_dir`; path checks are not a
  sandbox. A hostile same-user process is outside the isolation model.
- Elevated features (`attach`, eventual evaluation) are explicit opt-ins.
  `perf`/`vgdb` are experimental and hidden when permissions are insufficient.
- Capability probes run at startup and periodically; a tool is listed only when
  implemented, enabled, permitted and usable. Persisted evidence tools remain
  available for existing runs by design.

## Evidence

Every run has an opaque `run_<32 hex>` directory with a normalized report and,
where applicable, raw evidence. Incomplete/truncated/timeout/canceled evidence
sets the completion flag to false and adds warnings. Tool-reported values are
kept distinct from server classification and fingerprints.

## Extension points

New backends should add a capability probe, an isolated business package, a
tool definition with a typed schema and bounded evidence, tests with a real
fixture or an explicit skip, and an ADR. Do not expose free command strings.
