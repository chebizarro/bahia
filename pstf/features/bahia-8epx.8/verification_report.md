# Verification Report: bahia-8epx.8

## Scope

Implemented only relay-strategy Item 8: NIP-34 repository selection and branch/state lookup relay routing in browser web helpers.

## Evidence

- `web/src/lib/nostr/repositories.js` preserves `30617` `relays` tag values as `relayUrls`.
- `web/src/lib/stores/repositories.svelte.js` preserves `relayUrls` in NIP-34 repository selections.
- `web/src/lib/nostr/branches.js` accepts selected repository relay hints and passes them to the EOSE-bounded `30618` query before global Bahia relay fallback.
- `web/src/lib/nostr/pool-query.js` can query an explicit relay set without replacing the global client relay configuration.
- `docs/user-guide/nostr-integration.md` and `docs/nostr-event-implementation-guide.md` document repository relay preference and missing-hint degraded fallback.

## Verification

Targeted tests run on 2026-06-06:

```bash
cd web && npm test -- --run tests/unit/test-utils-and-fixtures.test.js tests/unit/repositories-store.test.js
# Test Files 2 passed (2); Tests 13 passed (13)
```

## Out of scope

CLI/operator discovery fallback, NIP-42 AUTH, NIP-86 relay administration, service NIP-65 publication, and SoulFactory/ngit relay separation were not changed.
