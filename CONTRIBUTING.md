# Contributing

Thank you for improving MCP Native Diagnostics. This document describes the
expectations that keep the project safe and reviewable.

## Prerequisites

- Linux, preferably Ubuntu 24.04 LTS or newer.
- Go 1.25 or newer.
- `gcc` (required by `go test -race`).
- For backend work: `valgrind`, `gdb` with DAP support, `lldb`/`lldb-dap`,
  `clang`, `cmake`/`ctest`. Run `./scripts/preflight.sh` for a read-only
  inventory; missing tools must be installed explicitly.

## Getting started

```sh
git clone git@github.com:Morditux/traceanvil-mcp.git
cd traceanvil-mcp
go build ./...
./scripts/check.sh
```

## Before opening a pull request

`./scripts/check.sh` must pass. It runs:

```sh
gofmt -l .          # must print nothing
go mod tidy -diff
go mod verify
go vet ./...
go test ./...
go test -race ./...
go build ./...
```

Useful bounded checks:

```sh
GOCACHE="$PWD/.gocache" FUZZ_TIME=5s ./scripts/fuzz.sh
GOCACHE="$PWD/.gocache" BENCH_TIME=3x ./scripts/bench.sh
```

Parser/API changes should add a fuzz target or seed and a benchmark when they
process potentially large evidence.

Pull requests should also:

- include positive **and** negative tests for new behaviour (invalid input,
  cancellation, timeout, policy denial);
- keep `analysis_complete` semantics honest: partial data must never be reported
  as "no errors";
- preserve raw evidence and reference it from normalized diagnostics;
- document new configuration values, limits and error codes;
- pin any new dependency to an exact version and justify it in the pull request;
- avoid exposing a new execution surface without a policy check and tests.

## Code style

- Standard Go style; `gofmt` is mandatory.
- Small, explicit interfaces with constructor injection. No mutable global
  singletons for sessions or jobs.
- Business logic belongs in `internal/*` packages, not in MCP handlers.
- Use `context.Context` for anything that can block or spawn a process.
- Return typed, actionable errors. Do not invent fields that the underlying tool
  did not report.
- Documentation, comments, identifiers and commit messages are written in
  **English**.
- Keep documentation and fixtures in sync with behaviour.

## Security rules

Contributions must not:

- introduce free-form shell execution or client-supplied executable paths;
- weaken or bypass Yama, `perf_event_paranoid`, AppArmor, seccomp or similar
  controls to make a test pass (document the missing capability instead);
- use `sudo` at server runtime or escalate privileges;
- log secrets, environment dumps or unbounded tool output;
- merge a feature that executes user programs without limits, cancellation and
  cleanup.

Read [SECURITY.md](SECURITY.md) before touching the runner, policy, artifact store
or debugger backends.

## Releases

Release changes must follow [docs/release.md](docs/release.md) and
[GOVERNANCE.md](GOVERNANCE.md): clean tree, full gate, checksums, SBOM, and
optional human-controlled signing. Do not publish an unsigned tag as a signed
release and do not commit `dist/`.

## Commit messages

Use the imperative mood, a short subject line and a body explaining *why*:

```text
Add bounded process-group cleanup

Track the child process group so cancellation terminates the whole tree,
with TERM followed by KILL after a grace period.
```

Keep commits focused; do not mix refactoring with behaviour changes.

## Licensing

By contributing, you agree that your contribution is licensed under the Apache
License 2.0 (see [LICENSE](LICENSE)). No separate CLA is required.
