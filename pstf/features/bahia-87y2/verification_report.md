# bahia-87y2 Verification Report

## Scope

Focused first pass for Bead `bahia-87y2`: hydrate operator relay policy from service-signed canonical kind `30900` relay-settings state.

## Intended behavior

- Relay policy mutations remain ContextVM kind `25910` acknowledgments.
- Durable relay policy truth is kind `30900` with `d=relay-settings:operator`, `domain=relay-settings`, and `schema=bahia.relay-settings.v1`.
- Backend and Settings UI consume that state with narrow subscriptions and keep subscriptions open after EOSE for live convergence.
- Background/live hydration records canonical state as a synchronized snapshot and does not mutate shared `config.Config`; atomic live relay-pool reconfiguration remains separate (`bahia-2kjh`).
- Settings UI must not overwrite dirty operator inputs from incoming canonical state; it retains newer canonical state as pending until the operator explicitly applies or dismisses it.

## Implementation evidence

- Backend: `internal/controlplane/relay_settings_hydrator.go` subscribes to kind `30900` with service author and relay-settings semantic tags, validates inbound events, enforces trusted author/schema/tags, accepts valid topology-empty canonical states, stores the latest replaceable state in a lock-protected cloned snapshot, and handles EOSE, CLOSED, and AUTH-required closures without mutating shared `config.Config` from the background runner.
- App wiring: `internal/app/app.go` registers the hydrator on control-plane relays when a service pubkey is available.
- Web: `web/src/lib/nostr/relay-settings-controlplane.js` builds the scoped read-model filter, parses trusted canonical state, applies latest replaceable semantics, and exposes a subscription helper using existing relay subscription callbacks.
- UI: `web/src/routes/settings/+page.svelte` hydrates Operator Relay Policy inputs from canonical state only when inputs are clean; if an operator has local edits, incoming state is retained as pending with explicit Apply Canonical State / Keep Local Edits actions. It surfaces catch-up/closed/auth status while leaving Browser Session Relays local-only.
- Docs: `docs/user-guide/nostr-integration.md` documents startup/live canonical `30900` hydration.

## Verification

Local command results in this run:

- PASS: `GOCACHE=/tmp/bahia-go-cache go test ./internal/controlplane -run 'TestRelaySettingsHydrator|TestRelaySettings'`
- PASS: `cd web && npm test -- --run tests/unit/relay-settings-controlplane.test.js`
- PASS: `GOCACHE=/tmp/bahia-go-cache go test ./internal/app -run TestDoesNotExist`
- PASS: `cd web && npm run build`

## Boundaries

- No new relay-routing kinds were introduced.
- No polling, sleep, or timeout-based relay-settings completion logic was added.
- NIP-86 remains represented as authorized target policy and optional relay-owner HTTP administration; this slice does not alter NIP-86 method authorization or secret resolution.
- Live/atomic relay-pool reconfiguration after hydrated policy changes is tracked separately in Bead `bahia-2kjh`; this slice hydrates safe canonical snapshots and UI state without rebuilding already-constructed pools or mutating shared config from the background hydrator.
