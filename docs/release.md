# Release process

## Versioning

Versions use `vMAJOR.MINOR.PATCH` tags. Payload `schema_version` values are
independent from the binary version and change only for incompatible API
changes with an ADR.

## Build

From a clean checkout:

```sh
VERSION=v1.0.0 ./scripts/release.sh
./scripts/test-packages.sh
```

The script refuses a dirty tree unless `ALLOW_DIRTY=1`. It builds static Linux
`amd64` and `arm64` binaries with `-trimpath`, `-buildvcs=false` and embedded
version/commit metadata. The build date comes from `SOURCE_DATE_EPOCH` when set,
otherwise from the commit timestamp, so a repeated build from the same commit
and Go toolchain can be compared by hash. It then emits:

- `mcp-native-diagnostics_v1.0.0_linux_amd64`
- `mcp-native-diagnostics_v1.0.0_linux_arm64`
- one SPDX 2.3 `.spdx.json` per binary, generated from embedded Go build info
  with an architecture-specific document name and deterministic creation time;
- one `.deb` and one `.rpm` per architecture, built from the same binary;
- `SHA256SUMS`, sorted and covering binaries, packages and SBOMs.

The release host needs `dpkg-deb` and `rpmbuild` (Ubuntu packages `dpkg-dev`
and `rpm`). Package verification also needs `rpm2cpio` (provided by `rpm` on
Ubuntu) and `cpio`. Native distro packaging tools avoid a new Go dependency or
third-party packaging action. CI records the installed package versions.

Run the full gate before release:

```sh
GOCACHE="$PWD/.gocache" ./scripts/check.sh
go test -race ./...
```

## Signing

Checksums and artifacts are unsigned by default. To add a detached GPG
signature for `SHA256SUMS`:

```sh
RELEASE_GPG_KEY=release-key ./scripts/release.sh
```

`REQUIRE_SIGNING=1` makes a missing key fatal. The key must already exist in
the release operator's keyring; the script never generates or uploads keys.
Publish the fingerprint and SBOM alongside the artifacts.

Pushing a `vMAJOR.MINOR.PATCH` tag triggers the GitHub release workflow. It runs
the full gate, builds both formats for both architectures, and checks package
metadata, binary contents, documentation, permissions and absence of install
scripts without installing or executing the target binaries. Invalid RPM
metadata is tested as well.

The workflow creates a GitHub Release for the existing tag, with automatically
generated release notes and only the four **unsigned** `.deb`/`.rpm` files as
downloadable assets. Tags containing a prerelease suffix, such as
`v1.0.1-rc.1`, produce a prerelease. Download the packages from the repository's
**Releases** page; there is no workflow artifact ZIP or 30-day artifact expiry.
GitHub also provides its standard source archives automatically.

Publication uses the runner's GitHub CLI and the job's `GITHUB_TOKEN` with
`contents: write`; no personal token or additional action is needed. The tag
must already exist remotely (`--verify-tag`). An existing release causes the
publication step to fail rather than replacing its assets.

Raw binaries, SBOMs, checksums and temporary build files are not uploaded; the
local release command still generates the supply-chain files described above.
To trigger publication, first commit and push the workflow and packaging files,
then push a new version tag pointing to that commit:

```sh
git tag v1.0.1
git push origin v1.0.1
```

Signing is deliberately a separate, human-controlled step unless a later ADR
adds a protected signing environment.

## Verification

```sh
sha256sum -c SHA256SUMS
gpg --verify SHA256SUMS.asc SHA256SUMS
./mcp-native-diagnostics_v1.0.0_linux_amd64 version
jq . mcp-native-diagnostics_v1.0.0_linux_amd64.spdx.json >/dev/null
```

Check the SBOM dependency names/versions and the embedded build metadata. A
missing or modified artifact is not a release.

## Debian package

`scripts/release.sh` also invokes `scripts/build-deb.sh` for amd64 and arm64.
The package installs only `/usr/bin/mcp-native-diagnostics` plus documentation
under `/usr/share/doc/mcp-native-diagnostics`; it has no service, maintainer
script or setuid bit. `dpkg-deb` is required on the release host. Verify with:

```sh
dpkg-deb --info mcp-native-diagnostics_1.0.0_amd64.deb
dpkg-deb --contents mcp-native-diagnostics_1.0.0_amd64.deb
```

## RPM package

`scripts/build-rpm.sh` packages the same static binary for RPM `x86_64` (Go
`amd64`) and `aarch64` (Go `arm64`). It uses a temporary build tree, builds only
binary RPMs and cleans intermediates on exit. The payload and security
properties match the Debian package; no automatic backend dependencies, service,
install scripts, source RPM or debug package are added. The prebuilt binary is
neither executed nor stripped again.

RPM versions omit the tag's `v`, replace `-` with `~` for prereleases and `+`
with `.` for build metadata; the RPM release is `1`.

```sh
rpm -qpi mcp-native-diagnostics-1.0.0-1.x86_64.rpm
rpm -qpl mcp-native-diagnostics-1.0.0-1.x86_64.rpm
```

Development validation used Ubuntu 26.04.1, Go 1.27.0 and RPM
`6.0.1-1build1`, installed from Ubuntu with `--no-install-recommends` to build
and inspect RPMs. Existing `dpkg-dev` `1.23.7ubuntu1` and `cpio`
`2.15+dfsg-2.1ubuntu0.1` were reused. Both package formats and architectures
were built and extracted successfully; actionlint `v1.7.7` and ShellCheck
validated the workflow/scripts. Ubuntu 24.04 execution remains the CI check.
