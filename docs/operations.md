# Operations guide

## Scope

The server is a local stdio MCP process. It is launched by the MCP client, not
by a network listener. Run it as a dedicated non-root user with read access to
the declared targets and write access only to `report_dir`.

Do not run tools that execute untrusted binaries as a sandbox. `workspace_roots`
and target declarations are trust decisions; use a separate container, VM or
OS user if isolation is required.

## Build and install

```sh
go build -trimpath -o mcp-native-diagnostics ./cmd/mcp-native-diagnostics
./mcp-native-diagnostics version
./mcp-native-diagnostics validate-config --config /etc/mcp-native-diagnostics.json
./mcp-native-diagnostics doctor --config /etc/mcp-native-diagnostics.json
```

`doctor` performs read-only checks and reports where each backend is hidden.
It does not install packages, run a declared target or change kernel settings.

## Configuration

Start from `config.example.json`. Keep `report_dir` on a dedicated filesystem
with enough room for `storage.max_bytes`; use mode `0700` ownership for the
runtime user. A live server holds an exclusive `.server.lock`; a second server or
a purge on the same `report_dir` is refused. Stop the server before purge. Keep `workspace_roots` narrow; do not use `/`.

The server never elevates privileges, runs sudo, changes
`kernel.yama.ptrace_scope` or `kernel.perf_event_paranoid`, or enables
`PR_SET_PTRACER` on production processes. Restricted capabilities are reported
in `system_capabilities` and their tools are hidden.

## Artifact retention and purge

`storage.max_age_days`, `storage.max_runs` and `storage.max_bytes` are defaults
for explicit purge. `purge_on_start=false` is the safe default; set it to true
only if deleting old evidence at startup is intended.

```sh
mcp-native-diagnostics purge --config config.json --dry-run
mcp-native-diagnostics purge --config config.json
```

Stop the server before purging. The command only removes direct
`run_<32 hex>` directories owned by the current user. It does not follow
symlinks, remove unknown entries or remove files outside `report_dir`.
Inspect the dry-run JSON before applying it.

## Backup and restore

Back up `report_dir` as a unit. Run directories are immutable from the server's
perspective after a run completes, but write access is still the same-user trust
boundary. A restored run can be queried by `analysis_report`,
`analysis_artifact`, `profile_artifact`, `ctest_report` or other evidence tools
as long as its directory and `result.json`/normalized report are intact.

## Logs and diagnostics

- Protocol stdout is reserved for MCP; diagnostics go to stderr.
- `system_capabilities` explains hidden tools and probe reasons.
- Run directories contain `result.json`, raw evidence and normalized reports.
- A missing or truncated artifact is reported; it is never treated as an empty
  clean result.
- For a failed run, inspect `result.json`, `stdout.log`, `stderr.log`, then the
  tool-specific normalized report.

## Upgrade and rollback

1. Verify the release checksum and SBOM as described in
   [release.md](release.md).
2. Stop the MCP client/server process.
3. Replace the binary; keep `report_dir` and configuration unchanged.
4. Run `doctor`, then a small `analysis_start`/`debug_status` probe.
5. Keep the previous binary until the new one passes local checks.

The JSON schema version is part of each payload. An incompatible schema change
requires an ADR and a new milestone; do not silently reinterpret old evidence.

## Security checklist

- Dedicated non-root user; no sudoers entry needed.
- Narrow workspace roots and target allowlists.
- No arbitrary shell, monitor passthrough or free debugger commands.
- `policy.profile` remains `safe-default` unless an elevated feature is
  explicitly reviewed.
- `perf` and `vgdb` remain opt-in and experimental.
- Review `system_capabilities` after toolchain or kernel upgrades.
- Report suspected security issues privately as described in
  [../SECURITY.md](../SECURITY.md).

## Benchmarks

Process-leak stress check:

```sh
GOCACHE="$PWD/.gocache" go test ./internal/runner -run TestNoLeakedProcessesAfter100Runs -count=1
```

Parser benchmarks are bounded and environment-specific:

```sh
BENCH_TIME=3x ./scripts/bench.sh
```

Reference smoke run on the J14 development host (AMD Ryzen 5 3600, Go 1.27,
`-benchtime=1x`): Callgrind ~30 MB/s, Massif ~159 MB/s, perf stat ~38 MB/s,
large Valgrind XML ~30 MB/s (about 24 MB allocated per 2,000-error document). Record the host and results in the release
report; do not turn them into a pass/fail threshold without a stable reference
machine.

## Fuzz smoke and corpus

CI runs a bounded five-second-per-target fuzz smoke campaign. For release
hardening use a longer campaign on a dedicated machine:

```sh
GOCACHE="$PWD/.gocache" FUZZ_TIME=10m ./scripts/fuzz.sh
```

Crash inputs are written under `testdata/fuzz/` by the Go tool; inspect them and
commit only reviewed regression seeds. The script never contacts the network and
never installs tools.
