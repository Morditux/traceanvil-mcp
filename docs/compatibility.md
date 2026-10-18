# Compatibility inventory

Recorded on 2026-10-18 from a **read-only** inspection of the reference development
machine. Re-run `./scripts/preflight.sh` after changing the toolchain and update
this table; CI runners may differ.

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
| Perf | 7.0.14 (installed, kernel-restricted) |
| Git | 2.53.0 |

## Backends and tools

| Component | Observed state | Consequence | Milestone |
|---|---|---|---|
| Go toolchain | available (1.27.0) | build and tests enabled | J0 |
| gcc / g++ | available (15.2.0) | C/C++ fixtures and `-race` enabled | J2+ |
| cmake / ctest | available (4.2.3) | CTest integration possible | J10 |
| gdb | available (17.1); DAP mode not yet probed | GDB tooling stays hidden until a DAP probe succeeds | J6 |
| gdbserver | **not installed** | remote/`vgdb` scenarios unavailable | J13 |
| valgrind / vgdb | **not installed** | Memcheck, Helgrind, DRD, Callgrind, Massif tools unavailable | J3, J9, J12, J13 |
| callgrind_annotate / ms_print | **not installed** | Callgrind/Massif summaries unavailable | J12 |
| lldb / lldb-dap | **not installed** | LLDB backend and `backend=lldb` unavailable | J7 |
| clang / clang++ | **not installed** | Clang-built sanitizer fixtures unavailable; GCC remains usable | J9 |
| ninja | **not installed** | Optional; CMake can use Makefiles | J10 |
| perf | present, but `perf_event_paranoid=4` | profiling remains experimental and must report the restriction | J13 |
| jq / xmllint / python3 | available | inspection and fixture utilities | — |
| shellcheck | **not installed** | optional script linting | — |

Suggested explicit installation (human decision, never performed by the server):

```sh
sudo apt-get install -y --no-install-recommends \
  valgrind gdb lldb clang ninja-build shellcheck
```

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

- The server starts and serves `system_capabilities` even though Valgrind and
  LLDB are absent.
- `memcheck_run`, `helgrind_run`, `drd_run`, `profile_callgrind`,
  `profile_massif`, `profile_perf` and the LLDB backend must **not** appear in
  `tools/list`.
- GDB tools appear only after a successful, isolated DAP probe. A `gdb` binary on
  `PATH` alone is not sufficient.
- No tool reports "no errors" when its backend is missing; the result is an
  explicit unavailable/unsupported status.

## Notes

- Valgrind support varies with kernel, architecture and instruction set. A probe
  based on `--version` does not prove that Memcheck can run; an authorized fixture
  run is required during J3.
- DAP behaviour differs between GDB and LLDB; each discrepancy must be covered by
  a capability check and a degraded behaviour test.
