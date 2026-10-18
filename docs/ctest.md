# CTest integration (J10)

CTest integration is opt-in. The server never configures, builds or modifies a
project. It only discovers and runs tests inside a **trusted project directory**
declared in `ctest.projects`.

```json
{
  "backends": {"ctest": true},
  "ctest": {
    "test_timeout_seconds": 30,
    "projects": [{
      "id": "solver-tests",
      "test_dir": "/workspace/native-project/build",
      "allowed_tests": ["Core.basic", "Core.io"],
      "profiles": {"quick": ["Core.basic"]}
    }]
  }
}
```

`test_dir` must contain `CTestTestfile.cmake`. Declaring the project is the trust
boundary: CTest tests may execute arbitrary commands from that reviewed
`CTestTestfile.cmake`, just as a declared target may execute its binary. The
server adds no isolation beyond the process runner, policy limits and artifact
quota.

## Discovery

`ctest_discover` takes only `project_id` and runs:

```text
ctest --show-only=json-v1
```

with a fixed minimal environment, a short timeout and bounded output. It never
runs a test. The response contains only names from `allowed_tests`; configured
tests missing from CTest produce warnings and make `complete=false`. Raw test
commands and unlisted test names are not returned.

## Selection and limits

`ctest_run` accepts:

- `project_id`;
- `tests[]`: exact names from `allowed_tests`, or omitted to select all;
- `profile`: a configured subset, mutually exclusive with `tests`;
- `timeout_ms`: optional overall timeout, only shorter than
  `limits.timeout_seconds`;
- `test_timeout_ms`: optional per-test limit, only shorter than
  `ctest.test_timeout_seconds`.

The server builds a quoted, anchored exact-name `--tests-regex`; client regexes
and extra CTest flags are impossible. It also passes `-FA '.*'` so CTest cannot
automatically add fixture setup/cleanup tests outside the allowlist. Any
unselected testcase in JUnit makes the report `error`/incomplete.
 Tests run sequentially (`ctest` default)
with `--output-on-failure`, `--output-junit`, `--timeout` and
`--no-tests=error`. The raw JUnit report is bounded and parsed as untrusted XML.

CTest may apply a test's own `TIMEOUT` property instead of the default; the
server reports the resulting CTest status and its raw evidence. It also
re-checks each passed test's JUnit duration against the requested per-test
limit and downgrades an over-limit test to `timeout`. It does not silently claim
that a missing test passed.

## Result semantics

Each selected test gets one of `passed`, `failed`, `timeout`, `skipped` or
`not_run`. The aggregate result is:

- `failed` when any selected test failed or timed out;
- `incomplete` when JUnit evidence is missing/unparsable, the overall process
  timed out/was canceled, an unexpected nonzero CTest exit has no parsed failing
  test, or a selected test has no result;
- `passed` only when all selected tests are accounted for, no failure/timeout
  was observed and no selected test was skipped or not run. Skipped tests are
  counted separately and make the aggregate `incomplete`, not a pass.

`complete` describes the evidence and invocation, not a claim that unselected
or undiscovered tests would pass. A clean run is not proof that the project's
test suite is exhaustive.

## Reports and artifacts

`ctest_status` exposes lifecycle, aggregate result and counts.
`ctest_report` returns a bounded per-test page with `has_more` when clamped;
the hard page bound equals the configured allowed-test bound (512).
`ctest_artifact` exposes allowlisted excerpts:

| Type | File |
|---|---|
| `junit` | `ctest.xml` |
| `stdout` | `stdout.log` |
| `stderr` | `stderr.log` |
| `report` | `ctest.json` |

All artifacts are opened through the artifact store with `O_NOFOLLOW`; no
filesystem path is accepted or returned. The raw JUnit report is kept even when
the normalized report is incomplete.

## Capability and security

`backend.ctest` is probed with `ctest --help` for the required fixed options; no
test is run. `ctest_discover` and `ctest_run` are hidden unless the capability
is available and at least one project has a usable `test_dir`. The evidence
tools stay published for persisted runs.

The server never executes a build, never accepts a project path from the client,
never runs a shell or a client regex, and never exposes arbitrary CTest flags.
A hostile project is outside this threat model; use system isolation for
untrusted projects.
