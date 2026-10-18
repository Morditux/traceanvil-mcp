#!/usr/bin/env bash
#
# Bounded large-log parser benchmarks. Results are environment-specific and
# are recorded manually in docs/operations.md; this is not a pass/fail gate.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
go test ./internal/valgrind ./internal/profile ./internal/perf \
  -run='^$' -bench='BenchmarkParse' -benchtime="${BENCH_TIME:-1x}" -benchmem -count=1
