# Compatibility inventory

Recorded on 2026-10-18 from a **read-only** inspection of the reference development
machine and refreshed at J3 from a read-only inspection. Re-run
`./scripts/preflight.sh` after changing the toolchain and update this table; CI
runners may differ.

## Host

| Item | Value |
|---|---|
| Distribution | Ubuntu 26.04.1 LTS |
| Kernel | 7.0.0-38-generic |
| Architecture | x86_64 (amd64) |
| Go | go1.27.0 |
| GCC / G++ | 15.2.0 |
| CMake / CTest | 4.2.3 |
| GDB | 17.1 |
| LLDB / lldb-dap | 21.1.8 |
| Clang / Clang++ | 21.1.8 |
| Valgrind / vgdb | 3.26.0 |
| Ninja | 1.13.2 |
| ShellCheck | 0.11.0 |
| Perf | 7.0.14 (installed, kernel-restricted) |
| Git | 2.53.0 |

## Build dependencies

| Module | Version |
|---|---|
| Go module baseline | `go 1.25.0` |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` |

Transitive modules are pinned in `go.sum`. `./scripts/check.sh` runs
`go mod tidy -diff` so an untidy module graph fails locally and in CI.

## Backends and tools

| Component | Observed state | Consequence | Milestone |
|---|---|---|---|
| Go toolchain | available (1.27.0) | build and tests enabled | J0 |
| gcc / g++ | available (15.2.0) | C/C++ fixtures and `-race` enabled | J2+ |
| cmake / ctest | available (4.2.3) | CTest integration possible | J10 |
| gdb | available (17.1); DAP mode not yet probed | GDB tooling stays hidden until a DAP probe succeeds | J6 |
| gdbserver | available (17.1); not probed | remote/`vgdb` scenarios possible but experimental | J13 |
| valgrind | available (3.26.0); Memcheck XML probe passes | internal Memcheck pipeline available; no MCP tool until J4 | J3 |
| vgdb | available (Valgrind 3.26.0); not probed | experimental scenarios deferred | J13 |
| callgrind_annotate / ms_print | available (3.26.0) | Callgrind/Massif summaries possible | J12 |
| lldb / lldb-dap | available (21.1.8); DAP not yet probed | LLDB backend stays hidden until the J7 probe | J7 |
| clang / clang++ | available (21.1.8) | sanitizer fixtures possible | J9 |
| ninja | available (1.13.2) | optional CMake generator | J10 |
| shellcheck | available (0.11.0) | optional script linting | — |
| perf | present, but `perf_event_paranoid=4` | profiling remains experimental and must report the restriction | J13 |
| jq / xmllint / python3 | available | inspection and fixture utilities | — |

Suggested explicit installation (human decision, never performed by the server):

At J3, Valgrind, GDB, LLDB, Clang, Ninja, ShellCheck, gdbserver,
`callgrind_annotate` and `ms_print` are installed. No optional package used by
the J3 tests is missing.

`lldb-dap` may be shipped as `lldb-dap-<version>`; discovery must handle both names
and record the resolved absolute path.

## Kernel and security settings

Read only; the project never modifies these values.

| Knob | Value | Meaning |
|---|---|---|
| `kernel.yama.ptrace_scope` | 1 | Restricted ptrace; attach to unrelated processes is denied. |
| `kernel.perf_event_paranoid` | 4 | Performance events are effectively unavailable to unprivileged users. |
| `kernel.kptr_restrict` | 1 | Kernel pointers are hidden. |

## Expected behaviour on this machine

- The server starts and serves `system_capabilities` regardless of backend
  availability.
- `memcheck_run`, `helgrind_run`, `drd_run`, `profile_callgrind`,
  `profile_massif`, `profile_perf` and the LLDB backend must **not** appear in
  `tools/list` at J3. The Memcheck pipeline is internal until J4.
- `backend.valgrind` is `available` after the functional Memcheck/XML help probe;
  every run revalidates the binary.
- GDB and LLDB records remain `unknown` until their isolated DAP probes (J6/J7);
  a binary on `PATH` alone is not sufficient.
- No tool reports "no errors" when its backend is missing; the result is an
  explicit unavailable/unsupported status.

## Notes

- At J1, `doctor` reported every backend as `unknown` (binary discovered) or
  `unavailable`. As of J3, `backend.valgrind` is `available` on this machine;
  `backend.gdb` and `backend.lldb` remain `unknown` until J6/J7.
- Valgrind support varies with kernel, architecture and instruction set. The J3
  probe checks `--version` plus the Memcheck XML options in `--tool=memcheck
  --help`; the first authorized fixture run revalidates the binary. A help check
  is still not a substitute for running the tool on a real target.
- DAP behaviour differs between GDB and LLDB; each discrepancy must be covered by
  a capability check and a degraded behaviour test.
