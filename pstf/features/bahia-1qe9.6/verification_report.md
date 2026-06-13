# bahia-1qe9.6 Verification Report

## Status
Implemented SBOM orchestration service and ContextVM integration for unambiguous artifact/deployment subjects and explicit package/repository digests.

## Evidence
- `GOCACHE=/tmp/bahia-go-build-cache go test ./internal/service ./internal/controlplane`
  - Result: PASS
  - Covers service orchestration success, idempotency, Blossom upload hash verification path, 30078/30004 publication, 30315 status, 4903 audit, relay OK rejection, AUTH-like rejection, CLOSED-like failure, and subject resolution ambiguity.
- `GOCACHE=/tmp/bahia-go-build-cache go test ./internal/adapters/nostr -run TestProjectorSystemDiscoveryAdvertisesDNSOnlyWhenSourceConfigured`
  - Result: PASS
  - Confirms ContextVM discovery advertises `sbom/generate` and `sbom/import`.
- `GOCACHE=/tmp/bahia-go-build-cache go test -mod=mod ./internal/app`
  - Result: PASS
  - Confirms application wiring compiles with SBOM generator/storage/repository/publisher dependencies and ContextVM handler registration.

## Implemented scope
- `bahia-1qe9.6.1`: Added `service.SBOMOrchestrator` for generate/import orchestration over existing generator, parser, Blossom storage, attestation, Nostr builder, and repository projection slices.
- `bahia-1qe9.6.2`: Publishes OK-verified `30315` SBOM status and `4903` audit events.
- `bahia-1qe9.6.3`: Registers ContextVM `sbom/generate` and `sbom/import` handlers on the existing encrypted ContextVM transport.
- `bahia-1qe9.6.4`: Resolves artifact subjects from artifact models and deployment subjects from deployment intent desired hashes; package and repository subjects require explicit immutable digests when coordinates/commit resolution is ambiguous.

## Notes
- No REST generation endpoint was added.
- Legacy `30079` is not published by the orchestration path.
- Repository and package digest auto-resolution remains intentionally blocked unless callers provide explicit immutable digests.
