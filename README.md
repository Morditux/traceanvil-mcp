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

**Early development — J4 (analysis API and pagination) complete.**

The repository now provides:

- a working MCP server over **stdio** (`serve`);
- strict JSON configuration with `validate-config`;
- `doctor`, a read-only inspection of configuration, paths and capabilities;
- a capability registry, periodic revalidation and conditional tool publication
  with `tools/list_changed`;
- up to six MCP tools: `system_capabilities`, `analysis_start`,
  `analysis_status`, `analysis_cancel`, `analysis_report` and
  `analysis_artifact` (`analysis_start` is published only while the Valgrind
  capability is available);
- a bounded runner: allowlisted targets, symlink revalidation, process-group
  cleanup, timeouts/cancellation, bounded output, atomic private artifacts and
  job lifecycle (`internal/policy`, `runner`, `artifacts`, `jobs`);
- a Memcheck pipeline (`internal/valgrind`, `internal/diagnostic`): allowlisted
  Valgrind options, XML evidence, a bounded streaming parser, canonical
  diagnostics, leak/suppression reporting and `analysis.json`;
- an analysis API (`internal/analysis`): named errors, asynchronous start,
  status, cancellation, per-request timeouts, filtered and cursor-paginated
  reports, bounded artifact excerpts, persisted-run fallback and response/cache
  quotas;
- a functional Valgrind probe that exposes `backend.valgrind` as `available` only
  after Memcheck XML support is verified.

`analysis_start` launches one allowlisted target under Valgrind; the other
analysis tools only inspect its bounded evidence. GDB arrives with J6, LLDB with
J7, sanitizers and CTest with J9/J10, profilers with J12/J13. Do not deploy this
project yet.

Roadmap, safety rules and acceptance criteria are defined in the project
specification and tracked as milestones J0 to J14.

## Requirements

- Linux (Ubuntu 24.04 LTS or newer is the reference platform), `amd64` first.
- Go **1.25 or newer** (CI tests `1.25.x` and the current stable release).
- A C toolchain (`gcc`) for `go test -race` and the C fixtures.
- Valgrind for the real Memcheck end-to-end tests; those tests skip with a
  documented reason when Valgrind or `cc` is unavailable.

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
stdout, stderr or `result.json`. No tool accepts a filesystem path or a shell
command, and partial evidence is never returned as a clean result.

Tools are published from the capability registry. `analysis_start` appears only
while `backend.valgrind` is `available`; a missing backend never prevents the
server from starting or hides persisted evidence. Capabilities are revalidated
every 30 seconds and `notifications/tools/list_changed` is emitted when the
exposed set changes. All payloads carry `schema_version = 1`. See
[docs/mcp.md](docs/mcp.md) for schemas, error codes and pagination, and
[docs/valgrind.md](docs/valgrind.md) for the evidence format.

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
internal/diagnostic/          Canonical report, normalization and fingerprints
internal/valgrind/            Memcheck options, XML parser and analysis service
internal/analysis/            Analysis API, named errors, pagination and artifacts
internal/mcpserver/           MCP server, tool publication, analysis tools
internal/version/             Build information
testdata/fixtures/c/          Reviewed C fixtures for Memcheck E2E tests
testdata/fixtures/valgrind/   Static valid/truncated/malformed XML fixtures
tests/e2e/                    Real stdio MCP client/server tests
tests/integration/            Policy → runner → artifacts → jobs, Memcheck E2E
docs/adr/                     Architecture decision records
docs/configuration.md         Configuration reference
docs/artifacts.md             Run directory layout and manifests
docs/valgrind.md              Memcheck pipeline, report schema and limits
docs/mcp.md                   MCP transport, tool discovery and payloads
docs/compatibility.md         Recorded toolchain inventory
config.example.json           Example configuration
scripts/check.sh              Formatting, vet, tests, race, build
scripts/preflight.sh          Read-only machine inventory
.github/workflows/ci.yml      Continuous integration
```

Further analysis packages (`dap`, `debugger`, `sanitizer`, `profile`, `ctest`,
...) are introduced by their milestones.

## Development

```sh
./scripts/check.sh      # gofmt, vet, go test, go test -race, go build
./scripts/preflight.sh  # read-only inventory; changes nothing
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
- a real stdio MCP end-to-end test that drives `analysis_start` → `status` →
  `report` → `artifact` against Valgrind, and integration tests for cancellation
  and per-request timeouts that verify no job slot leaks and no false clean
  result.

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
