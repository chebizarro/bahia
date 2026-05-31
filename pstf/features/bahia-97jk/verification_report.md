# Verification Report: bahia-97jk

Date: 2026-05-31

## Summary

Implemented eager relay discovery/connection on app load. The root layout now starts `initializeAuth()` and `eagerRelayConnect()` together, then runs `loadAll()` after both startup paths settle. `eagerRelayConnect()` reuses the existing Nostr discovery path so relay connection status changes to connecting immediately and discovery results are cached for later control-plane bootstrap.

## Evidence

- `web/src/routes/+layout.svelte` uses `Promise.all([initializeAuth(), eagerRelayConnect()])` before `loadAll()`.
- `web/src/lib/stores/system.svelte.js` exports `eagerRelayConnect()` and dedupes it with `loadSystemInfo()`.
- `web/src/lib/nostr/pool.js` dedupes in-flight `connect()` calls for the same relay set so eager and follow-on bootstraps do not duplicate the same handshake.
- Unit coverage verifies eager discovery starts immediately and in-flight relay connection reuse.

## Commands Run

- `npm run test:unit -- --run tests/unit/system-store.test.js tests/unit/nostr-client-parsing.test.js` — failed because concurrent client split work left `web/src/lib/nostr/validation.js` syntactically invalid before this task's assertions could run. Tracked as Bead `bahia-9blw`.
- `npm run test:unit -- --run tests/unit/system-store.test.js tests/unit/nostr-pool.test.js` — passed (2 files, 5 tests).

## Remaining Work

- `bahia-9blw` tracks the unrelated `validation.js` syntax error from concurrent client split work that blocks tests importing `client.js`.
