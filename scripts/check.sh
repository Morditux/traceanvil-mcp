#!/usr/bin/env bash
#
# Repository quality gate, shared by contributors and CI.
# Runs only local Go tooling; it never installs packages and never needs sudo.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

step() {
	printf '\n==> %s\n' "$1"
}

step "gofmt"
unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
	printf 'The following files are not gofmt-formatted:\n%s\n' "$unformatted" >&2
	exit 1
fi

step "go vet"
go vet ./...

step "go test"
go test ./...

step "go test -race"
go test -race ./...

step "go build"
go build ./...

printf '\nAll checks passed.\n'
