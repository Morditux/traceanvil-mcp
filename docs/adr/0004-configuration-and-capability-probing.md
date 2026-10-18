# ADR 0004 — Configuration schema and capability probing at J1

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J1

## Context

J1 introduces the first externally visible MCP surface, strict configuration and
capability discovery. Two failure modes must be avoided:

1. overstating readiness — reporting a backend as available merely because a
   binary exists on `PATH`;
2. turning configuration mistakes or a missing tool into a server crash.

The server must also reserve stdout for the MCP protocol and remain usable with
no analysis backend installed.

## Decision

### Configuration

- One strict JSON document, `schema_version = 1`, unknown fields rejected,
  maximum 1 MiB, no trailing JSON value.
- `workspace_roots`, `report_dir`, target `program` and target `cwd` must be
  absolute, clean, bounded and outside `/`, `/proc`, `/sys` and `/dev`.
- `program` and `cwd` must be lexically inside a declared root. Symlink
  resolution and runtime revalidation are deferred to the execution layer (J2),
  because validation must not require the target to exist.
- Target IDs are restricted to `[a-z0-9._-]`, unique and at most 64 bytes.
- Limits have documented defaults and ranges; zero selects the default, and
  out-of-range values are rejected.
- `attach` and `evaluate` require the `elevated-local` profile. That profile is
  accepted but its features are not implemented at J1.
- `validate-config` performs no filesystem access. `doctor` adds stat-only
  warnings for missing roots, targets and report directories.

### Capability probing

- A registry stores one record per capability with state
  `available|unavailable|disabled|unsupported|unknown`, resolved path, version,
  reason, last check, probe outcome and duration.
- Probes run concurrently with a per-probe timeout (5 seconds by default), a
  panic recovery boundary and bounded output capture.
- Version probes invoke only the resolved binary with `--version`, a minimal
  `LC_ALL=C` environment and `WaitDelay`, so a grandchild cannot hold the pipes
  open past the deadline. No user program is executed. Relative `PATH` entries
  are rejected so a binary planted in the process working directory cannot be
  picked up implicitly.
- `lldb-dap` was resolved but not executed at J1 because its `--version`
  behavior is not guaranteed to terminate. The J7 probe executes it only for a
  bounded DAP initialize handshake and treats `--version` as optional.
- Discovery success yields `unknown`, never `available`. A binary being present
  does not prove its analysis features work. Functional probes arrive with J3
  (Valgrind), J6 (GDB) and J7 (LLDB); the J7 probe discovers versioned
  `lldb-dap` names and requires a DAP handshake.
- Disabled backends yield `disabled`; missing executables yield `unavailable`.
  Neither aborts server startup.
- The registry is safe for concurrent reads during refresh.

### Tool publication

- Tool definitions declare a required capability; an empty requirement means
  always implemented and usable.
- `SyncTools` reconciles the published list against the registry and uses the
  SDK's `AddTool`/`RemoveTools`, which emit `tools/list_changed`.
- The server advertises `tools.listChanged = true` during initialization.
- At J1, no periodic or business-call-triggered refresh exists. `SyncTools` is
  exercised by tests and will be driven by controlled administrative events in
  later milestones. Action tools must revalidate capabilities at invocation.

### MCP surface and transport

- The MCP server uses `github.com/modelcontextprotocol/go-sdk/mcp` pinned at
  **v1.8.0** (verified against the module API, `go 1.25.0`).
- `serve` uses `IOTransport` over the injected stdin/stdout, which makes the
  transport testable. stdout carries only JSON-RPC messages; logs go to stderr.
- `system_capabilities` is read-only, has no side effects, and returns
  `schema_version = 1`, build identity, policy, limits, capability records,
  tool statuses and warnings.
- Capabilities are not re-probed on each status call at J1. The payload contains
  `last_checked`; action tools will revalidate before executing anything.

## Consequences

- A backend may be reported `unknown` even when its binary is installed. This is
  intentional and documented; later functional probes will upgrade the state.
- Configuration files can be validated before targets are built. `doctor`
  surfaces the corresponding warnings.
- The tool list can legitimately differ between machines and does not include
  analysis tools yet.
- Extended `elevated-local` permissions validate but have no effect until their
  milestones.
