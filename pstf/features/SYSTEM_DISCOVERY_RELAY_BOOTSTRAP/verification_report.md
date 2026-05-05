# Verification Report — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Summary
- Verified: `SDRB-AC-002`, `SDRB-AC-003`, `SDRB-AC-004`, `SDRB-AC-006`, `SDRB-AC-007`
- Partially verified: `SDRB-AC-001`, `SDRB-AC-005`, `SDRB-AC-008`, `SDRB-AC-009`
- Failed: `SDRB-AC-010`
- Current recommendation: do **not** mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` verified yet

The strongest current evidence is in the shared browser store and handler suites: caching, EOSE-bounded bootstrap, replaceable-event handling, reconnect state, and conditional control-plane advertisement all passed. The remaining gaps are mostly missing proof, except for one real contract failure: the implementation still exposes and normalizes raw `nostr.relays` fallback even though the approved sidecar-first slice explicitly rejects that behavior.

## Commands Run
- `go test ./internal/api/handlers ./pkg/client ./cmd/cli`
- `cd web && npm test -- --run tests/unit/system-store.test.js tests/unit/controlplane-store.test.js tests/unit/stores-index.test.js tests/unit/encrypted-controlplane.test.js tests/unit/api-client-retry-and-edges.test.js`
- `cd web && npm run test:e2e -- tests/e2e/controlplane-nostr-smoke.spec.js`

## Acceptance Criteria Status
- `SDRB-AC-001` — **Partially verified**
  - Evidence: sidecar-first handler advertisement is partly covered, but the full approved handler contract remains untested (`SDRB-T-001` not implemented).
- `SDRB-AC-002` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` passed; it covers conditional kind advertisement and explicit legacy false flags.
- `SDRB-AC-003` — **Verified**
  - Evidence: `web/tests/unit/system-store.test.js` passed; it proves cache, concurrent dedupe, and force reload.
- `SDRB-AC-004` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` and `web/tests/e2e/controlplane-nostr-smoke.spec.js` passed; they prove discovered public bootstrap, EOSE-bounded query, and live subscription handoff.
- `SDRB-AC-005` — **Partially verified**
  - Evidence: connection failure is covered and passed, but required fail-closed branches for missing `relay_read_models` and missing browser bootstrap URLs remain untested.
- `SDRB-AC-006` — **Verified**
  - Evidence: replaceable latest-wins, tombstones, and spoofed-author rejection all passed in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-007` — **Verified**
  - Evidence: reconnect/disconnect reactivity passed in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-008` — **Partially verified**
  - Evidence: CLI precedence tests passed, but the required discovery-empty negative branch is missing.
- `SDRB-AC-009` — **Partially verified**
  - Evidence: encrypted transport helper tests passed and prove some separation from public bootstrap, but the approved minimal capability-gating contract is not yet fully asserted and there is no multi-consumer coherence test.
- `SDRB-AC-010` — **Failed**
  - Evidence: code inspection confirms `internal/api/handlers/system.go` still exposes `nostr.relays` when sidecar mode is disabled and `web/src/lib/stores/controlplane.svelte.js` still normalizes that fallback, which conflicts with `HITL-SYSTEM_DISCOVERY_RELAY_BOOTSTRAP-001` and the approved AC.

## Test Matrix Status
- Passing tests: `6`
  - `SDRB-T-002`, `SDRB-T-003`, `SDRB-T-004`, `SDRB-T-005`, `SDRB-T-007`, `SDRB-T-008`
- Not implemented / incomplete proof: `5`
  - `SDRB-T-001`, `SDRB-T-006`, `SDRB-T-009`, `SDRB-T-010`, `SDRB-T-011`
- Blocked: `1`
  - `SDRB-T-012` (blocked by the live raw-`nostr.relays` divergence tracked in `bahia-u6e1`)

## Defects
- `SDRB-D-001` major — system discovery still exposes and normalizes raw `nostr.relays` outside the approved sidecar-first contract
- `SDRB-D-002` minor — sidecar-first public bootstrap handler coverage is incomplete
- `SDRB-D-003` minor — fail-closed browser bootstrap negatives are untested
- `SDRB-D-004` minor — operator discovery fallback lacks the required empty-discovery negative case
- `SDRB-D-005` minor — encrypted capability gating and multi-consumer system-info coherence remain under-proven

## Ambiguities / Human Decisions Needed
- No new product-intent ambiguity was discovered during verification.
- Existing HITL decisions remain sufficient for this stage.
- The current blocker is implementation/test evidence, not a missing approval.

## Confidence Assessment
- Confidence is **medium** for the shared browser bootstrap path because the handler, store, and E2E evidence are strong and passed.
- Confidence is **low to medium** for the full slice as approved because one must-level AC (`SDRB-AC-010`) currently fails and several other must-level ACs still rely on missing or incomplete tests.
- The Playwright run emitted Vite proxy `ECONNREFUSED` warnings for unrelated REST endpoints during page load, but the relay-backed smoke assertions still passed and did not require REST fallback for this slice.

## Recommendation
- Do **not** mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` verified.
- Patch `SDRB-D-001` first by removing or quarantining raw `nostr.relays` exposure/normalization from the approved browser bootstrap path.
- Then add the missing proof for:
  - `SDRB-T-001`
  - `SDRB-T-006`
  - `SDRB-T-009`
  - `SDRB-T-010`
  - `SDRB-T-011`
- After those land, rerun PSTF verification for the slice.
