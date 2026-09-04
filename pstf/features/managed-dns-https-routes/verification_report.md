# Verification Report: managed-dns-https-routes

## Scope

- Beads: `bahia-l1o46`, `bahia-3kbce`
- External task: `bahia-managed-https-dns-routes-20260903`

## Acceptance mapping

| Criterion | Evidence | Result |
|---|---|---|
| AC1 | `docs/user-guide/guides/managed-dns-and-https-routes.md`, `docs/user-guide/index.md` | PASS |
| AC2 | Guide configuration checked against `internal/config/config.go`; reload semantics checked against `cmd/server/main.go` | PASS |
| AC3 | `TestManagedDNSAndHTTPSRouteFlow` | PASS |
| AC4 | `TestManagedDNSAndHTTPSRouteFlow`, existing route-attach handler tests | PASS |
| AC5 | `TestManagedDNSAndHTTPSRouteFlow`, existing coordinator and Cloudflare compensation tests | PASS |
| AC6 | `TestDNSOverrideNames` | PASS |
| AC7 | Operational-boundary and final-URL sections in the guide | PASS |

## Verification evidence

Executed on branch `task/bahia-managed-https-dns-routes` on 2026-09-03:

- `go test ./internal/reconcile -run 'TestDNSOverrideNames|TestDNSReconcilerAppliesPersistedRecordOverride' -count=1` — PASS
- `go test ./internal/controlplane -run 'TestRouteAttach' -count=1` — PASS
- `go test ./internal/adapters/routing -run 'TestCloudflareApply' -count=1` — PASS
- `go test ./internal/workflow -run 'TestManagedDNSAndHTTPSRouteFlow|TestExecuteDeployment_RouteOnly' -count=1` — PASS
- `go vet ./internal/reconcile ./internal/workflow ./internal/controlplane ./internal/adapters/routing` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `go test ./...` — PASS

The Astillero-shaped test is deterministic and uses no sleeps. It executes artifact deployment, records converged service state, reconciles `astillero.sharegap.net` to the fake DNS backend, plans a route covered by the desired-state hash, executes a route-only apply without another runtime convergence, and verifies compensated restoration on routing failure.

## Deviations

None.
