# bahia-1qe9.5 Verification Report

## Status
Implemented the SBOM Nostr publisher slice for 30078 reference event building, 30004 availability-list building, and deterministic relay OK verification around the SBOM index publisher.

## Evidence
- `GOCACHE=/tmp/bahia-go-build-cache go test ./internal/adapters/sbom -run 'Test(BuildSBOM|IndexPublisher|BuildIndexEntry|ParseIndex|FilterFor)'`
  - Result: PASS
  - Covers 30078 builder shape/validation, 30004 builder shape/dedupe, adapter-compatible OK result conversion, and accepted/rejected/auth-required/closed publish outcomes through deterministic fake publishers.
- `GOCACHE=/tmp/bahia-go-build-cache go test ./internal/domain`
  - Result: PASS
  - Covers domain model compatibility after adding availability-list reference metadata fields.

## Notes
- Full `go test ./internal/adapters/sbom ./internal/domain` was not used as the final gate because out-of-scope Syft generator tests fail in the current workspace with `sqlite driver is required for cataloging newer RPM databases, none registered`.
- The migration manifest gap remains tracked as `bahia-bl58`; this slice did not touch the migration manifest path.
