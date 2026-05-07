# Verification Report — TRANSPORT_POLICY_GOVERNANCE

## Summary
Current verification still does **not** establish full compliance with the approved acceptance criteria or the full test matrix, but the browser payment-transport defect cluster is now fixed and verified.

What is verified now:
- Implemented deterministic unit coverage for discovery separation, encrypted request/result transport, route-access policy, encrypted `/payments` store behavior, relay-backed bootstrap semantics, and the dashboard Recent Spend encrypted-payment helper/readiness path is passing.
- Representative encrypted browser e2e coverage is passing.
- Representative public browser e2e coverage is passing for public-relay usage and relay-backed progression, but it still lacks the accepted-OK assertion required by the matrix.
- The dashboard Recent Spend surface now sources worker payment history through encrypted browser transport, and the dashboard e2e harness explicitly fails if canonical REST `payments/history` reads are attempted.
- Existing Go coverage for explicit pre-acceptance-only operator HTTP fallback is passing.
- Manual review with explicit recorded findings confirms the canonical evidence corpus treats 311xx as compatibility-only and uses 596x/598x families for approved proof.

What still blocks a full verification result:
- The settings page still renders `systemInfo.nostr.relays`, which violates `TPG-AC-010`.
- Four required verification proofs are still missing or incomplete: accepted-OK public proof (`TPG-T-002`), signer-without-NIP-44 fail-closed proof (`TPG-T-005`), EventSource-exclusion contract proof (`TPG-T-009`), and mixed public+encrypted single-session proof (`TPG-T-015`).

## Commands Run
```bash
cd web && npx vitest run --config vitest.config.js \
  tests/unit/controlplane-store.test.js \
  tests/unit/encrypted-controlplane.test.js \
  tests/unit/encrypted-route-stores.test.js \
  tests/unit/route-access.test.js \
  tests/unit/encrypted-domain-stores.test.js

cd web && npx playwright test \
  tests/e2e/service-deployment-public-smoke.spec.js \
  tests/e2e/notifications-encrypted-smoke.spec.js

go test ./cmd/cli ./pkg/client

cd web && npx vitest run --config vitest.config.js \
  tests/unit/encrypted-domain-stores.test.js \
  tests/unit/dashboard-cost-summary.test.js

cd web && npx playwright test tests/e2e/dashboard-smoke.spec.js
```

Observed results from this patch verification:
- Vitest targeted patch run: 2 files, 10 tests passed.
- Playwright targeted patch run: `dashboard-smoke.spec.js`, 22 tests passed.

Manual review performed:
- `docs/control-planes.md`
- `docs/protocol-compatibility.md`
- `docs/nostr-commands.md`
- `web/tests/e2e/service-deployment-public-smoke.spec.js`
- `web/tests/e2e/notifications-encrypted-smoke.spec.js`
- `web/src/routes/+page.svelte`
- `web/src/routes/settings/+page.svelte`
- `web/src/lib/stores/payments.svelte.js`
- `web/src/lib/api/client.js`
- `web/src/lib/stores/auth.svelte.js`

Manual review findings recorded in this verification pass:
- `docs/nostr-commands.md:185` states kinds `31100–31105` are deprecated compatibility commands and new integrations must not publish them.
- `docs/protocol-compatibility.md:135` states the `31100-31105` bridge exists only as deprecated compatibility behavior.
- `docs/control-planes.md:339-341` maps legacy `31102`/`31103`/`31104`/`31105` behaviors to canonical `5961`/`5966`/`5962` request families.
- `web/tests/e2e/service-deployment-public-smoke.spec.js:46,66,89-90` records public proof using `5961`, `5966`, `5964`, and `5989`, and explicitly not `5980`.
- `web/tests/e2e/harnesses/notifications-encrypted.js:156` plus `web/tests/e2e/notifications-encrypted-smoke.spec.js:65` record the representative encrypted proof using kind `5980` and encrypted notification operations.
- `web/src/routes/+page.svelte:203-229` now routes dashboard Recent Spend payment-history reads through `requestPaymentHistoryRecords(...)`, which issues encrypted `payments.history` requests.
- `web/src/lib/stores/payments.svelte.js:25-39` now waits for system discovery and auth initialization before issuing encrypted payment-history requests, preventing a cold-start miss when workers load first.

## Acceptance Criteria Status
| AC ID | Status | Basis |
| --- | --- | --- |
| TPG-AC-001 | pass | `TPG-T-001` passed in Vitest; public bootstrap and encrypted capability separation is covered by current unit tests. |
| TPG-AC-002 | partial | Existing public e2e flow passed, but `TPG-T-002` is still incomplete because accepted relay OK handling is not asserted by the current harness. |
| TPG-AC-003 | pass | `TPG-T-003` and `TPG-T-004` passed; encrypted request/result transport and representative encrypted browser flow are both verified. |
| TPG-AC-004 | partial | Current code inspection supports fail-closed behavior when NIP-44 helpers are absent (`auth.svelte.js` throws locally), but the signer-capability scenario required by `TPG-T-005` is not yet covered by an implemented test. |
| TPG-AC-005 | pass | `TPG-T-006` passed; route-level REST compatibility remains opt-in only via explicit overrides. |
| TPG-AC-006 | pass | `TPG-T-007` remains passing for the `/payments` store, and `TPG-T-008` now passes with an encrypted dashboard harness that guards REST `payments/history` to failure and asserts encrypted `payments.history` relay traffic. |
| TPG-AC-007 | not_verified | Existing evidence shows the legacy `streamLogs(...)` helper remains in `web/src/lib/api/client.js`, but the dedicated EventSource-exclusion contract proof `TPG-T-009` does not exist yet, so this AC is not fully verified. |
| TPG-AC-008 | pass | `go test ./cmd/cli ./pkg/client` passed; existing operator tests verify explicit pre-acceptance-only fallback. |
| TPG-AC-009 | pass | Manual review verified docs classify 311xx as deprecated compatibility-only, while representative approval-backed tests use 596x/598x families. |
| TPG-AC-010 | fail | Settings UI still renders `systemInfo.nostr.relays` directly and does not yet source operator relay visibility from a service-authored kind 30002 relay-list event. |
| TPG-AC-011 | pass | `TPG-T-013` and `TPG-T-014` passed; relay-backed public bootstrap and encrypted request/result completion are grounded in subscriptions, EOSE-bounded catch-up, accepted publish handling, and correlated results. |
| TPG-AC-012 | not_verified | The required mixed public+encrypted single-session proof `TPG-T-015` does not exist yet, so this AC is still unverified. |

