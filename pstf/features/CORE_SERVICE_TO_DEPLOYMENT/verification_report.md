# Verification Report — CORE_SERVICE_TO_DEPLOYMENT

## Summary

This PSTF slice is fully verified against the selected signer-first public-controlplane evidence set.

The verified scope is the current core public service-to-deployment path:
- public relay bootstrap for services/environments/artifacts/builds/intents/runs
- signer-first service creation
- signer-first deployment policy preview request wiring
- signer-first deployment intent creation
- signer-first deployment approval
- deployment history/detail rendering from public read models

The older REST-shaped deployment E2Es were intentionally excluded from authoritative evidence because they no longer match the implemented product contract.

## Commands Run

```bash
npm --prefix web run test:unit -- tests/unit/controlplane-store.test.js tests/unit/controlplane-requests.test.js tests/unit/public-controlplane.test.js
npm --prefix web run test:e2e -- tests/e2e/service-deployment-public-smoke.spec.js
```

## Results

- `npm --prefix web run test:unit -- tests/unit/controlplane-store.test.js tests/unit/controlplane-requests.test.js tests/unit/public-controlplane.test.js`
  - **passed**
  - 3 files, 18 tests
- `npm --prefix web run test:e2e -- tests/e2e/service-deployment-public-smoke.spec.js`
  - **passed**
  - 1 Playwright browser E2E

## Acceptance Criteria Status

| Criterion | Status | Notes |
|---|---|---|
| CSTD-AC-001 | pass | Public relay read-model bootstrap is covered by unit and browser evidence. |
| CSTD-AC-002 | pass | Service create is verified as kind 5964 + 7963 without REST CRUD mocks. |
| CSTD-AC-003 | pass | Policy preview wiring is signer-first public command wiring; browser trace includes kind 5989. |
| CSTD-AC-004 | pass | Deployment intent create is verified as kind 5961 + correlated public result flow. |
| CSTD-AC-005 | pass | Deployment approval is verified as kind 5966 + correlated public result flow. |
| CSTD-AC-006 | pass | `/deployments` now derives from relay-backed store state rather than a stale one-shot snapshot. |
| CSTD-AC-007 | pass | `/deployments/pending` now derives from relay-backed store state rather than a stale one-shot snapshot. |
| CSTD-AC-008 | pass | A browser E2E proves the current signer-first public journey without REST CRUD mocks. |
| CSTD-AC-009 | pass | The selected browser flow uses public request kinds and excludes encrypted request kind 5980. |

## Defects

### Resolved during verification

- **Deployment history route stale snapshot bug**
  - Problem: `web/src/routes/deployments/+page.svelte` copied `deploymentIntents` into local component state inside `loadAllIntents()`. Late-arriving relay projections after navigation were not rendered.
  - Resolution: derive `intents` reactively from the shared controlplane store after bootstrap.

- **Pending approvals route stale snapshot bug**
  - Problem: `web/src/routes/deployments/pending/+page.svelte` copied `deploymentIntents` into local component state and manually filtered rows after approval/rejection.
  - Resolution: derive `pendingIntents` reactively from the shared controlplane store and let approved/rejected 31967 updates drive the UI.

### Classified but not treated as current-slice defects

- **REST-shaped deployment E2Es are stale evidence**
  - `web/tests/e2e/deployment-workflow-critical.spec.js` and related smoke tests still encode the older REST CRUD workflow.
  - They were not used to verify this slice because the current implementation routes core service/deployment writes through the public signer-first controlplane.

## Confidence Assessment

**Confidence: high**

Why high:
- Unit tests cover public relay bootstrap, public request publication, correlated result handling, and domain-specific service/deployment command wiring.
- Browser E2E proves the current service/deployment journey over signer-first public controlplane requests and relay-backed history/detail pages.
- Verification surfaced and resolved real route-level state bugs rather than only documenting the existing implementation.

## Caveats

1. Immediate post-create service list hydration is not a separate acceptance criterion in this slice.
   - The backend contract clearly emits a 7963 terminal result for service create.
   - User-visible service list hydration depends on the service-registry projector path and is treated as an eventual read-model concern rather than a separate MUST in this slice.

2. Deployment run log retrieval remains outside this slice.
   - `/deployments/runs/[id]` uses encrypted request/result fetches for stored stdout/stderr snapshots.
   - That behavior belongs to the encrypted request slice, not the core public deployment slice.

## Recommendation

Treat `CORE_SERVICE_TO_DEPLOYMENT` as fully verified for the current signer-first public service/deployment path.

Recommended next moves:
1. Classify or retire the remaining REST-shaped deployment E2Es so they stop implying the wrong product contract.
2. Decide whether immediate post-create service list hydration should become an explicit release criterion once the projector timing is treated as normative behavior.
3. Continue PSTF onboarding with the next adjacent slice that touches artifacts/builds or deployment run logs.
