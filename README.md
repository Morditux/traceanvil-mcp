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

**Early development — J0 (repository scaffolding) only.**

The repository now provides the Go module, the `version` command, project
documentation, architecture decision records and a CI quality gate. **No analysis
backend and no MCP server are implemented yet**; `serve`, `doctor` and
`validate-config` arrive with J1. Do not deploy this project yet.

Roadmap, safety rules and acceptance criteria are defined in the project
specification and tracked as milestones J0 to J14.

## Requirements

- Linux (Ubuntu 24.04 LTS or newer is the reference platform), `amd64` first.
- Go **1.25 or newer** (CI tests `1.25.x` and the current stable release).
- A C toolchain (`gcc`) for `go test -race`.

Analysis backends have additional requirements (Valgrind, GDB, LLDB, sanitizers,
`perf`). Their availability is detected at runtime and missing tools disable the
corresponding MCP tools instead of failing the server. See
[docs/compatibility.md](docs/compatibility.md) for the recorded state of the
development machine.

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

### Command-line contract

| Command | Behaviour |
|---|---|
| `version` | Prints one line of build information to stdout. |
| `help`, `-h`, `--help` | Prints usage to stdout. |
| *(none)* | Prints usage to stderr, exit code `2`. |
| *(unknown)* | Prints an error and usage to stderr, exit code `2`. |

Exit codes: `0` success, `1` runtime failure (reserved), `2` invalid usage.
These codes apply to the compiled binary. `go run` reports any non-zero exit as
`1` and prints the real status separately.
When the MCP server is added, stdout of `serve` will be reserved exclusively for
the MCP protocol; logs go to stderr.

## Repository layout

```text
cmd/mcp-native-diagnostics/   Command entry point
internal/cli/                 Argument dispatch and exit codes
internal/version/             Build information
docs/adr/                     Architecture decision records
docs/compatibility.md         Recorded toolchain inventory
scripts/check.sh              Formatting, vet, tests, race, build
scripts/preflight.sh          Read-only machine inventory
.github/workflows/ci.yml      Continuous integration
```

Analysis packages (`policy`, `runner`, `jobs`, `artifacts`, `diagnostic`,
`valgrind`, `dap`, `debugger`, ...) are introduced by their milestones.

## Development

```sh
./scripts/check.sh      # gofmt, go vet, go test, go test -race, go build
./scripts/preflight.sh  # read-only inventory; changes nothing
```

`preflight.sh` never installs packages, never edits kernel settings and never
executes a user program. Installing tools is an explicit, human-approved step.

See [CONTRIBUTING.md](CONTRIBUTING.md) for code, test and review expectations.

## Security

This server executes native programs. Directory allowlists are **not** a sandbox,
and an allowlisted binary may still be malicious. Read [SECURITY.md](SECURITY.md)
before using the project with untrusted code.

## License

Apache License 2.0. See [LICENSE](LICENSE).
