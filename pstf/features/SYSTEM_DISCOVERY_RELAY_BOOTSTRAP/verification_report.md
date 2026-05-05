# Verification Report — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Summary
- Verified: `SDRB-AC-001`, `SDRB-AC-002`, `SDRB-AC-003`, `SDRB-AC-004`, `SDRB-AC-006`, `SDRB-AC-007`, `SDRB-AC-010`
- Partially verified: `SDRB-AC-005`, `SDRB-AC-008`, `SDRB-AC-009`
- Current recommendation: do **not** mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` fully verified yet

The highest-risk contract failure is fixed: system discovery no longer exposes raw `nostr.relays`, and the shared browser bootstrap helper no longer treats that field as a valid public bootstrap surface. The handler suite now also proves the approved service-key-backed sidecar-first bootstrap contract. Remaining work is narrower and test-oriented: fail-closed browser negatives, the CLI empty-discovery negative branch, and the multi-consumer encrypted/discovery coherence proof are still missing.

## Commands Run
- `go test ./internal/api/handlers ./pkg/client ./cmd/cli`
- `cd web && npm test -- --run tests/unit/system-store.test.js tests/unit/controlplane-store.test.js tests/unit/stores-index.test.js tests/unit/encrypted-controlplane.test.js tests/unit/api-client-retry-and-edges.test.js`
- `cd web && npm run test:e2e -- tests/e2e/controlplane-nostr-smoke.spec.js`
- `go test ./internal/api/handlers`
- `cd web && npm test -- --run tests/unit/controlplane-store.test.js`

## Acceptance Criteria Status
- `SDRB-AC-001` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` now proves the sidecar-first public bootstrap contract in a service-key-backed configuration, including `browser_relays`, `sidecar_url`, `relay_read_models`, derived `service_pubkey`, and non-reliance on raw `nostr.relays`.
- `SDRB-AC-002` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` covers conditional kind advertisement and explicit legacy false flags.
- `SDRB-AC-003` — **Verified**
  - Evidence: `web/tests/unit/system-store.test.js` proves cache, concurrent dedupe, and force reload.
- `SDRB-AC-004` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` and `web/tests/e2e/controlplane-nostr-smoke.spec.js` prove discovered public bootstrap, EOSE-bounded query, and live subscription handoff.
- `SDRB-AC-005` — **Partially verified**
  - Evidence: connection failure is covered, but required fail-closed branches for missing `relay_read_models` and missing browser bootstrap URLs remain untested.
- `SDRB-AC-006` — **Verified**
  - Evidence: replaceable latest-wins, tombstones, and spoofed-author rejection pass in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-007` — **Verified**
  - Evidence: reconnect/disconnect reactivity passes in `web/tests/unit/controlplane-store.test.js`.
- `SDRB-AC-008` — **Partially verified**
  - Evidence: CLI precedence tests pass, but the required discovery-empty negative branch is still missing.
- `SDRB-AC-009` — **Partially verified**
  - Evidence: encrypted transport helper tests prove some separation from public bootstrap, but the approved minimal capability-gating contract is not yet fully asserted and there is no multi-consumer coherence test.
- `SDRB-AC-010` — **Verified**
  - Evidence: `internal/api/handlers/system.go` no longer emits raw `nostr.relays`; `web/src/lib/stores/controlplane.svelte.js` no longer normalizes that fallback; the updated handler and store tests explicitly prove raw `nostr.relays` is not accepted as the approved bootstrap path.

## Test Matrix Status
- Passing tests: `8`
  - `SDRB-T-001`, `SDRB-T-002`, `SDRB-T-003`, `SDRB-T-004`, `SDRB-T-005`, `SDRB-T-007`, `SDRB-T-008`, `SDRB-T-012`
- Not implemented / incomplete proof: `4`
  - `SDRB-T-006`, `SDRB-T-009`, `SDRB-T-010`, `SDRB-T-011`

## Defects
- `SDRB-D-001` verified — raw `nostr.relays` is no longer exposed or normalized for approved browser bootstrap
- `SDRB-D-002` verified — sidecar-first public bootstrap handler coverage now includes the approved service-key-backed success case
- `SDRB-D-003` open — fail-closed browser bootstrap negatives are untested
- `SDRB-D-004` open — operator discovery fallback lacks the required empty-discovery negative case
- `SDRB-D-005` open — encrypted capability gating and multi-consumer system-info coherence remain under-proven

## Ambiguities / Human Decisions Needed
- No new product-intent ambiguity was discovered during this patch.
- Existing HITL decisions remain sufficient.
- The remaining blockers are evidence gaps, not specification conflicts.

## Confidence Assessment
- Confidence is **medium to high** for the sidecar-first browser bootstrap contract after removal of raw `nostr.relays` exposure and the new handler/store regression coverage.
- Confidence is still **medium** for the full slice because three must-level ACs remain only partially verified due to missing negative-path and cross-consumer proof.

## Recommendation
- Do **not** mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` fully verified yet.
- Next patch should add the remaining proof for:
  - `SDRB-T-006`
  - `SDRB-T-009`
  - `SDRB-T-010`
  - `SDRB-T-011`
- After those land, rerun PSTF verification for the slice.
