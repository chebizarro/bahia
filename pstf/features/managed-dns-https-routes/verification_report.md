# Verification Report: managed-dns-https-routes

## Scope

- Beads: `bahia-l1o46`, `bahia-3kbce`, `bahia-ex2ri`, `bahia-kkk56`
- External tasks: `bahia-managed-https-dns-routes-20260903`, `bahia-deployed-artifact-dns-routes-20260904`

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
| AC8 | Domain hash/validation, planner, handler, client, and CLI opt-out tests | PASS |
| AC9 | Nginx ownership, atomic apply, collision, health, and failure-restoration tests | PASS |
| AC10 | Nginx absent-stanza removal convergence test | PASS |
| AC11 | Composite ordering/compensation tests and route-only coordinator execution | PASS |
| AC12 | Internal-routing config validation tests and LAN HTTPS documentation | PASS |
| AC13 | `TestCentralizedOwnershipGuardsEndToEndAstillero`, `TestDNSReconcilerConvergesRemoteAgentInclude` | PASS |
| AC14 | Ordered migration and independent rollback sections in the runbook and centralized-ownership guide | PASS |

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

## 2026-09-05 internal HTTPS extension evidence

Executed on branch `task/bahia-managed-guards`:

- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test ./internal/domain/... ./internal/service/... ./internal/adapters/routing/... ./internal/config/... ./internal/app/... ./internal/workflow/... ./internal/controlplane/... ./pkg/client/... ./cmd/cli/... -count=1` — PASS
- `go test ./...` — PASS
- JSON parsing of `feature_spec.json`, `acceptance_criteria.json`, and `test_matrix.json` — PASS

The first targeted run exposed the already-tracked `bahia-uwh8e` local UDP-responder scheduling flake: the Cloudflare no-public-record test observed an `i/o timeout` under parallel package load while passing in isolation. The test helper now waits for the responder goroutine to start; the exact targeted command and full repository suite then passed.

## 2026-09-05 centralized ownership cross-guard evidence

- `go test ./internal/adapters/dns -run TestCentralizedOwnershipGuardsEndToEndAstillero -count=1` — PASS
- `TestCentralizedOwnershipGuardsEndToEndAstillero` drives the authoritative Astillero A record through the serialized remote-agent path, checks the `local=/sharegap.net/` and `address=` lines, applies the same hostname's Cloudflare plus InternalHTTPS plan through the composite, verifies the nginx vhost, and proves both route outputs affect the signed desired-state hash.
- `docs/runbooks/core01-dnsmasq-agent.md` now sequences both hand-applied guard migrations and gives independent DNS and internal-HTTPS rollback procedures.
- `docs/user-guide/guides/managed-dns-and-https-routes.md` now presents external Cloudflare, internal DNS authority, and internal HTTPS as one centralized ownership flow.
- The first full-suite run exposed a final-attempt error-classification race in `TestCloudflareVerifyHTTPSReportsNoPublicDNSRecordBeforeHTTP`: the overall deadline replaced an earlier actionable NXDOMAIN result with `i/o timeout`. `verifyHTTPS` now preserves the earlier DNS result when its final lookup reaches the verification deadline; the regression passed 20 consecutive runs.
- `go build ./...` — PASS
- `go vet ./...` — PASS
- `go test ./... -count=1` — PASS
- `cd web && npx vitest run` — PASS (95 files, 718 tests)
- `cd web && pnpm run lint` — PASS (0 errors, 0 warnings)

## Deviations

The scoped Oracle review integration did not render the published 27-file patch to the reviewer; the patch artifact was manually inspected instead. No commits, pushes, or nostrig commands were run, per task instruction.
