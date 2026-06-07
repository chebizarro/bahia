# Verification Report — RELAY_STRATEGY_CONTEXTVM_BROWSER_DISCOVERY

Date: 2026-06-07
Beads: `bahia-8epx.4`, `bahia-8epx.4.1`, `bahia-8epx.4.2`, `bahia-8epx.4.3`

## Evidence

- `web/src/lib/stores/discovery.svelte.js` now recognizes `bahia-contextvm-v1`, exposes `nostr.contextvm_relays`, and records `nostr.contextvm_relay_metadata` / `_discovery.contextvm_relay_metadata`.
- Missing or empty `bahia-contextvm-v1` falls back to `bahia-browser-v1` relays with degraded metadata; browser bootstrap still fails closed when the browser relay set is absent.
- `web/src/lib/nostr/encrypted-controlplane.js` prefers discovered `nostr.contextvm_relays` for encrypted ContextVM request/result traffic and falls back to `nostr.browser_relays` for older discovery payloads.
- Encrypted availability remains gated by `features.encrypted_nostr_requests`, `nostr.service_pubkey`, and resolved relays.
- The discovery tests inject EVENT, EOSE, and auth-required CLOSED callbacks; no sleeps or polling completion logic were added.

## Checks

- `cd web && npm test -- --run tests/unit/discovery-store.test.js tests/unit/encrypted-controlplane.test.js` — passed, 2 files / 22 tests.

## Out of scope / remaining work

- Service NIP-65 publication, backend projector/kinds changes, CLI fallback, NIP-34 repository routing, AUTH epic work, and NIP-86 remain owned by sibling relay-strategy Beads.
