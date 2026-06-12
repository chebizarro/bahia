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

## Conclusion

The route console regression coverage is verified for the representative Bahia web routes. The deterministic harness now covers REST data, Nostr bootstrap discovery, relay EVENT/EOSE delivery, route HTTP success, and NIP-11 metadata lookup for the DNS route.
