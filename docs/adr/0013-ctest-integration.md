# ADR 0013 — Trusted CTest integration

- **Status:** Accepted
- **Date:** 2026-10-18
- **Milestone:** J10

## Context

J10 adds project-level test discovery and execution. CTest test definitions are
arbitrary commands from a `CTestTestfile.cmake`; the server must not accept a
project directory, build command, regex or extra CTest flags from a client. A
test report must distinguish selected tests from the full project suite and
must not turn missing or unparsable evidence into a pass.

## Decision

### Configuration trust boundary

- Add `ctest.projects[]` with an id, an absolute `test_dir` inside
  `workspace_roots`, an exact `allowed_tests` allowlist, and optional named
  profiles that are subsets of that allowlist.
- Requiring a configured project is the trust declaration. The server never
  configures, builds or modifies it. CTest tests may execute arbitrary reviewed
  commands just as an allowlisted target binary may run.
- Validate all project paths and cardinalities at configuration time; re-check
  the directory and `CTestTestfile.cmake` immediately before discovery/run.

### Discovery

- Run only `ctest --show-only=json-v1` with a fixed minimal environment, bounded
  output and a short timeout. This does not execute tests.
- Parse CTest JSON as untrusted and return only configured exact names. Report
  configured-but-missing tests as warnings and `complete=false`.

### Execution and selection

- `ctest_run` accepts exact `tests[]` or a configured `profile`; both empty means
  all allowed tests. Reject duplicates, unlisted names, profile/test mixtures and
  client regexes.
- Build a quoted anchored exact-name `--tests-regex` internally. Fixed flags are
  `--output-on-failure --output-junit <private> --timeout <bounded>
  --no-tests=error --tests-regex <exact>`.
- The overall timeout is bounded by `limits.timeout_seconds`; the per-test
  timeout is bounded by `ctest.test_timeout_seconds`. Run one CTest process
  through the existing job manager, process group, quota and cancellation
  foundation. No build or shell is invoked.
- CTest may apply a test-specific `TIMEOUT` property; after parsing, a passed
  test whose JUnit duration exceeds the requested per-test limit is downgraded to
  `timeout` so the configured bound is not silently exceeded.

### Reports

- Parse CTest JUnit XML (both `<testsuite>` and `<testsuites>` roots) with bounds
  on bytes, test count, names and retained failure messages.
- Associate every selected test to exactly one JUnit result. Missing/duplicate/
  unselected results, JUnit read errors, process timeout/cancellation, signals
  or an unexplained nonzero CTest exit make the report incomplete.
- Persist `ctest.json` with `passed`/`failed`/`timeout`/`skipped`/`not_run`
  statuses, aggregate counts and artifact references. Preserve `ctest.xml` and
  bounded stdout/stderr artifacts.
- `ctest_status`, `ctest_report`, `ctest_artifact` and `ctest_cancel` use opaque
  run ids. No filesystem path is accepted or returned.

### Capability and tools

- Add `backend.ctest`, probed with `ctest --help` for the fixed JSON/JUnit/regex
  options. Never run a test during the probe.
- Publish `ctest_discover` and `ctest_run` only when the capability is available
  and at least one project has a usable `test_dir`. Keep evidence tools
  available for persisted runs.
- Expose `ctest_discover`, `ctest_run`, `ctest_status`, `ctest_cancel`,
  `ctest_report` and `ctest_artifact`; no free-form build or CTest command tool.

## Consequences

- Project test execution is explicit, allowlisted and bounded.
- Reports are precise about selected tests and incomplete evidence.
- A hostile project is outside the isolation model; system isolation is
  required. CTest's own `TIMEOUT` property can override the default per-test
  limit, which is documented rather than silently remapped.
