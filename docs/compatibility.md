# Compatibility inventory

Recorded on 2026-10-18 from a **read-only** inspection of the reference development
machine and refreshed at J6 from a read-only inspection plus real GDB DAP tests.
Re-run `./scripts/preflight.sh` after changing the toolchain and update this
table; CI runners may differ.

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
| gdb | available (17.1); DAP `initialize` verified | `backend.gdb` becomes `available`; the J6 debug tools are exposed while it remains available | J6 |
| gdbserver | available (17.1); not probed | remote/`vgdb` scenarios possible but experimental | J13 |
| valgrind | available (3.26.0); Memcheck XML probe passes | Memcheck pipeline and `analysis_start` exposed while the capability is available | J3/J4 |
| vgdb | available (Valgrind 3.26.0); not probed | experimental scenarios deferred | J13 |
| callgrind_annotate / ms_print | available (3.26.0) | Callgrind/Massif summaries possible | J12 |
| lldb / lldb-dap | available (21.1.8); DAP not yet probed | LLDB backend stays hidden until the J7 probe | J7 |
| clang / clang++ | available (21.1.8) | sanitizer fixtures possible | J9 |
| ninja | available (1.13.2) | optional CMake generator | J10 |
| shellcheck | available (0.11.0) | optional script linting | — |
| perf | present, but `perf_event_paranoid=4` | profiling remains experimental and must report the restriction | J13 |
| jq / xmllint / python3 | available | inspection and fixture utilities | — |

Suggested explicit installation (human decision, never performed by the server):

At J6, Valgrind, GDB, LLDB, Clang, Ninja, ShellCheck, gdbserver,
`callgrind_annotate` and `ms_print` are installed. No package was installed for
J6; the real GDB tests use the existing 17.1 package. No optional package used by
the J6 tests is missing.

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
- On this machine `backend.valgrind` is `available`, so `analysis_start` is
  published; `backend.gdb` is also `available` after a real DAP `initialize`, so
  `debug_start` is published while at least one declared target currently
  resolves, and the other GDB action tools are published while GDB is available.
  `helgrind_run`, `drd_run`, `profile_callgrind`, `profile_massif`,
  `profile_perf` and the LLDB backend must **not** appear in `tools/list` at J6.
  The analysis evidence tools and debug management tools stay published where
  documented.
- `backend.valgrind` is `available` after the functional Memcheck/XML help probe;
  every run revalidates the binary, and a capability refresh removes
  `analysis_start` if the backend disappears. The GDB probe launches `gdb
  --quiet --nx --interpreter=dap`, completes `initialize`, records capabilities
  and closes the process; it never loads a target. Every debug start revalidates
  the binary and target policy.
- LLDB remains `unknown` until its isolated DAP probe (J7); a binary on `PATH`
  alone is not sufficient. The J6 surface is GDB-only.
- GDB 17.1 does not answer `launch` until `configurationDone` arrives, and
  dispatches the two requests concurrently. The implementation writes `launch`,
  retries `configurationDone` only on the transient "launch or attach not
  specified" response, and awaits the launch future. This quirk is covered by
  real GDB integration tests and documented in [debugger.md](debugger.md).
- GDB 17.1 advertises `supportTerminateDebuggee=true`. `debug_stop` nevertheless
  sends `disconnect(terminateDebuggee=true)` and then terminates the GDB process
  group (TERM, then KILL); a session never relies solely on the adapter
  capability to stop the debuggee. A fixture that writes its own PID verifies
  that the debuggee is gone after `debug_stop`. J6 refuses
  `debug_stop(terminate=false)` with `unsupported_capability`: a real detach test
  showed the inferior was not reliably preserved when GDB was released, so
  detach is deferred rather than reported falsely. The session remains running
  and can be stopped explicitly.
- Real tests verify stop-on-entry, verified breakpoints, continue, pause, step,
  threads, stack/scopes/locals, stripped-symbol degradation, stop while running,
  debuggee PID termination and GDB process cleanup. GDB 17.1 gives GDB and the
  debuggee different process group ids; cleanup of the debuggee is verified
  explicitly rather than inferred from the GDB group. GDB and `cc` are required; tests skip with a documented
  reason when either is unavailable.
- No tool reports "no errors" when its backend is missing; the result is an
  explicit unavailable/unsupported status. Missing symbols are reported as
  missing, not synthesized.

## Notes

- At J1, `doctor` reported every backend as `unknown` (binary discovered) or
  `unavailable`. As of J3/J4, `backend.valgrind` is `available` on this machine.
  At J6, `backend.gdb` is also `available` after the DAP handshake;
  `backend.lldb` remains `unknown` until J7. The GDB capability probe does not
  run a user program, but every `debug_start` executes the declared target, which
  remains untrusted code.
- Valgrind support varies with kernel, architecture and instruction set. The J3
  probe checks `--version` plus the Memcheck XML options in `--tool=memcheck
  --help`; the first authorized fixture run revalidates the binary. A help check
  is still not a substitute for running the tool on a real target.
- DAP behaviour differs between GDB and LLDB; each discrepancy must be covered by
  a capability check and a degraded behaviour test.
