# WEB_NOSTR_VALIDATION_EOSE Verification Report

## Scope
Bucket 2 only: browser/web Nostr inbound validation, EOSE-authoritative query completion, branch/control-plane caller behavior, and replacement of weak web tests.

## Evidence
- Added browser inbound EVENT validation before subscription callbacks.
- `queryUntilEose` and `query` now reject timeout/abort/closed-incomplete states with `NostrIncompleteEOSEError` instead of resolving partial history.
- Branch lookup uses EOSE-backed querying and returns explicit errors for incomplete history.
- Control-plane result waits aggregate CLOSED reasons and distinguish auth-related closures.

## Verification
Passed:

```bash
pnpm --dir web exec vitest run --config vitest.config.js tests/unit/sanity.test.js tests/unit/test-utils-and-fixtures.test.js tests/unit/nostr-client-parsing.test.js tests/unit/controlplane-requests.test.js
```

Result: 4 files, 57 tests passed.

## Notes
A mistaken invocation through `pnpm --dir web test:unit -- ...` ran the broader suite instead of the intended file list and failed in tests outside the Bucket 2 gate. The focused command above is the targeted verification for this patch.
