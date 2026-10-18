# MCP surface

The server speaks MCP over **stdio**. stdout carries only newline-delimited
JSON-RPC 2.0 messages; logs go to stderr. There is no HTTP transport, no
telemetry and no remote access.

## Tool discovery

`tools/list` is computed from the capability registry. A tool is published only
when all of the following hold:

1. the tool is implemented in this build;
2. configuration enables its backend;
3. local policy permits the operation;
4. the required capability is `available`.

Tools are reconciled at startup and can be refreshed on controlled events in
later milestones. The server advertises `tools.listChanged = true` and emits
`notifications/tools/list_changed` when the list changes. Clients that do not
refresh dynamically must reconnect.

At J3 only one tool is implemented: `system_capabilities`. It is always
available and read-only. The Memcheck pipeline exists internally and writes
`analysis.json`/`memcheck.xml`, but `analysis_start`, `analysis_status`,
`analysis_report` and `analysis_artifact` are J4 work and must not appear in
`tools/list` yet.

## `system_capabilities`

**Input:** an empty object.

```json
{}
```

**Output (`structuredContent`):**

```json
{
  "schema_version": 1,
  "server": {
    "name": "mcp-native-diagnostics",
    "version": "0.0.0-dev",
    "commit": "0da022a…",
    "date": "2026-10-18T00:42:04Z",
    "go_version": "go1.27.0",
    "modified": false
  },
  "policy": {
    "profile": "safe-default",
    "attach": false,
    "evaluate": false
  },
  "limits": {
    "timeout_seconds": 120,
    "max_jobs": 2,
    "max_sessions": 2,
    "max_report_bytes": 10485760,
    "max_response_items": 100
  },
  "capabilities": [
    {
      "id": "backend.valgrind",
      "kind": "backend",
      "backend": "valgrind",
      "state": "available",
      "path": "/usr/bin/valgrind",
      "version": "valgrind-3.26.0",
      "features": ["memcheck", "xml-output", "suppressions"],
      "reason": "binary and requested feature verified (Memcheck tool and XML options verified from help output; every run revalidates the binary)",
      "last_checked": "2026-10-18T12:00:00Z",
      "probe": {
        "attempted": true,
        "success": true,
        "duration_ms": 18
      }
    },
    {
      "id": "backend.gdb",
      "kind": "backend",
      "backend": "gdb",
      "state": "unknown",
      "path": "/usr/bin/gdb",
      "version": "GNU gdb (Ubuntu 17.1-2ubuntu1) 17.1",
      "reason": "binary detected; functional support not verified (DAP probe planned for J6)",
      "last_checked": "2026-10-18T12:00:00Z",
      "probe": {
        "attempted": true,
        "success": true,
        "duration_ms": 12
      }
    }
  ],
  "tools": [
    {
      "name": "system_capabilities",
      "state": "available",
      "exposed": true,
      "reason": "implemented and enabled"
    }
  ],
  "warnings": [
    "Only system_capabilities is implemented at this milestone; analysis tools arrive with J4.",
    "Valgrind is functionally probed for Memcheck XML support; GDB and LLDB remain discovery-only (J6/J7).",
    "Capability probes run at startup only; restart the server after installing or removing a backend."
  ]
}
```

The tool also returns the same JSON as text content. `schema_version` is `1` and
will change only for an incompatible payload change.

### Capability states

| State | Meaning |
|---|---|
| `available` | Functionally verified and usable. |
| `unavailable` | Required executable or resource is missing. |
| `disabled` | Explicitly disabled by configuration. |
| `unsupported` | Backend present but the feature is not supported. |
| `unknown` | Could not be verified, including binary-only discovery. |

At J3, Valgrind becomes `available` only after the binary and its Memcheck XML
options are verified. GDB and LLDB remain discovery-only and therefore `unknown`
when their executable exists: a binary on `PATH` does not prove that a DAP
session works.

### Probe fields

| Field | Meaning |
|---|---|
| `probe.attempted` | A probe was started. |
| `probe.success` | The probe mechanism completed (not that the capability is ready). |
| `probe.duration_ms` | Probe duration in milliseconds. |
| `probe.detail` | Optional diagnostic detail; no raw tool output is included. |

## Error behaviour

Unknown tools and invalid arguments are rejected by the MCP layer. A backend
that disappears after startup does not crash the server; later action tools must
revalidate and return `backend_unavailable` rather than a false "no errors"
result.

The server never reports "no errors" when a backend is missing or a probe is
incomplete. At J3 no analysis is performed through MCP and no MCP tool launches
a target; the internal Memcheck pipeline is reachable only from Go tests until
J4 wires `analysis_start`.
