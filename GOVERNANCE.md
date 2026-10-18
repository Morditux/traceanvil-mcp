# Governance

## Roles

- **Maintainer:** approves milestones, security-sensitive changes, ADRs and
  releases; owns the repository and signing keys.
- **Contributor:** submits focused changes with tests, docs and a factual
  verification report.
- **Security reviewer:** reviews execution surfaces, capability exposure,
  process cleanup and release provenance.

## Change process

1. Read `PLAN.md` and `AGENTS.md`; do not treat planned work as implemented.
2. Prefer a small, typed increment over a broad unverified feature.
3. Add positive and negative tests, including timeout/cancellation and policy
   denial where applicable.
4. Run the full gate and record actual commands/results.
5. Security/API/trust changes require an ADR and maintainer approval.
6. Never commit `AGENTS.md`, `PLAN.md`, `INSTALL-UBUNTU.md`, secrets, local
   binaries or generated artifacts.

## Releases

Releases follow `docs/release.md`. Tagged artifacts must have checksums; signed
checksums are required when the project publishes a signing identity. SBOMs are
generated from embedded build information and must accompany the binary.
No release may claim a backend that is unavailable or experimental without
clearly labeling it.

## Security reports

Do not open a public issue for a suspected vulnerability. Follow
`SECURITY.md`. Maintainers acknowledge reports, reproduce them on a supported
platform, and coordinate a fix and disclosure.

## Compatibility

Supported baselines are recorded in `docs/compatibility.md`. A change that drops
a supported toolchain, OS or protocol capability requires an ADR and release
note. CI exercises the Ubuntu LTS matrix and the Go module baseline.
