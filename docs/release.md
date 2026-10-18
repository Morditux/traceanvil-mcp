# Release process

## Versioning

Versions use `vMAJOR.MINOR.PATCH` tags. Payload `schema_version` values are
independent from the binary version and change only for incompatible API
changes with an ADR.

## Build

From a clean checkout:

```sh
VERSION=v1.0.0 ./scripts/release.sh
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
- one `.deb` per architecture, built from the same binary;
- `SHA256SUMS`, sorted and covering binaries and SBOMs.

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

The GitHub release workflow runs the full gate, builds and uploads **unsigned**
artifacts for a tag. It does not create a GitHub Release or a signed claim.
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
