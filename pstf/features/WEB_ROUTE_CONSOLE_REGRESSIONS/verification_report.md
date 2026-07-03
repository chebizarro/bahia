# WEB_ROUTE_CONSOLE_REGRESSIONS Verification Report

## Beads

- Issue: `bahia-rso1` — Add Bahia web route console-error regressions

## Evidence

### Playwright MCP route sweep

- Ran a representative route-by-route sweep through the Playwright MCP against `http://127.0.0.1:4173`.
- Initial ad hoc MCP harness emitted `EOSE` without the scoped discovery `EVENT`s, producing repeated bootstrap errors: `No trusted Bahia system discovery event received before EOSE`.
- Corrected the MCP harness to deliver stored discovery `EVENT`s before `EOSE`; the rerun returned `[]` for routes with non-200 responses, page errors, or console errors.

### Regression test failure before harness fix

Command:

```sh
pnpm exec playwright test tests/e2e/route-console-regression.spec.js --reporter=list
```

Result before deterministic NIP-11 metadata fulfillment:

- 42 passed
- 1 failed: `/dns` logged `Failed to load resource: net::ERR_NAME_NOT_RESOLVED`

Cause: DNS route correctly queries NIP-11 relay metadata for `ws://relay.test.local`; the test harness had not fulfilled `http://relay.test.local/**`.

### Regression test after fix

Command:

```sh
pnpm exec playwright test tests/e2e/route-console-regression.spec.js --reporter=list
```

Result:

- 43 passed in 7.5s

### Lint

Command:

```sh
pnpm lint
```

Result:

- `svelte-check found 0 errors and 0 warnings`

### Review disposition

A review pass recommended asserting route HTTP success and adding CORS headers to the deterministic NIP-11 relay metadata fulfillment. Both changes were applied before final verification.

Unit tests were not run because the behavior under acceptance is Playwright route/runtime behavior and the change adds E2E-only coverage plus PSTF artifacts; no production or unit-targeted code changed.

## 2026-07-03 bahia-dgdm follow-up

Command:

```sh
npm run test:e2e -- route-console-regression.spec.js soul-provisioned-visibility.spec.js souls-gallery-live.spec.js --reporter=line --workers=1
```

Result:

- 45 passed in 35.9s

Evidence:

- `web/tests/e2e/route-console-regression.spec.js` now seeds the canonical relay-backed `30900` read models for the exact deployment intent/run and artifact IDs navigated by the route sweep.
- `web/tests/e2e/soul-provisioned-visibility.spec.js` and `web/tests/e2e/souls-gallery-live.spec.js` use current accessible card/stat selectors rather than strict ambiguous text/parent selectors.
- No sleep-based waits were added.

Residuals tracked outside this verified slice:

- `bahia-s2fz`: LLM async deploy/rollback appears to use the encrypted default transport instead of canonical public ContextVM `25910`; the LLM spec still fails because no public request reaches the deterministic harness.
- `bahia-qiyu`: relay-backed encrypted ContextVM fixture discovery/publish behavior needs a focused encrypted-transport pass.

## Conclusion

The route console regression coverage is verified for the representative Bahia web routes. The deterministic harness now covers REST data, Nostr bootstrap discovery, relay EVENT/EOSE delivery, route HTTP success, NIP-11 metadata lookup for the DNS route, and canonical `30900` read models for deployment/artifact detail pages.
