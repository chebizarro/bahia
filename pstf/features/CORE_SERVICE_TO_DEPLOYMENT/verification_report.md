# Verification Report — CORE_SERVICE_TO_DEPLOYMENT

## Summary

This PSTF slice is verified against the approved signer-first public-controlplane contract, including the 2026-05-06 HitL clarifications on preserved `/services` list state and backend canonical `5961` policy-gate enforcement.

Verified scope:
- public relay bootstrap for services, environments, artifacts, builds, intents, and runs
- signer-first service creation
- immediate `/services` hydration in the default view
- preserved `/services` search, runtime-filter, and pagination state after create
- signer-first deployment policy preview request wiring
- policy preview as a required gate before deployment intent creation
- backend policy enforcement for direct canonical `5961` deployment-intent requests
- signer-first deployment intent creation
- signer-first deployment approval
- deployment history and pending-approval rendering from public read models

## Commands Run

```bash
go test ./internal/controlplane
npm --prefix web run test:unit -- tests/unit/public-controlplane.test.js tests/unit/controlplane-store.test.js
npm --prefix web run test:e2e -- tests/e2e/service-deployment-policy-gate.spec.js tests/e2e/service-create-visibility-context.spec.js tests/e2e/service-deployment-public-smoke.spec.js
npm --prefix web run build
```

## Results

- `go test ./internal/controlplane`
  - **passed**
  - includes `reactor_policy_gate_test.go` for canonical backend `5961` policy blocking
- `npm --prefix web run test:unit -- tests/unit/public-controlplane.test.js tests/unit/controlplane-store.test.js`
  - **passed**
  - 2 files, 23 tests
- `npm --prefix web run test:e2e -- tests/e2e/service-deployment-policy-gate.spec.js tests/e2e/service-create-visibility-context.spec.js tests/e2e/service-deployment-public-smoke.spec.js`
  - **passed**
  - 3 Playwright files, 7 browser E2Es
- `npm --prefix web run build`
  - **passed**
  - production SvelteKit build completed successfully

## Acceptance Criteria Status

| Criterion | Status | Notes |
|---|---|---|
| CSTD-AC-001 | pass | Public relay read-model bootstrap remains covered by unit evidence for discovery, EOSE catch-up, canonical author filtering, and live subscription startup. |
| CSTD-AC-002 | pass | Service create remains verified as kind `5964` + correlated `7963` without REST write dependence. |
| CSTD-AC-003 | pass | `/services` now preserves active list state after create and only auto-shows the created service when it already matches the current view; default-view and preserved-context browser evidence both pass. |
| CSTD-AC-004 | pass | Policy preview remains verified as signer-first kind `5989` request/result wiring. |
| CSTD-AC-005 | pass | Browser coverage proves preview failure, blocker, and loading branches block creation, and backend coverage proves direct canonical `5961` requests are rejected when policy evaluation blocks. |
| CSTD-AC-006 | pass | Deployment intent create remains verified as kind `5961` + correlated public result flow when policy allows the request. |
| CSTD-AC-007 | pass | Deployment approval remains verified as kind `5966` + correlated public result flow. |
| CSTD-AC-008 | pass | `/deployments` remains reactive to relay-backed state after approval. |
| CSTD-AC-009 | pass | `/deployments/pending` remains reactive to relay-backed state and removes approved rows without refresh logic. |
| CSTD-AC-010 | pass | The smoke journey still proves the approved end-to-end public flow, while the focused browser specs prove the clarified list-state and policy-loading branches. |
| CSTD-AC-011 | pass | Active browser coverage still uses public request kinds and excludes encrypted request kind `5980`. |

## Defects Addressed

### Resolved in this verification pass

- **Backend policy gate bypass on canonical `5961` requests**
  - `internal/controlplane/reactor.go` now evaluates policy before creating a deployment intent and rejects blocked requests for any authorized signer-first client.
  - Covered by `internal/controlplane/reactor_policy_gate_test.go`.

- **`/services` create flow reset operator list state**
  - `/services` now preserves active search, runtime filter, and pagination after successful create.
  - Covered by `web/tests/e2e/service-create-visibility-context.spec.js`.

- **Missing browser proof for in-flight preview loading gate**
  - The public E2E harness now supports delayed policy preview resolution and the route is verified to keep `5961` unpublished until preview succeeds.
  - Covered by `web/tests/e2e/service-deployment-policy-gate.spec.js`.

### Previously resolved and still validated

- **Immediate service list hydration after signer-first create**
  - `/services` still upserts the created service from the successful `7963` result and shows it immediately when it belongs in the active view.

- **Deployment history route stale snapshot bug**
  - `/deployments` derives from relay-backed store state instead of a stale one-shot snapshot.

- **Pending approvals route stale snapshot bug**
  - `/deployments/pending` derives from relay-backed store state instead of a stale one-shot snapshot.

## Confidence Assessment

**Confidence: high**

Why high:
- Unit and Go contract tests cover relay bootstrap, public request publication, correlated result handling, and backend policy enforcement on the canonical deploy-intent path.
- Browser E2E now proves the approved happy path plus the clarified negative/path-dependent branches for preserved list state and preview loading/failure/blockers.
- Build verification confirms the changed routes, harnesses, and shared stores compile cleanly.

## Caveats

1. Deployment run log retrieval remains outside this slice.
   - `/deployments/runs/[id]` uses encrypted request/result fetches for stored stdout/stderr snapshots.
   - That behavior belongs to the encrypted request slice, not the core public deployment slice.

2. Deployment detail rendering remains observed browser behavior, not a separate acceptance criterion.
   - The smoke E2E still visits deployment detail and shows run visibility.
   - Readiness claims in this report map only to the current AC set.

## Recommendation

Treat `CORE_SERVICE_TO_DEPLOYMENT` as approved and verified for the current signer-first public service/deployment path.
