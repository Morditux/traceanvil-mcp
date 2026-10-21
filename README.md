<p align="center">
  <img src="docs/assets/traceanvil-logo.png" alt="TraceAnvil logo: an anvil with an execution trace" width="180">
</p>

<h1 align="center">TraceAnvil MCP</h1>

<p align="center">Native diagnostics for AI coding agents, backed by reproducible evidence.</p>

<p align="center">
  <a href="https://github.com/Morditux/traceanvil-mcp/actions/workflows/ci.yml"><img src="https://github.com/Morditux/traceanvil-mcp/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI status"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.25 or newer"></a>
  <a href="docs/compatibility.md"><img src="https://img.shields.io/badge/platform-Linux-FCC624?logo=linux&amp;logoColor=black" alt="Platform: Linux"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache 2.0"></a>
</p>

TraceAnvil is a local [Model Context Protocol](https://modelcontextprotocol.io/)
server written in Go. It lets agents investigate native programs with Valgrind,
GDB, LLDB, sanitizers and profilers through typed tools over **stdio**.

Each run preserves raw evidence alongside structured diagnostics. Tools appear
only when their backend is implemented, enabled, permitted and usable. Partial
or failed analyses are never presented as clean results.

**Status:** v1.0 release candidate. Review the [security model](SECURITY.md)
before use; deployment is not yet recommended.

## What it does

| Area | Tools and capabilities |
|---|---|
| Memory and concurrency | Valgrind Memcheck, Helgrind, DRD; ASan, UBSan, LSan, TSan |
| Interactive debugging | GDB and LLDB through DAP: breakpoints, stepping, threads, stacks and variables |
| Test integration | Discovery and execution of explicitly configured CTest projects |
| Evidence and comparison | Raw artifacts, structured reports and diagnostic diffs |
| Profiling | Callgrind and Massif reports and comparisons |
| Opt-in features | Core analysis, controlled GDB attach; experimental `perf stat` and `vgdb` |

Runs have timeouts, cancellation, output limits and process-group cleanup.
Executable targets must be named in the configuration. The MCP API exposes no
free-form shell commands, debugger scripts, expression evaluation or raw memory
reads.

## Quick start

Requires **Linux** and **Go 1.25+**. Ubuntu 24.04 LTS is the reference platform.
Install the analysis backends you need separately; unavailable backends hide
the corresponding tools. See [compatibility](docs/compatibility.md).

```sh
go build -o build/mcp-native-diagnostics ./cmd/mcp-native-diagnostics
cp config.example.json config.local.json
```

Edit `config.local.json` to set your workspace, report directory, allowed targets
and enabled backends, then validate and inspect it:

```sh
./build/mcp-native-diagnostics validate-config --config config.local.json
./build/mcp-native-diagnostics doctor --config config.local.json
```

`doctor` reports capabilities without launching a user program.

## Client integration

Replace the absolute paths below with your built binary and validated
`config.local.json`. Merge each example into your existing client configuration.
The client starts `serve` over stdin/stdout; **stdout is reserved for MCP** and
logs go to stderr. The executable retains its `mcp-native-diagnostics` name.

### OpenCode

Add this to `opencode.json` at your project's root:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "traceanvil": {
      "type": "local",
      "command": [
        "/absolute/path/to/build/mcp-native-diagnostics",
        "serve", "--config", "/absolute/path/to/config.local.json"
      ],
      "enabled": true
    }
  }
}
```

Start OpenCode in that project. See the [OpenCode MCP guide](https://opencode.ai/docs/mcp-servers/).

### Codex

Add this to `~/.codex/config.toml`, or `.codex/config.toml` for a trusted project:

```toml
[mcp_servers.traceanvil]
command = "/absolute/path/to/build/mcp-native-diagnostics"
args = ["serve", "--config", "/absolute/path/to/config.local.json"]
```

Start a new Codex session and use `/mcp` to check the connection.
See the [Codex MCP guide](https://developers.openai.com/codex/mcp/).

### Claude Code

Run this from your project's root to register the server in `.mcp.json`:

```sh
claude mcp add --transport stdio --scope project traceanvil -- \
  /absolute/path/to/build/mcp-native-diagnostics \
  serve --config /absolute/path/to/config.local.json
```

Start Claude Code, approve the project server when prompted, and use `/mcp`
to check the connection. See the [Claude Code MCP guide](https://code.claude.com/docs/en/mcp).

### Google Antigravity

In the IDE's agent side panel, open **… → MCP Servers → Manage MCP Servers →
View raw config**. Merge this into `mcp_config.json`:

```json
{
  "mcpServers": {
    "traceanvil": {
      "command": "/absolute/path/to/build/mcp-native-diagnostics",
      "args": ["serve", "--config", "/absolute/path/to/config.local.json"]
    }
  }
}
```

The global file is `~/.gemini/config/mcp_config.json`; for a workspace-only
setup, use `.agents/mcp_config.json`. These files also work with Antigravity CLI;
use `/mcp` there to reload the configuration and inspect connection status.
See the [Google Antigravity MCP guide](https://antigravity.google/docs/mcp).

### First request

With any of these clients, try:

> Use TraceAnvil's system_capabilities tool to list available backends and
> tools. Then run Memcheck on the configured solver-tests target, if available,
> and summarize the diagnostics with their run_id and raw evidence references.

`solver-tests` is the target ID in `config.example.json`; replace it with your
configured target ID. Only enabled, permitted and usable tools will appear.

## Safety and storage

Allowlisted programs run as your user. **Path restrictions are not a sandbox**;
untrusted binaries require separate system isolation. Attach and experimental
features require explicit configuration and existing OS permissions.

Raw evidence is stored under `report_dir`. Automatic startup purging is disabled
by default. Stop the server before inspecting and applying retention:

```sh
./build/mcp-native-diagnostics purge --config config.local.json --dry-run
./build/mcp-native-diagnostics purge --config config.local.json
```

Read [SECURITY.md](SECURITY.md) and the [operations guide](docs/operations.md)
for trust boundaries, retention, backup and upgrade guidance.

## Documentation

- [Configuration](docs/configuration.md) · [MCP tools, schemas and errors](docs/mcp.md)
- [Valgrind](docs/valgrind.md) · [Sanitizers](docs/sanitizers.md) · [Diagnostic diffs](docs/diff.md)
- [GDB and LLDB](docs/debugger.md) · [DAP](docs/dap.md) · [Core analysis and attach](docs/core-and-attach.md)
- [CTest](docs/ctest.md) · [Profiling](docs/profile.md) · [Experimental perf and vgdb](docs/experimental.md)
- [Architecture](docs/architecture.md) · [Artifacts](docs/artifacts.md) · [Compatibility](docs/compatibility.md)
- [Releases](docs/release.md) · [Changelog](CHANGELOG.md) · [Governance](GOVERNANCE.md)

## Development

```sh
./scripts/preflight.sh  # Read-only machine inventory
./scripts/check.sh      # Formatting, dependency verification, vet, tests, race, build
```

A C toolchain is required for race tests and native fixtures. Backend integration
tests skip with a reason when their required tools are unavailable.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow and
[release tooling](docs/release.md) for binaries, Debian/RPM packages, SBOMs and checksums.

## License

[Apache License 2.0](LICENSE).
