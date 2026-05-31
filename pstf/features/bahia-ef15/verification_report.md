# Verification Report: bahia-ef15

Date: 2026-05-31

## Summary

Changed controlplane bootstrap from blocking EOSE collection to a streaming Nostr subscription. `EVENT` callbacks now apply immediately while `controlplaneConnection.status` is `syncing`; per-relay `EOSE` callbacks mark the current subscription generation `live` only after every connected bootstrap relay has reported EOSE.

## Evidence

- `web/src/lib/stores/controlplane.svelte.js` no longer calls `nostr.queryUntilEose(readModelFilters())` in `bootstrapControlplane()`.
- Bootstrap starts `nostr.subscribe(readModelFilters(), { onEvent, onEose, onClosed })` immediately after relay connection.
- `controlplaneConnection.status` now includes `syncing` and transitions to `live` through `markBootstrapComplete()`.
- EOSE tracking uses normalized connected relay URLs and fails closed if a connect summary reports connected relays without URLs.
- Reconnect while still syncing creates a new subscription generation so stale EOSE callbacks cannot mark the current stream live.
- Unit tests were updated/added to cover immediate pre-EOSE application, multi-relay EOSE accounting, missing connected relay URL failure, and stale EOSE after reconnect.

## Commands Run

- `npm run test:unit -- --run tests/unit/controlplane-store.test.js` — blocked before assertions by unrelated parallel `bahia-rbqz` syntax error in `web/src/lib/nostr/pool-errors.js`.
- `npm run test:unit -- --run tests/unit/controlplane-store.test.js` — blocked before assertions by unrelated parallel `bahia-rbqz` syntax error in `web/src/lib/nostr/soul-drafts.js`; tracked by `bahia-uigl`.
- Oracle review mode over selected controlplane diff — reviewed; P1 findings were addressed.

## Remaining Work

- Complete or repair parallel bead `bahia-rbqz` so `web/src/lib/nostr/client.js` and re-exported modules parse, then use blocker bead `bahia-uigl` to rerun `npm run test:unit -- --run tests/unit/controlplane-store.test.js` and the broader web quality gates.
