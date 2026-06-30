# bahia-ilio Verification Report

## Summary

Implemented Option A asynchronous ContextVM acknowledgment semantics for SBOM generation/import.

Behavior now implemented:

1. `sbom/generate` and `sbom/import` ContextVM handlers decode/validate request payloads and enqueue orchestration instead of calling `SBOMOrchestrator.Generate` / `Import` synchronously.
2. Accepted responses return `accepted=true`, `status=accepted`, `run_id`, `status_d_tag`, `idempotencyKey`, and `observable_kinds`.
3. `service.SBOMAsyncRunner` is channel-driven and managed by the app `BackgroundManager` at the same tier as encrypted ContextVM transport; it has no polling loops, sleeps, or timeout-based completion logic.
4. The underlying orchestrator remains the idempotency and canonical publication authority, preserving OK verification, AUTH handling, CLOSED-before-EOSE failure, ndmr availability-list merge, and wqj5 subject locator resolution.
5. Docs now state that the ContextVM response is only an accepted coordinate and clients must observe `30315`/`4903` progress and terminal `30078`/`30004` truth.

## Verification commands

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/service ./internal/controlplane ./internal/app
```

Result: PASS on 2026-06-30.

```bash
GOCACHE=/tmp/bahia-go-cache go test ./internal/...
```

Result: PASS on 2026-06-30.

## Evidence

- `internal/controlplane/sbom_handlers_test.go`
  - `TestSBOMContextVMGenerateReturnsAcceptedAckAndEnqueues`
  - `TestSBOMContextVMImportReturnsAcceptedAckAndEnqueues`
- `internal/service/sbom_async_runner_test.go`
  - `TestSBOMAsyncRunnerAcceptedAckThenWorkerPublishesAfterAUTHAndOK`
  - `TestSBOMAsyncRunnerImportAckThenWorkerPublishesAfterOK`
  - `TestSBOMAsyncRunnerSurfacesClosedBeforeEOSEWithoutAvailabilityPublication`
  - `TestSBOMAsyncRunnerSurfacesOKRejectionWithoutProjection`
  - `TestSBOMAsyncRunnerRejectsFullQueueWithoutPolling`
- `docs/control-planes.md`, `docs/designs/sbom-real-support.md`, and `docs/user-guide/nostr-integration.md` document the asynchronous acknowledgment contract.

## Review follow-up

Oracle review flagged two issues before final handoff: the SBOM runner was originally registered at a lower lifecycle tier than encrypted ContextVM transport, and the real async import branch lacked service-level runner coverage. Fixes applied:

- `internal/app/app.go` now registers `sbom-async-runner` at Tier2 with encrypted ContextVM transport.
- `service.SBOMAsyncRunner` checks orchestrator readiness before returning accepted acknowledgments.
- `TestSBOMAsyncRunnerImportAckThenWorkerPublishesAfterOK` covers the real `EnqueueImport` branch.

## Remaining work

No remaining work identified for bahia-ilio. The bead is intentionally left open for user verification.
