# WEB_NOSTR_VALIDATION_EOSE Verification Report

## Scope
Browser/web Nostr inbound validation, EOSE-authoritative subscription completion, CLOSED/AUTH handling, branch/control-plane caller behavior, and replacement of weak web tests.

## Evidence
- Added browser inbound EVENT validation before subscription callbacks.
- Pool-backed browser subscriptions expose `onEvent`, `onEose`, `onClosed`, and AUTH/CLOSED classification paths; tests now target those callbacks instead of removed one-shot query helpers.
- Relay transport disconnects before EOSE keep active historical subscriptions live so reconnect can reissue scoped REQs and wait for EOSE instead of abandoning dashboard bootstrap.
- Branch lookup uses scoped subscriptions and returns explicit errors when CLOSED occurs before/after partial branch events.
- Soul Factory, docs, repositories, FIPS mesh, and discovery unit tests were migrated in `bahia-y32g` from the removed `queryUntilEose`/`queryOrPartial`/`fetchRuntimeCapabilities` API surface to the current pool subscription API while preserving EOSE/CLOSED/AUTH intent.
- The old explicit degraded metadata contract (`{ complete, degraded, relaySummary }`) is not consistently exposed by current pool-backed read models. That behavior was not hidden in tests; it is tracked as follow-up bead `bahia-qc5c`.
- Control-plane result waits aggregate CLOSED reasons and distinguish auth-related closures.

## Verification
Passed on 2026-05-20 for regression `bahia-f1mu`:

```bash
pnpm --dir web exec vitest run --config vitest.config.js tests/unit/sanity.test.js tests/unit/nostr-client-parsing.test.js
```

Result: 2 files, 49 tests passed.

Passed on 2026-05-20 for the broader WEB_NOSTR_VALIDATION_EOSE gate:

```bash
pnpm --dir web exec vitest run --config vitest.config.js tests/unit/sanity.test.js tests/unit/test-utils-and-fixtures.test.js tests/unit/nostr-client-parsing.test.js tests/unit/controlplane-requests.test.js
```

Result: 4 files, 59 tests passed.

Additional bahia-zui7 focused gate passed on 2026-06-02:

```bash
pnpm --dir web exec vitest run --config vitest.config.js tests/unit/sanity.test.js tests/unit/test-utils-and-fixtures.test.js tests/unit/nostr-client-parsing.test.js tests/unit/repositories-store.test.js tests/unit/souls-store.test.js
```

Result: 5 files, 104 tests passed.

Passed on 2026-06-30 for `bahia-y32g` after pool API test migration:

```bash
cd web && pnpm test:unit
```

Result: 70 files, 525 tests passed.

## Notes
`bahia-y32g` confirmed that `web/src/lib/nostr/pool-query.js`, `queryUntilEose`, `queryOrPartial`, `NostrIncompleteEOSEError`, and `fetchRuntimeCapabilities` are no longer part of the browser Nostr API. Tests that referenced those symbols were rewritten to the current public API rather than shimmed back into production. Behavioral coverage for event validation, EOSE-authoritative catch-up, CLOSED/AUTH handling, and persistent store subscriptions remains active. The retired degraded-metadata helper assertions are captured by `bahia-qc5c` so the pool-era contract can be specified and implemented deliberately.
