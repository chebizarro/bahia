# Verification Report — CORE_SERVICE_TO_DEPLOYMENT

## Summary

This PSTF slice is verified against the approved signer-first public-controlplane contract.

Verified scope:
- public relay bootstrap for services, environments, artifacts, builds, intents, and runs
- signer-first service creation
- immediate `/services` hydration after successful create
- signer-first deployment policy preview request wiring
- policy preview as a required gate before deployment intent creation
- signer-first deployment intent creation
- signer-first deployment approval
- deployment history and detail rendering from public read models

## Commands Run

```bash
npm --prefix web run test:unit -- tests/unit/public-controlplane.test.js tests/unit/controlplane-store.test.js
npm --prefix web run test:e2e -- tests/e2e/service-deployment-public-smoke.spec.js tests/e2e/service-deployment-policy-gate.spec.js
npm --prefix web run build
```

## Results

- `npm --prefix web run test:unit -- tests/unit/public-controlplane.test.js tests/unit/controlplane-store.test.js`
  - **passed**
  - 2 files, 23 tests
- `npm --prefix web run test:e2e -- tests/e2e/service-deployment-public-smoke.spec.js tests/e2e/service-deployment-policy-gate.spec.js`
  - **passed**
  - 3 Playwright browser E2Es
- `npm --prefix web run build`
  - **passed**
  - production SvelteKit build completed successfully

## Acceptance Criteria Status

| Criterion | Status | Notes |
|---|---|---|
| CSTD-AC-001 | pass | Public relay read-model bootstrap remains covered by unit and browser evidence. |
| CSTD-AC-002 | pass | Service create remains verified as kind `5964` + correlated `7963` without REST write dependence. |
| CSTD-AC-003 | pass | Immediate `/services` hydration is proven in the smoke journey with synthetic create projection suppressed, so visibility comes from the successful create-result path. |
| CSTD-AC-004 | pass | Policy preview remains verified as signer-first kind `5989` request/result wiring. |
| CSTD-AC-005 | pass | Browser coverage now proves preview failure and preview blockers both prevent kind `5961` publication and show blocking feedback. |
| CSTD-AC-006 | pass | Deployment intent create remains verified as kind `5961` + correlated public result flow. |
| CSTD-AC-007 | pass | Deployment approval remains verified as kind `5966` + correlated public result flow. |
| CSTD-AC-008 | pass | `/deployments` and `/deployments/pending` remain reactive to relay-backed state after approval. |
| CSTD-AC-009 | pass | Deployment detail and history remain rendered from public read models. |
| CSTD-AC-010 | pass | One browser smoke journey now proves the approved end-to-end core public contract. |
| CSTD-AC-011 | pass | Active browser coverage still uses public request kinds and excludes encrypted request kind `5980`. |

## Defects Addressed

### Resolved in this verification pass

- **Immediate service list hydration after signer-first create**
  - `/services` now upserts the created service from the successful `7963` result and resets filters/pagination so the row is visible immediately.
  - Covered by `web/tests/e2e/service-deployment-public-smoke.spec.js` with the synthetic create projection suppressed.

- **Policy preview treated as advisory instead of required gate**
  - `/services/[id]` now blocks deployment intent creation until preview succeeds and reports no blockers.
  - Covered by `web/tests/e2e/service-deployment-policy-gate.spec.js` for both preview-failure and blocker responses.

### Previously resolved and still validated

- **Deployment history route stale snapshot bug**
  - `/deployments` derives from relay-backed store state instead of a stale one-shot snapshot.

- **Pending approvals route stale snapshot bug**
  - `/deployments/pending` derives from relay-backed store state instead of a stale one-shot snapshot.

## Confidence Assessment

**Confidence: high**

Why high:
- Unit tests cover relay bootstrap, public request publication, correlated result handling, and command wiring.
- Browser E2E now proves both the approved happy path and the new mandatory negative-path policy gate behavior.
- Build verification confirms the changed routes and shared stores compile cleanly.

## Caveats

1. Deployment run log retrieval remains outside this slice.
   - `/deployments/runs/[id]` uses encrypted request/result fetches for stored stdout/stderr snapshots.
   - That behavior belongs to the encrypted request slice, not the core public deployment slice.

2. Immediate hydration still reconciles with later relay projections.
   - The approved UX promise is now satisfied from the create-result path.
   - Later Bahia-authored replaceable projections remain authoritative for steady-state convergence.

## Recommendation

Treat `CORE_SERVICE_TO_DEPLOYMENT` as approved and verified for the current signer-first public service/deployment path.
