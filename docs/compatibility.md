# Compatibility inventory

Recorded on 2026-10-18 from a **read-only** inspection of the reference development
machine and refreshed at J8 from a read-only inspection plus real GDB/LLDB
and deterministic diff tests.
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
| gdb | available (17.1); DAP `initialize` verified | `backend.gdb` becomes `available`; GDB debug sessions are exposed while it remains available | J6 |
| gdbserver | available (17.1); not probed | remote/`vgdb` scenarios possible but experimental | J13 |
| valgrind | available (3.26.0); Memcheck XML probe passes | Memcheck pipeline and `analysis_start` exposed while the capability is available | J3/J4 |
| vgdb | available (Valgrind 3.26.0); not probed | experimental scenarios deferred | J13 |
| callgrind_annotate / ms_print | available (3.26.0) | Callgrind/Massif summaries possible | J12 |
| lldb / lldb-dap | available (21.1.8); `lldb-dap` and `lldb-dap-21` discovered; DAP `initialize` verified | `backend.lldb` becomes `available`; LLDB debug sessions are exposed while it remains available | J7 |
| clang / clang++ | available (21.1.8) | sanitizer fixtures possible | J9 |
| ninja | available (1.13.2) | optional CMake generator | J10 |
| shellcheck | available (0.11.0) | optional script linting | — |
| perf | present, but `perf_event_paranoid=4` | profiling remains experimental and must report the restriction | J13 |
| jq / xmllint / python3 | available | inspection and fixture utilities | — |

Suggested explicit installation (human decision, never performed by the server):

At J8, Valgrind, GDB, LLDB, Clang, Ninja, ShellCheck, gdbserver,
`callgrind_annotate` and `ms_print` are installed. No package was installed for
J8; the real GDB/LLDB tests use the existing GDB 17.1 and LLDB 21.1.8 packages.
The diff tests are deterministic and need no additional toolchain.

`lldb-dap` is shipped here as both `lldb-dap` and `lldb-dap-21`; discovery handles
both names, prefers the unsuffixed adapter, orders versioned candidates newest
first, caps the candidate list and records the resolved absolute path.

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
  resolves, and the debug action tools are published while at least one debugger
  backend is available. `backend.lldb` is also `available` after its DAP
  handshake and versioned discovery. `helgrind_run`, `drd_run`,
  `profile_callgrind`, `profile_massif` and `profile_perf` must **not** appear in
  `tools/list` at J8. The analysis evidence tools, `diagnostics_diff` and debug
  management tools stay published where documented.
- `backend.valgrind` is `available` after the functional Memcheck/XML help probe;
  every run revalidates the binary, and a capability refresh removes
  `analysis_start` if the backend disappears. The GDB probe launches `gdb
  --quiet --nx --interpreter=dap`, completes `initialize`, records capabilities
  and closes the process; it never loads a target. Every debug start revalidates
  the binary and target policy. The LLDB probe discovers `lldb-dap` and
  `lldb-dap-<N>` in absolute PATH directories, verifies `initialize` with fixed
  init settings and never loads a target.
- `diagnostics_diff` is published whenever the analysis service exists, even
  when Valgrind is unavailable. It compares persisted reports only. New reports
  use `sha256-v2`; v1/v2 pairs are `incompatible_runs`. Downgrading
  configuration/evidence changes classify items `uncertain`; binary/path/outcome
  changes are reported without discarding classification.
- LLDB is no longer discovery-only: `backend.lldb` is `available` on this
  machine. A binary on `PATH` alone remains insufficient; the DAP handshake is
  required. Discovery prefers `lldb-dap`, then versioned candidates in
  descending numeric order, with a two-candidate cap. Both backends expose the
  same MCP tool names and differ in adapter details documented below and in
  [debugger.md](debugger.md).
- lldb-dap emits its initialize response with `seq:0` (all observed messages use
  sequence 0), sends the DAP `initialized` event only after `launch`, and reports
  stop-on-entry as a `reason:"exception"` SIGSTOP. The client accepts zero
  sequence numbers and the backend applies the LLDB-specific launch order.
- lldb-dap thread ids are process ids (for example `threadId=121`), unlike GDB's
  small thread ids. Stack frame ids are large opaque integers. The backend seeds
  a pause target from the process event and uses the stopped thread id.
- lldb-dap gives the inferior a private PTY (`/dev/pts/N`), whereas GDB uses
  `/dev/null`; neither shares the DAP pipe. LLDB emits module, capabilities,
  output and large `$__lldb_statistics` terminated events; the event loop drains
  and bounds them.
- GDB 17.1 does not answer `launch` until `configurationDone` arrives, and
  dispatches the two requests concurrently. The implementation writes `launch`,
  retries `configurationDone` only on the transient "launch or attach not
  specified" response, and awaits the launch future. This quirk is covered by
  real GDB integration tests and documented in [debugger.md](debugger.md).
- GDB 17.1 advertises `supportTerminateDebuggee=true`. `debug_stop` nevertheless
  sends `disconnect(terminateDebuggee=true)` and then terminates the GDB process
  group (TERM, then KILL); a session never relies solely on the adapter
  capability to stop the debuggee. A fixture that writes its own PID verifies
  that the debuggee is gone after `debug_stop`. J6/J7 refuse
  `debug_stop(terminate=false)` with `unsupported_capability`: a real detach test
  showed the inferior was not reliably preserved when the debugger was released,
  so detach is deferred rather than reported falsely. The session remains
  running and can be stopped explicitly. LLDB advertises the capability too, but
  uses the same refusal.
- Real tests verify stop-on-entry, verified breakpoints, continue, pause, step,
  threads, stack/scopes/locals, stripped-symbol degradation, stop while running,
  debuggee PID termination and GDB/LLDB process cleanup. GDB 17.1 gives GDB and
  the debuggee different process group ids; cleanup of the debuggee is verified
  explicitly rather than inferred from the debugger group. GDB, `lldb-dap` and
  `cc` are required for the full suite; tests skip with a documented reason when
  one is unavailable. LLDB tests cover the same common workflow: stop-on-entry,
  verified breakpoints, continue, pause, step, threads, stack/scopes/locals,
  stripped-symbol degradation, explicit unsupported detach, debuggee PID
  termination and stdio MCP end to end.
- No tool reports "no errors" when its backend is missing; the result is an
  explicit unavailable/unsupported status. Missing symbols are reported as
  missing, not synthesized.

## Notes

- At J1, `doctor` reported every backend as `unknown` (binary discovered) or
  `unavailable`. As of J3/J4, `backend.valgrind` is `available` on this machine.
  At J6, `backend.gdb` became `available` after the DAP handshake. At J7,
  `backend.lldb` is also `available` through `lldb-dap` 21.1.8 and its
  versioned-discovery probe. At J8, diagnostics_diff compares persisted reports
  without running a program; new reports carry fingerprint v2. Neither capability
  probe runs a user program, but every `debug_start` executes the declared target,
  which remains untrusted code.
- Valgrind support varies with kernel, architecture and instruction set. The J3
  probe checks `--version` plus the Memcheck XML options in `--tool=memcheck
  --help`; the first authorized fixture run revalidates the binary. A help check
  is still not a substitute for running the tool on a real target.
- DAP behaviour differs between GDB and LLDB; each discrepancy must be covered by
  a capability check and a degraded behaviour test.
- Fingerprint v2 is not a proof of identity. Inlining, missing symbols and path
  remapping can change a digest; v1 and v2 reports are never compared across
  recipes. Duplicate fingerprints and incomplete reports are explicitly
  uncertain.
