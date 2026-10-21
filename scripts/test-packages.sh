#!/usr/bin/env bash
# Verify both package formats against the release binaries, without installing.
set -euo pipefail
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
shopt -s nullglob
packages=(dist/*.deb dist/*.rpm)
if ((${#packages[@]} != 4)); then
  printf 'expected exactly two Debian and two RPM packages\n' >&2
  exit 1
fi
for arch in amd64 arm64; do
  case "$arch" in
    amd64) rpm_arch=x86_64 ;;
    arm64) rpm_arch=aarch64 ;;
  esac
  deb=(dist/*_"$arch".deb)
  rpm=(dist/*."$rpm_arch".rpm)
  binary=(dist/*_linux_"$arch")
  [[ ${#deb[@]} == 1 && ${#rpm[@]} == 1 && ${#binary[@]} == 1 ]]
  version="${binary[0]#dist/mcp-native-diagnostics_v}"
  version="${version%_linux_"$arch"}"
  rpm_version="${version//-/~}"
  rpm_version="${rpm_version//+/.}"
  [[ "$(dpkg-deb -f "${deb[0]}" Package)" == mcp-native-diagnostics ]]
  [[ "$(dpkg-deb -f "${deb[0]}" Version)" == "$version" ]]
  [[ "$(dpkg-deb -f "${deb[0]}" Architecture)" == "$arch" ]]
  [[ "$(rpm --dbpath "$work/rpmdb" -qp --qf '%{NAME} %{VERSION}-%{RELEASE} %{ARCH}' "${rpm[0]}")" == "mcp-native-diagnostics $rpm_version-1 $rpm_arch" ]]
  [[ -z "$(rpm --dbpath "$work/rpmdb" -qp --scripts "${rpm[0]}")" ]]
  dpkg-deb -x "${deb[0]}" "$work/deb-$arch"
  dpkg-deb -e "${deb[0]}" "$work/control-$arch"
  for script in preinst postinst prerm postrm; do
    [[ ! -e "$work/control-$arch/$script" ]]
  done
  mkdir "$work/rpm-$arch"
  rpm2cpio "${rpm[0]}" | cpio --quiet -idm --no-absolute-filenames -D "$work/rpm-$arch"
  for format in deb rpm; do
    root="$work/$format-$arch"
    cmp "${binary[0]}" "$root/usr/bin/mcp-native-diagnostics"
    [[ "$(stat -c %a "$root/usr/bin/mcp-native-diagnostics")" == 755 ]]
    for file in README.md SECURITY.md LICENSE config.example.json docs/operations.md; do
      cmp "$file" "$root/usr/share/doc/mcp-native-diagnostics/$file"
    done
  done
done

# Invalid metadata must be rejected before reaching RPM's macro parser.
expect_rejected() {
  if "$repo_root/scripts/build-rpm.sh" "$@" >"$work/rejected.log" 2>&1; then
    printf 'unexpectedly accepted invalid RPM arguments\n' >&2
    exit 1
  fi
}
expect_rejected
expect_rejected /nonexistent 1.0.0 amd64
expect_rejected "${binary[0]}" '1.0.0%{lua:print(1)}' amd64
expect_rejected "${binary[0]}" 1.0.0 riscv64
printf 'Both architectures and package formats verified; invalid RPM inputs rejected.\n'