## Test Matrix Status
| Test ID | Status | Classification | Evidence |
| --- | --- | --- | --- |
| TPG-T-001 | pass | verified existing test | Vitest: included in 5-file, 32-test passing unit run. |
| TPG-T-002 | not_implemented | test defect | Existing public smoke exists and passes, but accepted relay OK assertions are missing. |
| TPG-T-003 | pass | verified existing test | Vitest passing unit run. |
| TPG-T-004 | pass | verified existing test | Playwright: `notifications-encrypted-smoke.spec.js` passed. |
| TPG-T-005 | not_implemented | test defect | Missing signer-without-NIP-44 scenario coverage. |
| TPG-T-006 | pass | verified existing test | Vitest passing unit run. |
| TPG-T-007 | pass | verified existing test | Vitest passing unit run. |
| TPG-T-008 | pass | verified modified test | Playwright: `dashboard-smoke.spec.js` passed with REST `payments/history` guarded to failure and encrypted `payments.history` relay traces asserted for the Recent Spend card; supporting unit coverage now verifies the helper waits for auth/system readiness before requesting encrypted payment history. |
| TPG-T-009 | not_implemented | test defect | `web/tests/unit/transport-policy-contract.test.js` does not exist. |
| TPG-T-010 | pass | verified existing test | `go test ./cmd/cli ./pkg/client` passed. |
| TPG-T-011 | pass | manual verification | Recorded manual findings in this report cite docs and representative tests showing 311xx is compatibility-only and approval evidence uses 596x/598x families. |
| TPG-T-012 | not_implemented | product defect + missing test | Settings implementation still uses raw discovery relays and the target e2e file does not exist. |
| TPG-T-013 | pass | verified existing test | Vitest passing unit run. |
| TPG-T-014 | pass | verified existing test | Vitest passing unit run. |
| TPG-T-015 | not_implemented | test defect | Mixed single-session relay-separation proof file does not exist. |

Target existence check:
- Missing: `web/tests/unit/transport-policy-contract.test.js`
- Missing: `web/tests/e2e/settings-relay-visibility.spec.js`
- Missing: `web/tests/e2e/mixed-transport-session.spec.js`

## Defects
### Open product defects
- `TPG-D-002` — Settings relay visibility still depends on raw system discovery relays instead of service-authored NIP-51 relay-list data.

### Patched and verified in this pass
- `TPG-D-001` — Dashboard Recent Spend no longer depends on REST payment history; targeted unit and Playwright coverage verified encrypted `payments.history` browser transport for the card and the cold-start readiness path.

### Test defects / verification gaps
- `TPG-T-002` is still missing accepted-OK public proof.
- `TPG-T-005` is still missing explicit signer-without-NIP-44 fail-closed coverage.
- `TPG-T-009` target contract test does not exist.
- `TPG-T-015` target mixed-session e2e proof does not exist.

## Ambiguities / Human Decisions Needed
No new human-decision blockers were found in this patch verification pass.

Open items are implementation drift or missing proof, not unresolved product intent:
- Browser payment transport direction was already decided by `HITL-TRANSPORT_POLICY_GOVERNANCE-002`.
- Operator relay visibility direction was already decided by `HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-004`.
- EventSource treatment was already decided by `HITL-TRANSPORT_POLICY_GOVERNANCE-003`.

## Confidence Assessment
**Moderate for the verified implemented subset; still low for overall feature completion.**

Reasons:
- The currently implemented deterministic unit/e2e/Go coverage that exists is stable and passing.
- The browser payment transport drift (`TPG-D-001`) is now fixed and backed by targeted regression coverage that fails closed on legacy REST reads.
- The remaining load-bearing implementation drift (`TPG-AC-010`) is directly visible in source and does not depend on flaky environment behavior.
- Several matrix requirements remain unimplemented, so full end-to-end transport-governance verification is still incomplete.
- `TPG-AC-012` remains unverified because the required combined-session proof does not exist yet.

## Recommendation
**NEEDS_WORK**

Do not mark `TRANSPORT_POLICY_GOVERNANCE` verified yet.

Minimum next steps:
1. Fix `TPG-D-002` by sourcing settings relay visibility from a service-authored kind 30002 relay-list event.
2. Implement missing proof for `TPG-T-002`, `TPG-T-005`, `TPG-T-009`, and `TPG-T-015`.
3. Re-run the same verification commands plus the new tests once those changes land.
