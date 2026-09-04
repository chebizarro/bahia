# Verification Report: signed-environment-units

Date: 2026-09-04

## Result

PASS. The signer-first environment unit workflow is acceptance-mapped and the full Go build, vet, and test gates pass.

## Acceptance evidence

- **AC1 — signed-only unit commands:** `TestEnvironmentUnitsListUsesSignedReadOnly`, `TestEnvironmentUnitCreateSetsNonDefaultKeyAtomically`, and `TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation` configure an HTTP server that fails on any request and verify the signed environment read is used.
- **AC2 — authorized signed details result:** `TestEncryptedRouteHandlers_GetEnvironmentDetailsAuthorizesAndReturnsResolvedDetails` proves read authorization and resolved details; `TestOperatorGetEnvironmentDetailsNostrRequestAndDecode` proves the signed `environment/get-details` request/result round trip.
- **AC3 — preservation:** `TestGenericAppSignerFirstEnvironmentUnitOnboardingFlow` and `TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation` prove the old unit survives by `Key` with all `git_source` fields intact when the complete set adds a new unit.
- **AC4 — concurrency:** `TestEncryptedRouteHandlers_UpdateEnvironmentRequiresRevisionForCompleteUnitSet` proves the revision is mandatory; `TestOperatorContextVMEnvironmentConflictIsTyped` maps JSON-RPC `-32009`; `TestEnvironmentUnitUpdateRetriesConflictWithFreshCompleteSet` proves signed reread/remerge success; `TestEnvironmentCompleteSetUpdateStopsAfterBoundedConflicts` proves the clear three-attempt exhaustion error.
- **AC5 — generic app flow:** `TestGenericAppSignerFirstEnvironmentUnitOnboardingFlow` adds a Bahia-managed Compose unit to one `edge-01-docker` environment, preserves the existing Docker unit, previews and creates a deploy intent for the new unit, marks the deployment observable for the handler harness, and creates a route-attach intent with a plan for the same unit.

## Commands

All passed:

```text
go test ./cmd/cli -run 'TestEnvironmentUnitsListUsesSignedReadOnly|TestEnvironmentUnitCreateSetsNonDefaultKeyAtomically|TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation' -count=1
go test ./internal/controlplane ./pkg/client -run 'TestEncryptedRouteHandlers_GetEnvironmentDetailsAuthorizesAndReturnsResolvedDetails|TestOperatorGetEnvironmentDetailsNostrRequestAndDecode' -count=1
go test ./internal/controlplane ./cmd/cli -run 'TestGenericAppSignerFirstEnvironmentUnitOnboardingFlow|TestEnvironmentUnitUpdateReadMergesCompleteSetBeforeSignedMutation' -count=1
go test ./cmd/cli ./pkg/client ./internal/controlplane -run 'TestEnvironmentUnitUpdateRetriesConflictWithFreshCompleteSet|TestEnvironmentCompleteSetUpdateStopsAfterBoundedConflicts|TestOperatorContextVMEnvironmentConflictIsTyped|TestEncryptedRouteHandlers_UpdateEnvironmentRequiresRevisionForCompleteUnitSet' -count=1
go build ./...
go vet ./...
go test ./...
```

## Deviations

None. Handler/registry-level integration was used as permitted; no live relay was required.
