# WEB_NOSTR_VALIDATION_EOSE Verification Report

## Scope
Bucket 2 only: browser/web Nostr inbound validation, EOSE-authoritative query completion, branch/control-plane caller behavior, and replacement of weak web tests.

## Evidence
- Added browser inbound EVENT validation before subscription callbacks.
- `queryUntilEose` and `query` now reject timeout/abort/relay-CLOSED-incomplete states with `NostrIncompleteEOSEError` instead of resolving partial history.
- Relay transport disconnects before EOSE keep active historical queries subscribed so reconnect reissues the scoped REQ and waits for EOSE instead of abandoning dashboard bootstrap.
- Branch lookup uses EOSE-backed querying and returns explicit errors for incomplete history.
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

## Notes
A mistaken invocation through `pnpm --dir web test:unit -- ...` ran the broader suite instead of the intended file list and failed in tests outside the Bucket 2 gate. The focused command above is the targeted verification for this patch.
