# FP_BAHIA_ARCANA_04_ARTIFACT_FLOW verification

## Implemented

- Trusted HiveCI result correlation with the originally queued Bahia build.
- Embedded OCI-layout or external registry tag resolution with exact tag-resolved manifest-digest equality.
- Canonical verified artifact registration with database uniqueness convergence and projection re-publication on idempotent retries.
- Persisted verification time/source, tag-resolved digest, policy ID/state, trusted CI publisher, signature/SBOM/provenance, scan, and referrer-discovery state.
- Signed build-result recovery action that accepts only a Bahia build ID.
- Default-deny advanced manual registration policy, successful-build/repository binding, and protected provenance namespaces.
- Builds UI immutable selection and verification-provenance display; legacy unverified artifact projections are not deployment candidates.

## Verification

- PASS: `go test ./internal/pipeline ./internal/controlplane ./internal/service ./internal/repository ./internal/app ./internal/adapters/hiveci`
- PASS: `go test ./internal/api/router`
- PASS: affected SoulFactory Bahia integration tests.
- PASS: `go build ./...`
- PASS: `cd web && npm run test:unit` (78 files, 612 tests).
- PASS: `cd web && npm run lint` (0 errors, 0 warnings).
- PASS: `cd web && npm run build`.
- FULL-SUITE EXCEPTION: `go test ./...` reaches the unchanged, pre-existing `internal/soulfactory` failure `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods`, tracked by open bead `bahia-csxyx`; all artifact-flow and affected integration packages pass.
