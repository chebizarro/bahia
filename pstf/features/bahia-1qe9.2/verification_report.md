# bahia-1qe9.2 Verification Report

## Status
SBOM domain, database, and repository projection slice implemented and targeted verification passed.

## Evidence
- `go test ./internal/domain ./internal/repository`
  - Result: PASS
  - Covers subject-neutral domain additions and PostgreSQL repository insert/dedupe/list/publish-state/package-search tests.
- `go test ./internal/...`
  - Result: FAIL due to pre-existing/out-of-scope `internal/nostrmigration` coverage gap for `internal/kinds.SBOMAvailabilityList=30004`.
  - Follow-up tracked in Beads as `bahia-bl58`.

## Repository scope verified
- `internal/domain/sbom.go` preserves `ArtifactSBOM` and `SBOMPackage` while adding subject-neutral manifest projections.
- `internal/db/migrations/000043_sbom_manifests.up.sql` creates `sbom_manifests` and `sbom_manifest_packages` with dedupe indexes and publish-state fields.
- `internal/db/migrations/000043_sbom_manifests.down.sql` rolls back both generalized projection tables.
- `internal/repository/pg_sbom.go` stores generalized manifests/packages and writes artifact compatibility projections for artifact subjects.

## Out of scope
- Syft/cdxgen generation, ContextVM handlers, Nostr publishing builders, and REST rewiring were intentionally not implemented in this slice.
