# Verification Report — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Summary
- Verified: `SDRB-AC-001`, `SDRB-AC-002`, `SDRB-AC-003`, `SDRB-AC-004`, `SDRB-AC-005`, `SDRB-AC-006`, `SDRB-AC-007`, `SDRB-AC-008`, `SDRB-AC-009`, `SDRB-AC-010`
- Current recommendation: `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` is verified for the approved sidecar-first slice

The discovery/bootstrap slice now has complete proof across the approved contract. The handler no longer exposes raw `nostr.relays`, the browser bootstrap path fails closed on missing capability or missing relay advertisement, the encrypted helper proves explicit capability gating separate from public bootstrap, and the operator CLI proves both precedence and deterministic discovery-empty failure. One canonical Nostr discovery fixture is now reused across browser and CLI tests to demonstrate shared consumer coherence.

## Commands Run
- `go test ./internal/api/handlers ./pkg/client ./cmd/cli`
- `cd web && npm test -- --run tests/unit/system-store.test.js tests/unit/controlplane-store.test.js tests/unit/stores-index.test.js tests/unit/encrypted-controlplane.test.js tests/unit/api-client-retry-and-edges.test.js`
- `cd web && npm run test:e2e -- tests/e2e/controlplane-nostr-smoke.spec.js`
- `go test ./cmd/cli`
- `cd web && npm test -- --run tests/unit/controlplane-store.test.js tests/unit/encrypted-controlplane.test.js`

## Acceptance Criteria Status
- `SDRB-AC-001` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` proves the sidecar-first public bootstrap contract in a service-key-backed configuration, including `browser_relays`, `sidecar_url`, `relay_read_models`, derived `service_pubkey`, and non-reliance on raw `nostr.relays`.
- `SDRB-AC-002` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` covers conditional kind advertisement and explicit legacy false flags.
- `SDRB-AC-003` — **Verified**
  - Evidence: `web/tests/unit/system-store.test.js` proves cache, concurrent dedupe, and force reload.
- `SDRB-AC-004` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` and `web/tests/e2e/controlplane-nostr-smoke.spec.js` prove discovered public bootstrap, EOSE-bounded query, and live subscription handoff.
- `SDRB-AC-005` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` now covers all required fail-closed branches: missing `relay_read_models`, missing browser bootstrap URLs, and unreachable advertised relays.
- `SDRB-AC-006` — **Verified**
  - Evidence: replaceable latest-wins, tombstones, and spoofed-author rejection pass in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-007` — **Verified**
  - Evidence: reconnect/disconnect reactivity passes in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-008` — **Verified**
  - Evidence: `cmd/cli/operator_nostr_test.go` proves precedence for explicit flags, environment variables, canonical system discovery fallback, and deterministic failure when system discovery advertises no browser bootstrap URLs.
- `SDRB-AC-009` — **Verified**
  - Evidence: `web/tests/unit/encrypted-controlplane.test.js` proves public bootstrap metadata alone does not imply encrypted capability, while the explicit encrypted indicators in the canonical fixture enable the encrypted path separately.
- `SDRB-AC-010` — **Verified**
  - Evidence: `internal/adapters/nostr/projector.go` no longer emits raw `nostr.relays`; `web/src/lib/stores/controlplane.svelte.js` no longer normalizes that fallback; the updated handler and store tests explicitly prove raw `nostr.relays` is not accepted as the approved bootstrap path.

## Test Matrix Status
- Passing tests: `12`
  - `SDRB-T-001`, `SDRB-T-002`, `SDRB-T-003`, `SDRB-T-004`, `SDRB-T-005`, `SDRB-T-006`, `SDRB-T-007`, `SDRB-T-008`, `SDRB-T-009`, `SDRB-T-010`, `SDRB-T-011`, `SDRB-T-012`
- Not implemented / incomplete proof: `0`
- Blocked: `0`

## Defects
- `SDRB-D-001` verified — raw `nostr.relays` is no longer exposed or normalized for approved browser bootstrap
- `SDRB-D-002` verified — sidecar-first public bootstrap handler coverage includes the approved service-key-backed success case
- `SDRB-D-003` verified — fail-closed browser bootstrap negatives are covered
- `SDRB-D-004` verified — operator discovery fallback covers the required empty-discovery negative case
- `SDRB-D-005` verified — encrypted capability gating and multi-consumer Nostr discovery coherence are proven against the approved contract

## Ambiguities / Human Decisions Needed
- No new product-intent ambiguity was discovered.
- Existing HITL decisions remain sufficient.
- No additional human decision is needed for this verification cycle.

## Confidence Assessment
- Confidence is **high** for the approved sidecar-first discovery/bootstrap slice.
- The remaining noise in the E2E run is limited to unrelated Vite proxy `ECONNREFUSED` warnings for REST endpoints; the relay-backed discovery assertions still pass and do not rely on REST fallback for this slice.

## Recommendation
- Mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` verified for the approved sidecar-first slice.
- Next PSTF stage should be confidence scoring, then critic review / HITL release review for this feature.
