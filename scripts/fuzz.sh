#!/usr/bin/env bash
#
# Bounded fuzz smoke campaign. Real releases should run with a longer
# FUZZ_TIME and a corpus directory; this script never installs anything.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

duration="${FUZZ_TIME:-10s}"
targets=(
  "./internal/dap FuzzReadMessage"
  "./internal/valgrind FuzzParse"
  "./internal/profile FuzzParseCallgrind"
  "./internal/profile FuzzParseMassif"
  "./internal/perf FuzzParseStat"
  "./internal/config FuzzParseConfig"
  "./internal/sanitizer FuzzParseSanitizer"
)
for item in "${targets[@]}"; do
  read -r package function <<<"$item"
  printf '\n==> %s %s (%s)\n' "$package" "$function" "$duration"
  go test "$package" -run='^$' -fuzz="^${function}$" -fuzztime="$duration"
done
