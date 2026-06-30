# bahia-ndmr Verification Report

## Summary

Implemented relay-backed SBOM availability-list merge for canonical kind `30004` replacement publication.

Before publishing the replacement list, the SBOM orchestrator now:

1. Builds a scoped filter for the subject availability list (`kind=30004`, exact `#d`, `#subject`, `#subject_type`, `#domain=sbom`, `#schema=bahia.sbom.available-list.v1`, trusted publisher author).
2. Subscribes through an EOSE-aware relay subscriber.
3. Validates incoming EVENT id/signature/timestamp/tags/content.
4. Processes relay EVENTs until EOSE.
5. Handles AUTH, RelayEOSE, CLOSED, and EOSE deterministically.
6. Merges valid relay entries with local projection entries before publishing.
7. Publishes the complete replacement only after relay OK acceptance.

## Verification command

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/service ./internal/adapters/sbom
```

Result:

```text
ok  	github.com/openagentsinc/bahia/internal/service
ok  	github.com/openagentsinc/bahia/internal/adapters/sbom
```

## Evidence

- `internal/service/sbom_orchestrator_test.go`
  - `TestSBOMOrchestratorMergesRelayAvailabilityBeforeReplacement`
  - `TestSBOMOrchestratorFailsClosedWhenAvailabilitySubscriptionClosesBeforeEOSE`
  - `TestSBOMOrchestratorRejectsInvalidRelayAvailabilityContent`
- `internal/adapters/sbom/index_test.go`
  - existing availability list and OK verification coverage.

## Remaining work

No remaining work identified for bahia-ndmr. The bead is intentionally left open for user verification.
