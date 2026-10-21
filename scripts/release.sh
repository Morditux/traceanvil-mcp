#!/usr/bin/env bash
#
# Build Linux release binaries, Debian/RPM packages, SBOMs and checksums.
# Module downloads may be needed on a cold module cache. Signing is optional and
# requires an explicitly supplied GPG key. The script never installs packages.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

version="${VERSION:-}"
if [[ -z "$version" ]]; then
  printf 'VERSION is required (for example VERSION=v1.0.0)\n' >&2
  exit 1
fi
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9._-]+)?$ ]]; then
  printf 'VERSION must look like v1.0.0 (got %q)\n' "$version" >&2
  exit 1
fi
if [[ "${ALLOW_DIRTY:-0}" != "1" ]] && [[ -n "$(git status --porcelain)" ]]; then
  printf 'working tree is dirty; commit or set ALLOW_DIRTY=1 for a local build\n' >&2
  exit 1
fi
if ! command -v sha256sum >/dev/null 2>&1; then
  printf 'sha256sum is required\n' >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  printf 'go is required\n' >&2
  exit 1
fi
for tool in dpkg-deb rpmbuild; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf '%s is required\n' "$tool" >&2
    exit 1
  fi
done

commit="$(git rev-parse --short=12 HEAD)"
if [[ -n "${SOURCE_DATE_EPOCH:-}" ]]; then
  build_date="$(date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ)"
else
  build_date="$(git show -s --format=%cI HEAD)"
fi
go mod verify
dist="$repo_root/dist"
case "$dist" in
  "$repo_root"/*) ;;
  *) printf 'refusing to use dist outside the repository\n' >&2; exit 1 ;;
esac
rm -rf -- "$dist"
mkdir -p "$dist"

host_arch="$(go env GOARCH)"
sbom_tool="$dist/.sbom-tool"
trap 'rm -f "$sbom_tool"' EXIT
CGO_ENABLED=0 GOOS=linux GOARCH="$host_arch" go build -mod=readonly -buildvcs=false -trimpath -o "$sbom_tool" ./cmd/mcp-native-diagnostics
for arch in amd64 arm64; do
  name="mcp-native-diagnostics_${version}_linux_${arch}"
  printf '\n==> building %s\n' "$name"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -mod=readonly -buildvcs=false -trimpath \
      -ldflags "-s -w -X github.com/Morditux/traceanvil-mcp/internal/version.version=${version} -X github.com/Morditux/traceanvil-mcp/internal/version.commit=${commit} -X github.com/Morditux/traceanvil-mcp/internal/version.date=${build_date}" \
      -o "$dist/$name" ./cmd/mcp-native-diagnostics
  chmod 0755 "$dist/$name"
  "$repo_root/scripts/build-deb.sh" "$dist/$name" "${version#v}" "$arch"
  "$repo_root/scripts/build-rpm.sh" "$dist/$name" "${version#v}" "$arch"
  "$sbom_tool" sbom --binary "$dist/$name" --version "$version" --name "mcp-native-diagnostics-linux-${arch}" --created "$build_date" --output "$dist/${name}.spdx.json"
done
(
  cd "$dist"
  sha256sum ./*.spdx.json ./*.deb ./*.rpm ./*_linux_amd64 ./*_linux_arm64 | LC_ALL=C sort > SHA256SUMS
)

if [[ -n "${RELEASE_GPG_KEY:-}" ]]; then
  if ! command -v gpg >/dev/null 2>&1; then
    printf 'RELEASE_GPG_KEY is set but gpg is unavailable\n' >&2
    exit 1
  fi
  printf '\n==> signing SHA256SUMS with %s\n' "$RELEASE_GPG_KEY"
  gpg --batch --yes --local-user "$RELEASE_GPG_KEY" --armor \
    --detach-sign --output "$dist/SHA256SUMS.asc" "$dist/SHA256SUMS"
elif [[ "${REQUIRE_SIGNING:-0}" == "1" ]]; then
  printf 'REQUIRE_SIGNING=1 but RELEASE_GPG_KEY is not set\n' >&2
  exit 1
fi

printf '\nRelease artifacts in %s\n' "$dist"
ls -l "$dist"
