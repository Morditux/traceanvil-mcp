#!/usr/bin/env bash
# Build an RPM from a prebuilt static binary without running the target binary.
set -euo pipefail

if (($# != 3)); then
  printf 'usage: %s <binary> <version> <arch>\n' "$0" >&2
  exit 2
fi
binary="$1"
version="$2"
arch="$3"
if [[ ! -f "$binary" || ! -x "$binary" ]]; then
  printf 'binary is missing or not executable: %s\n' "$binary" >&2
  exit 1
fi
# Validate before passing the version to RPM's macro parser.
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9._-]+)?$ ]]; then
  printf 'invalid RPM version: %s\n' "$version" >&2
  exit 1
fi
case "$arch" in
  amd64) rpm_arch=x86_64 ;;
  arm64) rpm_arch=aarch64 ;;
  *) printf 'unsupported RPM architecture: %s\n' "$arch" >&2; exit 1 ;;
esac
if ! command -v rpmbuild >/dev/null 2>&1; then
  printf 'rpmbuild is required\n' >&2
  exit 1
fi
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="$(cd -- "$(dirname -- "$binary")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
payload="$work/payload"
doc="$payload/usr/share/doc/mcp-native-diagnostics"
install -d -m 0755 "$payload/usr/bin" "$doc/docs"
install -m 0755 "$binary" "$payload/usr/bin/mcp-native-diagnostics"
for file in README.md SECURITY.md LICENSE config.example.json; do
  install -m 0644 "$repo_root/$file" "$doc/$file"
done
install -m 0644 "$repo_root/docs/operations.md" "$doc/docs/operations.md"
# RPM forbids hyphens in Version. Tilde preserves prerelease ordering.
rpm_version="${version//-/~}"
rpm_version="${rpm_version//+/.}"
rpmbuild -bb --target "$rpm_arch" \
  --define "_topdir $work" \
  --define "_tmppath $work" \
  --define "_dbpath $work/rpmdb" \
  --define "_sourcedir $payload" \
  --define "package_version $rpm_version" \
  "$repo_root/packaging/mcp-native-diagnostics.spec"
output="$output_dir/mcp-native-diagnostics-${rpm_version}-1.${rpm_arch}.rpm"
install -m 0644 "$work/RPMS/$rpm_arch/mcp-native-diagnostics-${rpm_version}-1.${rpm_arch}.rpm" "$output"
printf '%s\n' "$output"
