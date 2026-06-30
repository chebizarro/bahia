# bahia-wqj5 Verification Report

## Summary

Implemented canonical immutable ContextVM subject locators for package and repository SBOM subjects.

Implemented behavior:

1. `domain.SBOMSubjectLocator` defines `package` and `repository` locator shapes.
2. `service.SBOMGenerateRequest` and `service.SBOMImportRequest` carry `subjectLocator`.
3. `controlplane.sbomImportParams` forwards `subjectLocator` to the orchestrator for `sbom/import`; `sbom/generate` unmarshals directly into the service request.
4. Package locator resolution requires package artifact coordinates plus SHA-256 and rejects missing/deleted artifacts or SHA mismatches.
5. Repository locator resolution accepts either validated git commit object IDs or immutable `sha256:<64-hex>` content digests and rejects mutable-name-only requests.
6. Documentation and HITL evidence record the canonical shapes.

## Verification commands

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/domain ./internal/service ./internal/controlplane
```

Result: PASS on 2026-06-30.

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## Evidence

- `internal/service/sbom_orchestrator_test.go`
  - `TestSBOMSubjectResolverPackageLocator`
  - `TestSBOMSubjectResolverPackageLocatorRejectsSHA256Mismatch`
  - `TestSBOMSubjectResolverRepositoryLocators`
  - `TestSBOMOrchestratorGenerateResolvesPackageSubjectLocator`
- `internal/domain/sbom.go`
  - `SBOMSubjectLocator`, `SBOMPackageArtifactLocator`, and `SBOMRepositoryLocator` request shapes.
- `docs/designs/sbom-real-support.md`, `docs/user-guide/features/packages.md`, and `docs/user-guide/nostr-integration.md`
  - canonical request field documentation.
- `pstf/features/bahia-wqj5/hitl_decisions.md`
  - immutable locator decision record.

## Remaining work

No remaining work identified for bahia-wqj5. The bead is intentionally left open for user verification.
