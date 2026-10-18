# MCP Native Diagnostics v1.0.0

This is the first v1.0 release of the local stdio MCP server for native
diagnostics. It is a trust-based development tool: targets, projects, core
profiles and elevated features are explicit administrator declarations.

## Highlights

- MCP server over stdio with capability-filtered tools and
  `notifications/tools/list_changed`.
- Valgrind Memcheck, Helgrind and DRD pipelines with bounded XML evidence and
  canonical reports.
- ASan, UBSan, LSan and TSan parsers for declared instrumented targets.
- Trusted CTest discovery, exact test selection, timeouts and JUnit reports.
- GDB/LLDB DAP debugging, trusted target launch, and controlled GDB attach with
  explicit PID allowlists and Yama/ownership checks.
- Configured core-dump analysis with fixed GDB commands and raw evidence.
- Callgrind/Massif native parsers, top self costs, peak heap and cautious
  comparisons.
- Experimental opt-in `perf stat` and fixed-command `vgdb` inspection.
- Bounded artifacts, explicit retention/purge, SPDX SBOMs and release scripts.

## Requirements

- Linux, reference Ubuntu 24.04 LTS; CI also exercises Ubuntu 22.04.
- Go 1.25+ to build from source.
- Optional backends are installed separately: Valgrind, GDB, LLDB/lldb-dap,
  Clang, CMake/CTest, `perf`, `vgdb`. `doctor` reports what is actually usable.
- No privileged helper, daemon or automatic sudo/sysctl change is installed.

## Install

Binary:

```sh
sha256sum -c SHA256SUMS
./mcp-native-diagnostics_v1.0.0_linux_amd64 version
./mcp-native-diagnostics_v1.0.0_linux_amd64 doctor --config config.json
```

Debian package (amd64/arm64):

```sh
sudo dpkg -i mcp-native-diagnostics_1.0.0_amd64.deb
mcp-native-diagnostics version
```

The package installs only the binary and documentation under
`/usr/share/doc/mcp-native-diagnostics`. It does not install a service,
maintainer script, setuid bit or configuration in `/etc`.

## Evidence and safety

Each run gets an opaque directory under `report_dir` with raw evidence,
normalized reports and completion warnings. Incomplete, truncated, timed-out or
canceled runs are never reported as clean. `storage.*` bounds retention; the
explicit `purge` command takes a `flock` and must be run while the server is
stopped. `docs/operations.md` contains the operational checklist.

The server never runs a shell, accepts arbitrary executable paths or exposes
free debugger/monitor commands. Elevated attach and experimental perf/vgdb
surfaces are opt-in and clearly labeled.

## Verification

- `./scripts/check.sh`, `go test -race ./...`, fuzz smoke and large-log
  benchmarks pass on the reference host.
- Release artifacts are produced with `-trimpath`, `-buildvcs=false` and a
  commit-derived build date. The release includes:
  - Linux amd64/arm64 binaries;
  - amd64/arm64 Debian packages;
  - SPDX 2.3 SBOMs;
  - sorted `SHA256SUMS`.
- Changelog: [CHANGELOG.md](../CHANGELOG.md)
- Operations: [operations.md](operations.md)
- Release process: [release.md](release.md)

Checksums are unsigned unless a release operator signs `SHA256SUMS` with the
documented GPG procedure. No signed claim is made here.
