# Verification Report: bahia-i7p0 / bahia-f4cg worker cleanup

## Decision
The canonical path remains repository-backed worker reads, `WorkerPolicyService` selection, `PaymentService` payment estimate/history/query, and generic Nostr subscriber ingestion. The dormant `WorkerCatalogService`, raw Loom `WorkerDiscovery`, and coordinator `WithWorkerCatalog` hook were removed rather than wired into production.

## Evidence
- Pre-removal search found no active non-test production caller for `NewWorkerDiscovery`, `NewWorkerCatalogService`, `WithWorkerCatalog`, `PreparePayment`, or `GetWorkerStats` outside their definitions.
- `internal/app/app.go` composes the coordinator with `workflow.WithWorkerPolicy(workerPolicySvc)` and `workflow.WithRuntimeResolver(runtimeResolver)`, not `WithWorkerCatalog`.
- Post-removal search found zero `internal/` references to `WorkerCatalogService`, `WithWorkerCatalog`, `NewWorkerCatalogService`, `NewWorkerDiscovery`, or `WorkerDiscovery`.
- Post-removal search still finds `service.JobStatsTracker.RecordJobCompletion`; this is not the removed catalog service and is retained for worker policy reputation scoring.

## Tests
- `go test ./internal/workflow` passed.
- `go test ./internal/service` passed.
- `go test ./internal/adapters/loom` passed.
- `go test ./internal/app` passed.

## Review
Oracle review found no D-scoped runtime regression. The review recommended removing the now-dead `startTime` parameter from `pollForCompletion`; that cleanup was applied before final verification.

## Remaining tracked work
- `bahia-v5ij`: stale documentation reference to `WorkerDiscovery` remains in `docs/protocol-compatibility.md`; it is outside this work item's owned files.
- `bahia-2mri`: stale checked-in PSTF coverage artifact references deleted `worker_catalog.go` symbols.
