# Verification Report — Bahia Worker Management Bucket D

## Scope

Service-layer scheduling enforcement for Beads `bahia-9db7` and `bahia-7omd`.

## Evidence

- Updated `WorkerPolicyService.SelectWorker` so only workers with active scheduling state are eligible for new generic placements.
- Updated `WorkerPolicyService.RankWorkers` to retain ineligible online workers with `Eligible=false` and scheduling rejection reasons for preview/debug consumers.
- Updated `MLPlacementService.SelectCandidate` so new inference placements use the same active-only scheduling semantics.
- Added `MLPlacementService.PreviewCandidates` so candidate preview/read-model callers can surface non-active worker rejection reasons.
- Reused one service-layer helper for generic and ML placement rejection messages, keeping cordon/drain/maintenance/disabled semantics consistent.
- Audited `RankWorkers` call sites with repository search; only tests, Beads metadata, and the worker-management plan referenced it, so the expanded preview-oriented `Eligible=false` contract has no production callers to update in this slice.

## Tests

- `go test ./internal/service` — PASS
- `go test ./...` — PASS
- Oracle review: selected Bucket D diff reviewed; follow-up actions applied for dead helper removal and table-driven ML non-active state coverage.

## Notes

No frontend/Svelte files or `internal/controlplane/` files were changed for this Bucket D slice.
