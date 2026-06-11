# Verification Report — SYSTEM_DISCOVERY_RELAY_BOOTSTRAP

## Summary
- Verified: `SDRB-AC-001`, `SDRB-AC-002`, `SDRB-AC-003`, `SDRB-AC-004`, `SDRB-AC-005`, `SDRB-AC-006`, `SDRB-AC-007`, `SDRB-AC-008`, `SDRB-AC-009`, `SDRB-AC-010`
- Current recommendation: `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` is verified for the approved sidecar-first slice

The discovery/bootstrap slice now has complete proof across the approved contract. The handler no longer exposes raw `nostr.relays`, the browser bootstrap path fails closed on missing capability or missing relay advertisement, the encrypted helper proves explicit capability gating separate from public bootstrap, and the operator CLI proves both precedence and deterministic discovery-empty failure. One canonical Nostr discovery fixture is now reused across browser and CLI tests to demonstrate shared consumer coherence.

## 2026-06-07 Relay-strategy wording cleanup
- Scope: documentation/PSTF wording only for Beads epic `bahia-8epx.1`.
- Verified that canonical discovery wording now names ContextVM discovery `11316`-`11320` plus NIP-51 relay sets `30002`; legacy `31974` is retained only as historical/migration context.
- Verified that FIPS relay guidance separates public overlay advert relays from sensitive Bahia endpoint/control relays and treats shared public exposure as an explicit deployment decision.
- Verified that the relay-purpose taxonomy documents owner, canonical mechanism, and trust/exposure boundary without adding relay-routing kinds.

## Commands Run
- `python3 -m json.tool pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/acceptance_criteria.json >/tmp/acceptance_criteria.json.check` (2026-06-07 docs/PSTF wording cleanup)
- `python3 -m json.tool pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/feature_spec.json >/tmp/feature_spec.json.check` (2026-06-07 docs/PSTF wording cleanup)
- `python3 -m json.tool pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/test_matrix.json >/tmp/test_matrix.json.check` (2026-06-07 docs/PSTF wording cleanup)
- Grep for the former normative `31974` discovery phrase across the scoped docs/PSTF files (2026-06-07 docs/PSTF wording cleanup; expected no matches).
- `go test ./internal/api/handlers ./pkg/client ./cmd/cli`
- `cd web && npm test -- --run tests/unit/system-store.test.js tests/unit/controlplane-store.test.js tests/unit/stores-index.test.js tests/unit/encrypted-controlplane.test.js tests/unit/api-client-retry-and-edges.test.js`
- `cd web && npm run test:e2e -- tests/e2e/controlplane-nostr-smoke.spec.js`
- `go test ./cmd/cli`
- `cd web && npm test -- --run tests/unit/controlplane-store.test.js tests/unit/encrypted-controlplane.test.js`
- `go test ./internal/api/handlers ./pkg/client ./cmd/cli -coverprofile=/tmp/go-discovery-bootstrap.coverprofile -covermode=atomic`
- `go tool cover -func=/tmp/go-discovery-bootstrap.coverprofile > pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/coverage/go-discovery-bootstrap-summary.txt`
- `cd web && npm test -- --coverage --run tests/unit/system-store.test.js`
- `cd web && pnpm test:unit -- --run tests/unit/discovery-store.test.js`
- `cd web && pnpm exec vitest run --config vitest.config.js tests/unit/controlplane-store.test.js` (2026-06-06: 12 tests passed)
- `cd web && pnpm test:unit` (2026-06-06: 64 files / 482 tests passed)

