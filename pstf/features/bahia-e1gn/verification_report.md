# bahia-e1gn verification report

## Fixed in earlier slices
- Replaced invalid repeated-`b` service/factory pubkeys in E2E harnesses/spec system discovery with a valid secp256k1 hex pubkey.
- Allowed public ContextVM kind `25910` commands to resolve from scoped `25910` result subscriptions while preserving encrypted gift-wrap handling.
- Kept public control-plane commands on kind `25910`; encrypted transports still use gift-wrapped `1059` by default.
- Fixed dashboard payment harness result delivery by queuing results until matching `REQ` subscriptions exist, then delivering through mock relay `EVENT` callbacks.
- Fixed public harness projection trace correlation without requiring broad local filtering.
- Restored dashboard event dialog clicks through table action handling.
- Removed generated Playwright artifacts from version control and ignored `web/playwright-report/` plus `web/test-results/`.

## Last-mile fixes
- `deployment-history-and-run-details.spec.js` rollback dialog: confirmed a production UI wiring regression. `Table.svelte` intentionally ignores ordinary button clicks in rendered cells unless they opt into `data-dashboard-action`; the deployment-history rollback button did not. Fixed the production route by marking the rollback button with `data-dashboard-action="rollback"` so row action dispatch opens `Confirm Rollback` without changing row-navigation behavior.
- `deployment-history-and-run-details.spec.js` run logs: confirmed a stale deterministic harness. The app now uses gift-wrapped `1059` for encrypted ContextVM run-log requests, while the spec harness only recognized plaintext `25910`. Updated the harness to accept `1059`, return a correlated `1059` encrypted result, and deliver it through the mock relay event injector instead of a timeout side path.
- `policies-crud-smoke.spec.js` delete/tombstone: passed in isolation and in the affected-spec batch after the deployment/run-log fixes. No production policy tombstone change was needed.
- `relay-backed-web-functionality.spec.js` DNS/FIPS: passed in isolation and in the affected-spec batch. No production DNS read-model change was needed.

## Verification
- `npx playwright test tests/e2e/dashboard-smoke.spec.js tests/e2e/environments-crud-smoke.spec.js` — **33 passed**.
- `npx playwright test tests/e2e/dashboard-smoke.spec.js -g "should display recent spend cost summary card"` — **1 passed**.
- Earlier full baseline: `npm run test:e2e` — **142 passed, 33 failed, 1 skipped, 7 did not run**.
- Focused deployment regression check: `npm run test:e2e -- tests/e2e/deployment-history-and-run-details.spec.js --reporter=line --workers=1` — **3 passed**.
- Affected-spec isolation check: `npm run test:e2e -- tests/e2e/deployment-history-and-run-details.spec.js tests/e2e/policies-crud-smoke.spec.js tests/e2e/relay-backed-web-functionality.spec.js --reporter=line --workers=1` — **19 passed**.
- Final full suite: `npm run test:e2e` — **180 passed, 1 skipped, 0 failed**.

## Notes
No sleep-based waits were introduced in the touched harness paths. The final fixes use existing scoped action dispatch, gift-wrapped `1059` encrypted ContextVM semantics, mock relay `EVENT` delivery, and OK-preserving publish paths.
