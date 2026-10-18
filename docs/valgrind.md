# Valgrind Memcheck pipeline (J3)

J3 adds an internal, end-to-end Memcheck pipeline: an allowlisted target is run
under Valgrind in XML mode, the raw XML is preserved as evidence, and a
canonical report is written next to it. **No MCP tool exposes this pipeline
yet.** `analysis_start`, `analysis_status`, `analysis_report` and
`analysis_artifact` arrive with J4. The public MCP surface at J3 is still
`system_capabilities`.

## Execution

The service resolves the target through the J2 policy immediately before launch:

1. the target id must be declared in `allowed_targets`;
2. the program and working directory must resolve inside `workspace_roots`;
3. the program must be an absolute regular executable;
4. requested arguments must be a multiset subset of `allowed_args`.

Valgrind itself is discovered by the capability registry from an absolute
`PATH` entry and revalidated on every run (regular executable file). The server
never builds a shell command: arguments are a Go slice.

```text
valgrind
  --tool=memcheck
  --xml=yes
  --xml-file=<run-dir>/memcheck.xml
  --error-exitcode=42
  --leak-check=<none|summary|full>
  --show-leak-kinds=<allowlisted selector>
  --track-origins=yes            (when enabled)
  --num-callers=<1..50>
  --suppressions=<configured file>   (when a profile is selected)
  --
  <target> <target args>
```

The `--` separator is mandatory: a target argument that looks like a Valgrind
option stays a target argument. The target receives the J2 minimal environment
(`PATH`, `LANG`, `LC_ALL`, `TMPDIR`), never the server environment, and stdin is
disconnected.

| Option | Default | Accepted values |
|---|---|---|
| `leak_check` | `full` | `none`, `summary`, `full` |
| `show_leak_kinds` | `all` | `all`, `definite`, `possible`, `reachable`, `definite,possible` |
| `track_origins` | `true` | boolean |
| `num_callers` | `12` | `1`–`50` |
| `suppressions_profile` | none | key of `valgrind.suppressions` |
| `trace_children` | `false` | **only `false` in J3**; `true` is rejected |

`trace_children=true` is deliberately unsupported: Valgrind then writes one XML
document per process and J3 does not implement multi-process collection or
attribution.

## Evidence and quotas

Each run gets a private `0700` directory under `report_dir`:

```text
run_<32 hex>/
├── result.json     job manifest written by the J2 manager
├── analysis.json   canonical diagnostic report (J3)
├── memcheck.xml    raw Valgrind XML (J3)
├── stdout.log      bounded target/Valgrind stdout
└── stderr.log      bounded target/Valgrind stderr
```

- stdout/stderr use the shared runner byte budget; overshoot is marked
  truncated.
- Valgrind writes `memcheck.xml` directly. A watchdog polls its size while the
  process runs and cancels the process group above `MaxRawXMLBytes` (default
  8 MiB). After the run the artifact store adopts the file: it must be a
  regular non-symlink file, is truncated to the remaining per-run quota,
  chmodded `0600` and charged against that quota. A 128 KiB reserve is kept for
  `analysis.json`, so adopting the raw XML cannot make the normalized report
  unpersistable.
- `analysis.json` is written atomically. If the artifact quota cannot hold the
  full report, a compact report is stored with `result=incomplete`,
  `analysis_complete=false`, `summary.truncated=true` and an explicit warning;
  the raw XML remains available for later parsing.
- This is a live bound, not a kernel quota. A target can still create untracked
  files in its `TMPDIR` (the run directory) and the watchdog is polled. A
  cgroup/container sandbox and global retention are future work (J14).

## XML parsing and limits

`internal/valgrind.Parse` decodes token by token with `encoding/xml`; it never
loads the whole document or recursively skips untrusted nesting. Unknown
elements and attributes are ignored so newer Valgrind releases remain readable.

| Limit | Default |
|---|---|
| XML bytes | 8 MiB (also the raw artifact bound) |
| diagnostics | 10 000 |
| frames per stack | 128 |
| frames per document | 65 536 |
| context stacks per diagnostic | 8 |
| string bytes per field | 4 096 |
| suppression entries | 512 |
| element depth | 64 |

A clean EOF is accepted only after `</valgrindoutput>`. Missing end tag, syntax
error, depth overflow, decoder error or any limit breach yields a partial
result and `analysis_complete=false`. The diagnostics parsed before the failure
are kept; a truncated document is never reported as "no errors".

The parser understands the Memcheck XML structure (`<protocolversion>`,
`<preamble>`, `<error>`, `<xwhat>`, `<stack>`, `<origin>`, `<errorcounts>`,
`<suppcounts>`, `<error_summary>`, `<heap_summary>`). `<errorcounts>` attaches
occurrence counts to diagnostics; repeated identical errors therefore appear as
one diagnostic with `occurrences > 1`.

## Canonical report

`analysis.json` uses `schema_version = 1`:

