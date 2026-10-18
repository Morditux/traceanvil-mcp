# ADR 0001 — MCP tool exposure and capability filtering

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J0 (foundations), enforced from J1

## Context

The server fronts optional native tools (`valgrind`, `gdb`, `lldb-dap`, sanitizers,
`perf`, `ctest`). Their presence, version and permissions differ between machines.
Advertising tools that cannot work would force clients to interpret
"not installed" errors and would turn capability discovery into guesswork.
Conversely, discovering capabilities must never execute a user program or require
privileges.

## Decision

1. **Separate registries.** A Capability Registry
   (`internal/capabilities`) records per-backend state; the MCP tool registry is
   built from it. Handlers contain no business logic.
2. **Four-condition exposure.** A tool is published only when
   `implemented && enabled_by_config && permitted_by_policy && runtime_ready`.
   A binary on `PATH` is not sufficient.
3. **Explicit states.** Each capability is `available`, `unavailable`, `disabled`,
   `unsupported` or `unknown`, with resolved absolute path, version, backend,
   supported features, reason, last-probe time and probe results.
4. **Probes are inert.** Startup probes use short timeouts, do not run user
   programs, do not require `sudo`, and do not modify system settings.
5. **Revalidation at invocation.** Every call rechecks availability, policy and
   session capabilities. A backend that vanished between discovery and execution
   returns `backend_unavailable`, never a substitute execution and never
   "no errors observed".
6. **Controlled list changes.** `tools/list` changes only after an external or
   administrative event (configuration reload, installation/removal, optional
   health check), never as a side effect of a business call. The server declares
   `tools.listChanged`, compares tool names and schemas, and emits
   `notifications/tools/list_changed` after the protocol handshake.
7. **Restricted schemas.** Common debug operations expose only the `backend`
   values actually usable on the machine. Per-session capabilities are still
   checked on every call and may return `unsupported_capability`.
8. **No escape hatch.** There is no `debug_execute_command`, no generic
   `evaluate`, no free-form Valgrind flag string and no client-supplied
   executable path. Options are strict typed fields.
9. **Stable envelope.** Stateful results carry `schema_version = 1`, opaque
   `run_id`/`session_id`, explicit job state, business result, capabilities,
   warnings and truncation flags. Incompatible changes require a new schema
   version and an ADR.

## SDK pinning

J0 adds no external Go dependency. The official
`github.com/modelcontextprotocol/go-sdk/mcp` module will be added in J1 at an
exact release version, recorded in `go.mod`, `go.sum` and the pull request.
`@latest` is never used in reproducible scripts or CI.

## Consequences

- More work in J1 (registry, probes, notifications) and a larger test matrix
  (no backend, single backend, all backends, restricted kernel permissions,
  backend removed mid-session, added/removed between two `tools/list` calls).
- Tool lists legitimately differ between machines; clients must reload or
  reconnect when notified.
- Some clients may not refresh dynamically; documentation must state that a
  restart is a valid fallback.
