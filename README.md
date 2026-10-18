# MCP Native Diagnostics

Local [Model Context Protocol](https://modelcontextprotocol.io/) server that lets an
LLM agent run and interpret **native dynamic analysis** on Linux: Valgrind tools,
GDB/LLDB debugging sessions, sanitizers and profilers.

The value of the project is not arbitrary command execution. It is:

- typed MCP schemas instead of free-form shell commands;
- immutable raw evidence (XML, logs, profiles) plus normalized diagnostics;
- reproducible runs with explicit limits, cancellation and cleanup;
- capability-aware tool discovery: a tool is exposed only when it is implemented,
  enabled, permitted and usable on the current machine.

## Project status

**v1.0 release candidate — J14 hardening, retention and release tooling complete.**

The repository now provides:

- a working MCP server over **stdio** (`serve`);
- strict JSON configuration with `validate-config`;
- `doctor`, a read-only inspection of configuration, paths and capabilities;
- Helgrind and DRD adapters on the Valgrind XML pipeline with tool-specific
  origins, severity classification, capabilities and concurrency tests;
- bounded ASan, UBSan, LSan and TSan log parsers plus a sanitizer execution
  backend for targets that explicitly declare their instrumentation;
- a versioned diagnostic diff core with fingerprint pairing, new/resolved/
  persistent/changed/uncertain classification, metadata compatibility checks,
  bounded pages and golden snapshots;
- a capability registry, periodic revalidation and conditional tool publication
  with `tools/list_changed`;
- the J4/J9 analysis tools (`analysis_start`, `helgrind_run`, `drd_run`,
  `asan_run`, `ubsan_run`, `lsan_run`, `tsan_run`, `analysis_status`,
  `analysis_cancel`, `analysis_report`, `analysis_artifact`), the J8
  `diagnostics_diff` tool, the J10 CTest tools (`ctest_discover`, `ctest_run`,
  `ctest_status`, `ctest_cancel`, `ctest_report`, `ctest_artifact`), the J11
  `core_analyze`/`core_artifact`/`debug_attach` tools, the J12 profile tools
  (`profile_callgrind`, `profile_massif`, `profile_compare`,
  `profile_artifact`), the experimental J13 `profile_perf`/`vgdb_inspect`
  tools (with their artifacts) and the J6/J7 GDB/LLDB debug tools
  (`debug_start`, `debug_status`, `debug_set_breakpoints`, `debug_continue`,
  `debug_pause`, `debug_step`, `debug_threads`, `debug_stack_trace`,
  `debug_scopes`, `debug_variables`, `debug_stop`). `debug_start` is published
  only while at least one of `backend.gdb` or `backend.lldb` is available and a
  usable target exists; execution/action tools require one available backend,
  and status/stop remain available for an existing session;
- a bounded runner: allowlisted targets, symlink revalidation, process-group
  cleanup, timeouts/cancellation, bounded output, atomic private artifacts and
  job lifecycle (`internal/policy`, `runner`, `artifacts`, `jobs`);
- a Memcheck pipeline (`internal/valgrind`, `internal/diagnostic`): allowlisted
  Valgrind options, XML evidence, a bounded streaming parser, canonical
  diagnostics, leak/suppression reporting, versioned fingerprints (v2) and
  `analysis.json`;
- an analysis API (`internal/analysis`): named errors, asynchronous start,
  status, cancellation, per-request timeouts, filtered and cursor-paginated
  reports, bounded artifact excerpts, persisted-run fallback, response/cache
  quotas and bounded two-run diff pages;
- a generic DAP client (`internal/dap`): bounded Content-Length framing,
  request/response correlation, late-response accounting, events, timeouts,
  cancellation, capability negotiation, session state transitions, reverse
  requests and a scripted mock adapter (`internal/dap/daptest`);
- a GDB/LLDB DAP backend (`internal/debugger`): declared-target launch, session
  state, breakpoints, continue/pause/step, threads, stack, scopes, variables,
  explicit launch termination, controlled GDB attach with detach-on-stop,
  session quotas, idle retention and a documented backend-difference matrix;
- a versioned `lldb-dap` discovery probe (`lldb-dap`, `lldb-dap-<N>`) that
  verifies a real DAP `initialize` handshake;
- functional capability probes: Valgrind exposes `backend.valgrind` only after
  Memcheck XML support is verified, and GDB/LLDB expose their backend only after
  a real DAP `initialize` handshake. Neither probe runs a user program.

`analysis_start` launches one allowlisted target under Valgrind; the other
analysis tools only inspect its bounded evidence. `debug_start` launches one
allowlisted target under GDB or LLDB through DAP, and `debug_attach` is limited to
an explicit GDB PID allowlist. `profile_callgrind` and `profile_massif` run
declared targets with fixed native-output flags. The debugger tools are
deliberately narrow: no expression evaluation, raw memory read, debugger command
passthrough or debuggee terminal I/O is exposed over MCP. Do not deploy this
project yet.

## Release and operations

```sh
VERSION=v1.0.0 ./scripts/release.sh
GOCACHE="$PWD/.gocache" ./scripts/check.sh
```

The release script builds static `amd64`/`arm64` binaries, Debian packages,
SPDX SBOMs and `SHA256SUMS`; optional GPG signing is described in
[docs/release.md](docs/release.md). Artifact retention uses `storage.*`; with the default
`purge_on_start=false`, disk use is bounded only when you run the explicit purge
or opt into startup purge. Inspect and apply it with:

```sh
mcp-native-diagnostics purge --config config.json --dry-run
mcp-native-diagnostics purge --config config.json
```

See [docs/operations.md](docs/operations.md) for backup, upgrade and security
checklists.

Roadmap, safety rules and acceptance criteria are defined in the project
specification and tracked as milestones J0 to J14.

## Requirements

- Linux (Ubuntu 24.04 LTS or newer is the reference platform), `amd64` first.
- Go **1.25 or newer** (CI tests `1.25.x` and the current stable release).
- A C toolchain (`gcc`) for `go test -race` and the C fixtures.
- Valgrind for the real Memcheck/Helgrind/DRD/Callgrind/Massif tests, a C
  toolchain with sanitizer runtimes for ASan/UBSan/LSan/TSan tests, and
  GDB/`lldb-dap` for the real debugger tests; those tests skip with a documented reason when a tool is
  unavailable.

Analysis backends have additional requirements (Valgrind, GDB, LLDB, sanitizers,
`perf`). Their availability is detected at runtime and missing tools hide the
corresponding MCP tools instead of failing the server. See
[docs/compatibility.md](docs/compatibility.md) for the recorded state of the
development machine and [docs/valgrind.md](docs/valgrind.md) for the Memcheck
pipeline, report schema and limitations.

## Build and run

```sh
go build ./...
go run ./cmd/mcp-native-diagnostics version

# Or build an explicit binary (build/ is git-ignored):
go build -o build/mcp-native-diagnostics ./cmd/mcp-native-diagnostics
./build/mcp-native-diagnostics version
```

Example output:

```text
mcp-native-diagnostics 0.0.0-dev (commit unknown, date unknown, go1.27.0)
```

### Quick start

```sh
cp config.example.json config.local.json   # edit paths for your machine
./build/mcp-native-diagnostics validate-config --config config.local.json
./build/mcp-native-diagnostics doctor --config config.local.json
./build/mcp-native-diagnostics serve --config config.local.json
```

`serve` speaks MCP over stdin/stdout. **stdout is reserved for the protocol**;
all logs and diagnostics go to stderr. A typical client entry looks like:

```json
{
  "mcpServers": {
    "native-diagnostics": {
      "command": "/absolute/path/to/mcp-native-diagnostics",
      "args": ["serve", "--config", "/absolute/path/to/config.local.json"]
    }
  }
}
```

See [docs/configuration.md](docs/configuration.md) for every field, default and
validation rule.

### Command-line contract

| Command | Behaviour |
|---|---|
| `serve --config <file>` | Runs the MCP server over stdio. |
| `doctor --config <file>` | Prints config, path and capability state; launches no user program. |
| `validate-config --config <file>` | Validates a configuration file and exits. |
| `purge --config <file> [--dry-run]` | Applies artifact age/count/byte retention; stop the server first. |
| `sbom [--output <file>] [--binary <path>]` | Emits an SPDX SBOM from embedded Go build information. |
| `version` | Prints one line of build information to stdout. |
| `help`, `-h`, `--help` | Prints usage to stdout. |
| *(none)* | Prints usage to stderr, exit code `2`. |
| *(unknown)* | Prints an error and usage to stderr, exit code `2`. |

Exit codes: `0` success, `1` configuration/runtime failure, `2` invalid usage.
These codes apply to the compiled binary. `go run` reports any non-zero exit as
`1` and prints the real status separately.

### Current MCP surface

`system_capabilities` reports build information, the effective policy and
limits, backend capability records, exposed tools and limitation warnings. It is
read-only and never launches a program.

`analysis_start` launches one Memcheck run of a declared target and returns a
`run_id`. `analysis_status` reports state, phase, progress and metrics;
`analysis_cancel` stops the process group; `analysis_report` returns the compact
summary plus a filtered, bounded page of diagnostics with an opaque cursor;
`analysis_artifact` returns bounded excerpts of `memcheck.xml`, `analysis.json`,
stdout, stderr or `result.json`. `diagnostics_diff` compares two persisted
reports by versioned fingerprint and returns bounded new, resolved, persistent,
changed and uncertain items. It never launches a program. Downgrading
configuration/evidence differences make items uncertain; binary/path/outcome
changes are reported without discarding the fingerprint comparison. Structurally
incompatible reports fail with `incompatible_runs` rather than producing a
misleading empty diff.

`debug_start` launches one declared target under GDB or LLDB over DAP and
returns a `session_id`; `backend` defaults to `gdb` when usable and falls back
to `lldb` when GDB is disabled or unavailable. `stop_on_entry` defaults to true.
`debug_set_breakpoints` resolves a source file inside a workspace root (symlink
escapes rejected);
`debug_continue` is asynchronous; `debug_pause` and `debug_step` wait briefly for
the next stop. Poll `debug_status` until `stop_generation` changes whenever the
response still reports `running`. `debug_threads`,
`debug_stack_trace`, `debug_scopes` and `debug_variables` return bounded pages;
`debug_stop` terminates by default. Detach is not exposed:
`terminate=false` returns `unsupported_capability` because a real detach did not
reliably preserve the inferior. Debuggee stdout, stderr and stdin are not
exposed. GDB uses `/dev/null` as the inferior tty; lldb-dap uses a private PTY.
Neither shares the DAP pipe.

No tool accepts a shell command, arbitrary executable path, PID, expression,
memory address or debugger script. Partial evidence is never returned as a clean
result, and missing symbols are reported rather than invented.

Tools are published from the capability registry. `analysis_start` appears only
while `backend.valgrind` is `available`; `debug_start` appears only while
`backend.gdb` or `backend.lldb` is `available` and a declared target currently
resolves under policy. Execution actions appear while at least one debugger
backend is available; `debug_status` and `debug_stop` stay available for an
existing session even if its backend disappears. A missing backend never
prevents the server from starting or hides persisted analysis evidence.
Capabilities are revalidated every 30 seconds and
`notifications/tools/list_changed` is emitted when the exposed set changes. All
payloads carry `schema_version = 1`. See [docs/mcp.md](docs/mcp.md) for schemas,
error codes and pagination, [docs/valgrind.md](docs/valgrind.md) for the evidence
format, [docs/diff.md](docs/diff.md) for run comparison,
[docs/sanitizers.md](docs/sanitizers.md) for sanitizer runs,
[docs/ctest.md](docs/ctest.md) for CTest integration,
[docs/core-and-attach.md](docs/core-and-attach.md) for cores/attach,
[docs/profile.md](docs/profile.md) for profiling,
[docs/experimental.md](docs/experimental.md) for perf/vgdb,
[docs/operations.md](docs/operations.md) for operations,
[docs/architecture.md](docs/architecture.md) for the architecture,
[docs/release.md](docs/release.md) for releases, and
[docs/debugger.md](docs/debugger.md) for the GDB/LLDB backends.

## Repository layout

```text
cmd/mcp-native-diagnostics/   Command entry point
internal/cli/                 Commands, orchestration and exit codes
internal/config/              Strict JSON configuration and validation
internal/capabilities/        Capability records, probes and registry
internal/policy/              Runtime target resolution and argument allowlist
internal/runner/              Bounded process execution and cleanup
internal/artifacts/           Private run directories and atomic writes
internal/jobs/                Job lifecycle, quotas and result manifests
internal/diagnostic/          Canonical report, fingerprints and run comparison
internal/valgrind/            Memcheck/Helgrind/DRD options, XML parser and service
internal/sanitizer/           ASan/UBSan/LSan/TSan parser, reports and service
internal/ctest/               Trusted CTest discovery, execution and JUnit reports
internal/core/                Configured core-dump analysis with fixed GDB
internal/profile/             Callgrind/Massif native parsers and comparisons
internal/perf/                Experimental fixed perf stat runner/parser
internal/vgdb/                Experimental fixed-command vgdb inspection
internal/sbom/                SPDX 2.3 generation from embedded Go build info
internal/analysis/            Analysis API, named errors, pagination and artifacts
internal/dap/                 Generic DAP framing, client, state and capabilities
internal/dap/daptest/         Scripted in-memory DAP adapter for tests
internal/debugger/            GDB/LLDB process, sessions, manager and debug API
internal/mcpserver/           MCP server, tool publication, analysis and debug tools
internal/version/             Build information
testdata/fixtures/c/          Reviewed C, concurrency and sanitizer fixtures
testdata/fixtures/valgrind/   Static valid/truncated/malformed XML fixtures
testdata/golden/diff/         Deterministic diff reports and expected output
tests/e2e/                    Real stdio MCP client/server tests
tests/integration/            Policy → runner → artifacts → jobs, Memcheck E2E
docs/adr/                     Architecture decision records
docs/configuration.md         Configuration reference
docs/artifacts.md             Run directory layout and manifests
docs/valgrind.md              Memcheck pipeline, report schema and limits
docs/diff.md                  Fingerprint pairing, compatibility and diff semantics
docs/sanitizers.md            ASan/UBSan/LSan/TSan execution, parsing and limits
docs/ctest.md                 CTest project discovery, selection and reports
docs/core-and-attach.md       Core profiles and controlled attach
docs/profile.md               Callgrind/Massif profiling and comparison
docs/experimental.md          Experimental perf and vgdb surfaces
docs/operations.md            Build, retention, purge, backup, troubleshooting
docs/architecture.md          Components, trust boundaries and data flow
docs/release.md                Release build, SBOM, checksums and signing
GOVERNANCE.md                 Roles, change process and release governance
docs/dap.md                   DAP framing, client lifecycle, state and limits
docs/debugger.md              GDB/LLDB backends, debug API, lifecycle and limits
docs/mcp.md                   MCP transport, tool discovery and payloads
docs/compatibility.md         Recorded toolchain inventory
config.example.json           Example configuration
scripts/check.sh              Formatting, dependency verify, vet, tests, race, build
scripts/fuzz.sh               Bounded parser/DAP fuzz campaign
scripts/bench.sh              Large-log parser benchmarks
scripts/release.sh            Cross-builds, SBOMs, checksums and optional signing
scripts/preflight.sh          Read-only machine inventory
.github/workflows/ci.yml      Ubuntu/Go matrix plus fuzz smoke
.github/workflows/release.yml Tagged unsigned release build
```

Further analysis packages (`profile`, ...) are
introduced by their milestones.

## Development

```sh
./scripts/check.sh      # gofmt, vet, go test, go test -race, go build
./scripts/preflight.sh  # read-only inventory; changes nothing

# Focused DAP, GDB and LLDB checks:
go test ./internal/dap ./internal/debugger
go test -fuzz=FuzzReadMessage -fuzztime=5s ./internal/dap
```

`go test ./...` includes:

- an end-to-end test that builds the binary, starts `serve` over real pipes and
  verifies that stdout contains only JSON-RPC messages;
- runner tests for timeouts, cancellation, TERM-ignoring processes, pipe-holding
  grandchildren and a 100-run process-leak check;
- an integration test that resolves a target, runs it through the job manager
  and reads `result.json` and the output logs;
- Memcheck end-to-end tests (invalid read/write, use-after-free, double free,
  uninitialised value, leaks, clean program, SIGSEGV, timeout, cancellation,
  truncated XML and suppressions) that compile the C fixtures and run real
  Valgrind; they skip when `valgrind` or `cc` is missing;
- analysis API tests for 20 000-diagnostic pagination, filters, cursor scoping,
  byte/item bounds, oversized and unreadable diagnostics, report corruption,
  artifact excerpts, FIFO/symlink rejection and named errors;
- deterministic diff tests and golden snapshots for fingerprint v2,
  new/resolved/persistent/changed/uncertain pairing, duplicate fingerprints,
  incompatible metadata, configuration changes, cursor binding and byte bounds;
- a real Valgrind integration test running the same deterministic fixture twice
  and comparing the reports as persistent;
- a real stdio MCP end-to-end test that drives `analysis_start` → `status` →
  `report` → `artifact` against Valgrind, and integration tests for cancellation
  and per-request timeouts that verify no job slot leaks and no false clean
  result;
- DAP client tests for fragmented frames, UTF-8 byte lengths, malformed and
  oversized frames, out-of-order and late responses, EOF/Close unblocking,
  pending/backpressure limits, blocked transports, capabilities, reverse
  requests, async launch/configurationDone ordering, state transitions and a
  fuzz target for the frame decoder;
- real GDB and LLDB integration and stdio MCP tests for stop-on-entry, verified
  breakpoints, continue/pause/step, threads, stack, scopes and locals, plus
  stripped-symbol fixtures that assert degraded evidence is explicit. They skip
  when `gdb`, `lldb-dap` or `cc` is missing;
- a backend parity matrix covering the common GDB/LLDB surface and explicit
  documentation of ordering, thread-id, stop-on-entry and terminal differences.

`preflight.sh` never installs packages, never edits kernel settings and never
executes a user program. Installing tools is an explicit, human-approved step.

See [CONTRIBUTING.md](CONTRIBUTING.md) for code, test and review expectations.

## Security

This server launches native programs. Directory allowlists are **not** a
sandbox, and an allowlisted binary may still be malicious. The runner uses
process groups, a minimal environment and bounded output, but it is not a
container. Valgrind XML and persisted reports are treated as untrusted input and
bounded by size, depth, record, frame and string limits; API responses are
bounded by items and bytes. A target still runs as the same user and can inspect
or tamper with its run directory. Read [SECURITY.md](SECURITY.md) before using
the project with untrusted code.

## License

Apache License 2.0. See [LICENSE](LICENSE).
