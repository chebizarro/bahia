# bahia-e1gn verification report

## Fixed in this slice
- Replaced invalid repeated-`b` service/factory pubkeys in E2E harnesses/spec system discovery with a valid secp256k1 hex pubkey.
- Allowed public ContextVM kind `25910` commands to resolve from scoped `25910` result subscriptions while preserving encrypted gift-wrap handling.
- Kept public control-plane commands on kind `25910`; encrypted transports still use gift-wrapped `1059` by default.
- Fixed dashboard payment harness result delivery by queuing results until matching `REQ` subscriptions exist, then delivering through mock relay `EVENT` callbacks.
- Fixed public harness projection trace correlation without requiring broad local filtering.
- Restored dashboard event dialog clicks through table action handling.
- Removed generated Playwright artifacts from version control and ignored `web/playwright-report/` plus `web/test-results/`.

## Verification
- `npx playwright test tests/e2e/dashboard-smoke.spec.js tests/e2e/environments-crud-smoke.spec.js` — **33 passed**.
- `npx playwright test tests/e2e/dashboard-smoke.spec.js -g "should display recent spend cost summary card"` — **1 passed**.
- `npm run test:e2e` — **142 passed, 33 failed, 1 skipped, 7 did not run**.

## Remaining grouped failures
- `bahia-la6v`: legacy services/policies CRUD specs need relay-native fixtures or explicit real REST fallback alignment.
- `bahia-rxae`: SBOM/artifact/service-secret specs need canonical relay/event fixtures matching current pages.
- `bahia-p0vz`: encrypted notification specs need expectations updated for gift-wrapped `1059` transport and mixed-session seed visibility.
- `bahia-dgdm`: route-console, LLM, and Souls residual regressions need current seeded detail records/selectors.

## Notes
No sleep-based waits were introduced in the touched harness paths. The fixes use scoped subscriptions, queued EVENT delivery, OK-preserving publish paths, and valid Nostr pubkeys.