## Acceptance Criteria Status
- `SDRB-AC-001` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` proves the sidecar-first public bootstrap contract in a service-key-backed configuration, including `browser_relays`, `sidecar_url`, `relay_read_models`, derived `service_pubkey`, and non-reliance on raw `nostr.relays`.
- `SDRB-AC-002` — **Verified**
  - Evidence: `internal/api/handlers/system_test.go` covers conditional kind advertisement and explicit legacy false flags.
- `SDRB-AC-003` — **Verified**
  - Evidence: `web/tests/unit/system-store.test.js` proves cache, concurrent dedupe, and force reload.
- `SDRB-AC-004` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` and `web/tests/e2e/controlplane-nostr-smoke.spec.js` prove discovered public bootstrap, scoped subscription bootstrap, immediate EVENT application, and live transition only after EOSE from every connected bootstrap relay.
- `SDRB-AC-005` — **Verified**
  - Evidence: `web/tests/unit/controlplane-store.test.js` covers shared bootstrap fail-closed branches: missing `relay_read_models`, missing browser bootstrap URLs, and unreachable advertised relays. `web/tests/unit/discovery-store.test.js` additionally proves the discovery store fails closed after EOSE when no trusted system discovery event or browser relay set is delivered.
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
- `SDRB-D-003` verified — fail-closed browser bootstrap negatives are covered, including discovery-store EOSE completion without trusted required events
- `SDRB-D-004` verified — operator discovery fallback covers the required empty-discovery negative case
- `SDRB-D-005` verified — encrypted capability gating and multi-consumer Nostr discovery coherence are proven against the approved contract

## Ambiguities / Human Decisions Needed
- No new product-intent ambiguity was discovered.
- Existing HITL decisions remain sufficient.
- No additional human decision is needed for this verification cycle.

## Confidence Assessment
- Confidence is **high** for the approved sidecar-first discovery/bootstrap slice.
- The 2026-06-06 controlplane-store unit verification proves EOSE-gated bootstrap without sleeps or timeout-based completion by deterministically injecting EVENT and EOSE callbacks before awaiting bootstrap resolution.
- The remaining noise in the E2E run is limited to unrelated Vite proxy `ECONNREFUSED` warnings for REST endpoints; the relay-backed discovery assertions still pass and do not rely on REST fallback for this slice.

## Recommendation
- Mark `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` verified for the approved sidecar-first slice.
- Next PSTF stage should be confidence scoring, then critic review / HITL release review for this feature.

## 2026-06-10 Discovery tag protocol hardening
- Scope: Beads issue `bahia-74yc`.
- Verified that the backend system discovery event builder now uses shared protocol constants/helpers for the required kind `11316` tags: `d=bahia-system-v1`, `schema=bahia.system-discovery.v1`, and `name=Bahia`.
- Verified that NIP-51 relay-set `d` tags for browser, ContextVM, and service relay discovery are centralized alongside the announcement constants because browsers filter on those coordinates.
- Verified that `web/src/lib/stores/discovery.svelte.js` now queries discovery history with a narrow `#d` filter for the exact discovery and relay-set coordinates it accepts, instead of broad-fetching all trusted `11316`/`30002` events.
- Added backend exact-envelope assertions for the discovery announcement tags, relay-set tags, and browser-critical discovery payload structure. Tag drift now breaks the projector contract test rather than being hidden by partial assertions.
- Added opt-in Playwright smoke `web/tests/e2e/system-discovery-real-sidecar-smoke.spec.js`. With `BAHIA_REAL_SIDECAR_SMOKE=1` plus real sidecar bootstrap relays and trusted service pubkeys, it boots the app without browser-side relay mocks and fails if EOSE completes without accepted trusted discovery state.
- Added `SDRB-AC-011` and `SDRB-T-013` to capture discovery tags as compatibility-reviewed protocol.

### Commands Run
- `python3 -m json.tool pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/acceptance_criteria.json >/tmp/sdrb-ac.json.check`
- `python3 -m json.tool pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/test_matrix.json >/tmp/sdrb-tm.json.check`
- `gofmt -w internal/adapters/nostr/discovery_protocol.go internal/adapters/nostr/projector.go internal/adapters/nostr/projector_test.go`
- `go test ./internal/adapters/nostr -run 'TestProjectorPublishesSystemDiscoverySnapshot|TestProjectorSystemDiscovery' -count=1`
- `cd web && pnpm test:unit -- --run tests/unit/discovery-store.test.js` (Vitest command executed the configured unit suite: 66 files / 511 tests passed.)
- `cd web && pnpm exec playwright test tests/e2e/system-discovery-real-sidecar-smoke.spec.js --reporter=line` (1 skipped because `BAHIA_REAL_SIDECAR_SMOKE` was not set in this local environment.)

### Remaining verification
- Real-sidecar smoke must be run in an environment that provides a live Bahia sidecar containing canonical discovery history. This is tracked outside prose in Beads for CI/deployment wiring if such an environment is not present locally.
