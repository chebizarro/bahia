# bahia-1qe9.3 Verification Report

## Status
SBOM parser and attestation subject refactor slice implemented and targeted verification passed.

## Acceptance criteria covered
- `bahia-1qe9.3.1`: `ParseManifest` returns a subject-neutral `domain.SBOMManifest` plus `domain.SBOMManifestPackage` rows for SPDX and CycloneDX without breaking existing artifact-scoped `Parse(data, artifactID)` behavior.
- `bahia-1qe9.3.2`: `BuildAttestationInput` accepts `domain.SBOMSubject`, validates subject digest form, rejects mismatched subject digests, preserves legacy subject-name/digest inputs, and verifies payload/subject digests.

## Evidence
- `go test -mod=mod ./internal/adapters/sbom -run 'Test(Parse|Attestation|Verify|Serialize|MediaType|BuildSBOMReferenceEvent|IndexPublisher|BuildSBOMAvailabilityListEvent|BuildIndexEntry)'`
  - Result: PASS
  - Covers parser compatibility, subject-neutral manifest parsing, SBOMSubject attestation construction, digest validation, payload digest verification, and existing deterministic SBOM reference/list builder behavior affected by stricter digest validation.

## Full package verification note
- `go test -mod=mod ./internal/adapters/sbom`
  - Result: FAIL outside this epic's requested scope.
  - Failing tests: `TestSyftGeneratorGeneratesSPDXJSONFromRepositoryFixture`, `TestSyftGeneratorGeneratesCycloneDXJSONFromRepositoryFixture`.
  - Failure: Syft reports `sqlite driver is required for cataloging newer RPM databases, none registered: sql: unknown driver "sqlite"`.
  - Tracked under existing generator Bead `bahia-1qe9.4.2`.

## Out of scope
Generators, service orchestration, REST, and ContextVM were not implemented in this slice.