```json
{
  "schema_version": 1,
  "run_id": "run_0123456789abcdef0123456789abcdef",
  "tool": {"name": "valgrind", "path": "/usr/bin/valgrind", "version": "valgrind-3.26.0"},
  "result": "errors_found",
  "analysis_complete": true,
  "program": {
    "target_id": "fixture",
    "path": "/ws/build/fixture",
    "sha256": "sha256:…",
    "args": [],
    "exit_code": 42,
    "duration_ms": 210
  },
  "summary": {
    "total": 1,
    "diagnostic_records": 1,
    "by_kind": {"InvalidRead": 1},
    "by_severity": {"error": 1},
    "suppressed": 0,
    "truncated": false
  },
  "diagnostics": [
    {
      "id": "diag_0123456789abcdef0123456789abcdef_0",
      "fingerprint": "sha256:…",
      "origin": "valgrind-memcheck",
      "kind": "InvalidRead",
      "severity": "error",
      "message": "Invalid read of size 4",
      "location": {"function": "main", "path": "invalid_read.c", "line": 8},
      "stack": [{"function": "main", "path": "invalid_read.c", "line": 8}],
      "occurrences": 1,
      "evidence": {
        "artifact": "memcheck.xml",
        "record_id": "0x0",
        "record_index": 0,
        "raw_kind": "InvalidRead"
      },
      "confidence": "tool_reported"
    }
  ],
  "artifacts": [{"name": "memcheck.xml", "kind": "raw_xml", "bytes": 2637, "sha256": "sha256:…"}],
  "warnings": []
}
```

### Results

| `result` | Meaning |
|---|---|
| `errors_found` | complete XML with `error_summary.errors > 0` |
| `no_errors_observed` | complete XML with zero unsuppressed errors |
| `incomplete` | partial XML, timeout, cancellation, quota/limit breach or inconsistent summary |
| `tool_error` | Valgrind produced no usable evidence or the job failed |
| `policy_denied` | reserved for the J4 request layer; J3 returns a typed policy error before starting |

`analysis_complete=false` whenever evidence is partial. `total=0` with
`analysis_complete=false` never means "no bug". `total` counts tool-reported
occurrences while `diagnostic_records` counts distinct records.

### Severity policy

Severity is the server's documented policy, not a Valgrind truth:

| Kind | Severity |
|---|---|
| `InvalidRead`, `InvalidWrite`, `InvalidFree`, `MismatchedFree`, `Overlap`, `InvalidJump`, `UninitCondition`, `UninitValue`, `SyscallParam`, `ClientCheck`, `Leak_DefinitelyLost`, `Leak_IndirectlyLost` | `error` |
| `Leak_PossiblyLost` | `warning` |
| `Leak_StillReachable` | `info` |
| unknown kinds | `warning` |

Still-reachable blocks are not automatically leaks.

### Stacks and fingerprints

- Stack frames keep function, object, source path and line. Paths under a
  workspace root are normalized to slash-separated relative paths; other paths
  stay absolute.
- Valgrind runtime frames (`vgpreload_*`, `/usr/lib*/valgrind`,
  `/usr/src/valgrind*`) are flagged `runtime` and excluded from the primary
  location and from fingerprints.
- A missing file or line stays missing. The server never invents a location.
- Fingerprint v1 is SHA-256 over `origin + kind + selected non-runtime frames`
  (function, normalized path, line). Addresses, PIDs, TIDs, timestamps and run
  ids are excluded. Inlining, missing symbols and line movement can still
  change it; it is a versioned heuristic, not proof of identity.

### Exit codes

`--error-exitcode=42` is a marker, not proof. XML is authoritative:

- a target that legitimately exits `42` with a complete, error-free XML is
  `no_errors_observed` with exit code `42`;
- a program that exits `7` with no Memcheck errors reports exit code `7`;
- a program that crashes with `SIGSEGV` is reported with `signal=11` (the signal
  observed on the Valgrind process, which mirrors the instrumented program); if
  Valgrind finished the XML, the analysis remains complete;
- missing, truncated or contradictory evidence yields `incomplete` or
  `tool_error`, never `no_errors_observed`.

## Suppressions

```json
{
  "valgrind": {
    "suppressions": {
      "project-known": "/home/user/work/native-project/suppressions/known.supp"
    }
  }
}
```

- At most 32 profiles. Ids follow `^[a-z0-9][a-z0-9._-]{0,63}$`.
- Files must be absolute, inside `workspace_roots`, at most 1 MiB, and are
  resolved with `EvalSymlinks` and revalidated inside the roots at invocation.
- Suppressed errors are not diagnostics and are never called "resolved". The
  report exposes their tool-reported counts and adds a warning that suppression
  is not evidence of absence.

## Capability probe

At startup the Valgrind probe runs `--version` and then
`--tool=memcheck --help`, with a short timeout and a minimal environment. It
requires `memcheck`, `--xml=yes`, `--error-exitcode` and `--suppressions` in the
help output. It never runs a user program and never uses sudo. `available` means
the binary and Memcheck/XML options were verified. Every actual run revalidates
the binary: a missing binary at `Start` is a typed `BackendError`, and a binary
that disappears before `exec` produces a `tool_error` report, never "no errors".
J4 maps these outcomes to its named API errors.

## Tests

```sh
go test ./internal/valgrind ./internal/diagnostic
go test ./tests/integration -run TestMemcheck
```

The integration suite compiles the C fixtures under
`testdata/fixtures/c` with `-g -O0 -fno-omit-frame-pointer` and runs real
Valgrind. It skips, with a documented reason, when `valgrind` or `cc` is not
installed (for example in the standard CI runner). Parser tests always run and
include a fuzz target.
