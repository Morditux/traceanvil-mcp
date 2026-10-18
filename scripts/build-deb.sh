#!/usr/bin/env bash
#
# Build one architecture's Debian package from an already-built release binary.
# The package installs only the server binary and documentation; it has no
# daemon, setuid helper, maintainer script or sysctl change.
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
if [[ ! "$version" =~ ^[0-9][A-Za-z0-9.+:~_-]*$ ]]; then
  printf 'invalid Debian version: %s\n' "$version" >&2
  exit 1
fi
case "$arch" in
  amd64|arm64) ;;
  *) printf 'unsupported Debian architecture: %s\n' "$arch" >&2; exit 1 ;;
esac
if ! command -v dpkg-deb >/dev/null 2>&1; then
  printf 'dpkg-deb is required\n' >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
root="$work/root"
doc="$root/usr/share/doc/mcp-native-diagnostics"
install -d -m 0755 "$root/DEBIAN" "$root/usr/bin" "$doc"
install -m 0755 "$binary" "$root/usr/bin/mcp-native-diagnostics"
for file in README.md SECURITY.md LICENSE config.example.json; do
  if [[ -f "$file" ]]; then
    install -m 0644 "$file" "$doc/$file"
  fi
done
if [[ -f docs/operations.md ]]; then
  install -d -m 0755 "$doc/docs"
  install -m 0644 docs/operations.md "$doc/docs/operations.md"
fi
chmod -R u+rwX,go+rX,go-w "$root"
installed_size="$(du -sk "$root/usr" | awk '{print $1}')"
cat >"$root/DEBIAN/control" <<EOF
Package: mcp-native-diagnostics
Version: $version
Section: utils
Priority: optional
Architecture: $arch
Installed-Size: $installed_size
Maintainer: Morditux <morditux@users.noreply.github.com>
Homepage: https://github.com/Morditux/traceanvil-mcp
Description: Local MCP server for native diagnostics
 MCP stdio server for Valgrind, GDB, sanitizers, CTest and profiling.
 It launches only administrator-declared local targets; no daemon or
 privileged helper is installed.
EOF
output="$(cd -- "$(dirname -- "$binary")" && pwd)/mcp-native-diagnostics_${version}_${arch}.deb"
dpkg-deb --build --root-owner-group "$root" "$output" >/dev/null
printf '%s\n' "$output"
