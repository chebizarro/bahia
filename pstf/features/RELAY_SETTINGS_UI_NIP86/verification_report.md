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

## Verification

- Backend targeted tests pass for relay URL validation, non-empty relay topology validation, strict trusted/admin pubkey validation, state/audit publication, read-only policy fetch behavior, and NIP-86 target restrictions.
- Web helper tests pass for ContextVM method/payload construction.
- Settings e2e visibility tests pass for the operator relay policy surface and local-only browser override boundary.
- Web production build passes.

## Boundaries

- No new relay-routing kind was introduced.
- NIP-86 remains relay-owner administration and is called only after ContextVM validation.
- Browser session relay edits remain local and are labelled as local-only, not persistent operator policy.
- Dedicated startup/live hydration from the canonical `30900` relay-settings read model remains tracked in Bead `bahia-87y2`; this patch publishes canonical state but does not yet add that subscriber/projection path.
