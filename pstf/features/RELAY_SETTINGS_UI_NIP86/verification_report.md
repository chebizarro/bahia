# RELAY_SETTINGS_UI_NIP86 Verification Report

## Scope

Implements Bead `bahia-ho1r`: an operator-facing relay settings UI and NIP-86 interface using Bahia's Nostr-native ContextVM mutation architecture.

## Evidence

- Backend ContextVM methods:
  - `settings/relay-policy.get`
  - `settings/relay-policy.apply`
  - `settings/relay-admin.call`
- Standard relay-list observables:
  - NIP-51 kind `30002` for `bahia-browser-v1`, `bahia-contextvm-v1`, and `bahia-service-v1`
  - NIP-51 kind `10050` for explicit notification DM receive relays
- Durable operator settings state:
  - kind `30900`
  - `d=relay-settings:operator`
  - `domain=relay-settings`
  - `schema=bahia.relay-settings.v1`
- Audit:
  - kind `4903`
  - `domain=relay-settings`
- Web UI:
  - Settings → Operator Relay Policy
  - Settings → Browser Session Relays local override
- Closeout code-lane additions inspected in this pass:
  - Backend NIP-86 relay administration target validation allows loopback plaintext development targets but requires external relay administration websocket targets to use `wss` and external HTTP administration targets to use `https`.
  - Settings-page canonical relay-settings hydration subscribes using advertised service relays in addition to browser/ContextVM relays.
  - Settings-page post-publish handling records the accepted mutation payload and prevents older/non-matching canonical `30900` state from overwriting the pending published operator policy.
  - The dirty-edit E2E now uses current valid Nostr timestamps (`now - 1`, `now`) instead of ancient values that the mock relay normalized non-deterministically.

## Verification

Local/sibling command results recorded for closeout:

- PASS: `GOCACHE=/tmp/bahia-go-cache go test ./internal/adapters/nostr ./internal/app ./internal/controlplane -run 'TestRelayPool|TestRelayTopologyCoordinator|TestRelaySettings|TestRelayAdmin'`
- PASS: `npm run test:unit -- --run tests/unit/relay-settings-controlplane.test.js` from `web/` (8 passed).
- PASS: `npm run test:e2e -- settings-relay-visibility.spec.js --grep "preserves dirty operator relay edits"` from `web/` (1 passed).
- PASS: `npm run test:e2e -- settings-relay-visibility.spec.js` from `web/` (4 passed).

## Defects

- `DEF-2026-06-07-DIRTY-CANONICAL-E2E` in `defects.json` is resolved. The blocker was a flawed E2E timestamp assumption, and the full settings relay visibility E2E gate now passes.

## Boundaries

- No new relay-routing kind was introduced.
- NIP-86 remains relay-owner administration and is called only after ContextVM validation.
- Browser session relay edits remain local and are labelled as local-only, not persistent operator policy.
- Bead `bahia-87y2` adds dedicated backend and Settings-page startup/live hydration from the canonical `30900` relay-settings read model using scoped `#d`, `#domain`, `#schema`, and service-author filters.
- Bead `bahia-2kjh` closes the runtime relay-pool topology convergence follow-up for hydrated relay policy.

## Closeout

All `bahia-ho1r` acceptance criteria are now covered by PSTF evidence: separated persistent/local relay settings UI, ContextVM mutation publication, canonical observable convergence, NIP-86 target restrictions, scoped subscription/EOSE/OK handling, fail-closed trust behavior, non-localStorage operator policy persistence, documentation, and deterministic tests.
