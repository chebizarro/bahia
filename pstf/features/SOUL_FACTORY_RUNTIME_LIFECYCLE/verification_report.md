# Verification Report — SOUL_FACTORY_RUNTIME_LIFECYCLE

## Summary

Final verification for Beads issue `bahia-i0rk.3` is **not complete**. Bahia and Metiq/Swarmstr evidence verified on 2026-05-15, but the OpenClaw portion and the full cross-runtime vertical slice are blocked by a product ownership decision.

Important correction: previous blocker `bahia-5558` was filed from the wrong OpenClaw root (`/Users/bizarro/Documents/Projects/openclaw-nostr`). The loaded workspace OpenClaw root is `/Users/bizarro/Documents/Dev/openclaw`, and it contains the expected local commits and files. However, per user direction on 2026-05-15, direct upstream OpenClaw modifications are not viable product-completion evidence because OpenClaw is not maintained by this project and is unlikely to accept this bridge/runtime PR shape. The real blocker is now tracked as `bahia-nrjg`: decide maintained fork vs separate adapter/sidecar vs dropping OpenClaw from the first vertical slice.

## Artifact status

- `feature_spec.json` — updated to `final_verification_blocked_by_openclaw_ownership_decision`
- `acceptance_criteria.json` — updated to `final_verification_blocked_by_openclaw_ownership_decision`
- `test_matrix.json` — updated with Bahia/Metiq passing evidence and OpenClaw blocked statuses
- `defects.json` — updated with `SFRL-D-004` / `bahia-nrjg` ownership blocker
- `hitl_decisions.md` — updated with OpenClaw ownership HITL decision
- `docs/soulfactory-runtime-control.md` — remains the shared runtime-control contract

## Acceptance Criteria Status

| AC ID | Status | Basis |
| --- | --- | --- |
| SFRL-AC-001 | Partially verified; final audit still planned | Bahia and Metiq inspected paths use signed Nostr events for runtime control. A final static no-REST audit must be rerun against the chosen OpenClaw ownership shape. |
| SFRL-AC-002 | Verified for Bahia relay bus | `go test ./internal/soulfactory/...` passed. `relay_bus_test.go` covers OK accepted/false/all-relay reject, EOSE realtime transition, CLOSED auth-required reissue, duplicate EVENT dedupe, and reconnect/reissue. |
| SFRL-AC-003 | Verified for Bahia provisioning/read-model orchestration | `go test ./internal/soulfactory/...` and `go test ./...` passed. Bahia tests cover exact `31952` draft capture, spec-hash propagation/mismatch rejection, replay short-circuiting, runtime binding/capability/deploy fields, and final `31951` ordering. |
| SFRL-AC-004 | Artifact-ready; shared fixture suite still partial | Contract document exists and Bahia/Metiq tests exercise the envelope shape. Dedicated reusable Go/TypeScript fixtures remain planned after OpenClaw ownership is decided. |
| SFRL-AC-005 | Verified for Bahia + Metiq; OpenClaw blocked | Bahia adapter/web tests and Metiq capability tests passed. Local OpenClaw root contains bridge capability code, but it cannot count as product-completion evidence until `bahia-nrjg` is resolved. |
| SFRL-AC-006 | Verified for Bahia request construction/result acceptance and Metiq runtime-side validation/idempotency; OpenClaw blocked | Bahia runtime adapter tests passed; Metiq bridge tests passed. OpenClaw runtime-side validation/execution is blocked by ownership decision. |
| SFRL-AC-007 | Verified for Bahia lifecycle semantics | Bahia tests passed for canonical `6950/7950` lifecycle results, migration alias behavior, replay idempotency, and no timeout/CLOSED terminal inference in web store tests. |
| SFRL-AC-008 | Blocked | Metiq bridge unit/integration evidence passed and Bahia is prepared, but the full OpenClaw + Metiq vertical slice cannot be closed while OpenClaw ownership is unresolved (`bahia-nrjg`). |

## Verification evidence from this final pass

### Bahia

- `go test ./internal/soulfactory/...` — passed on 2026-05-15.
- `go test ./...` — passed on 2026-05-15.
- `npm run test:unit -- --run tests/unit/souls-page.test.js tests/unit/souls-store.test.js tests/unit/nostr-client-parsing.test.js` from `web/` — passed on 2026-05-15 (`3` files, `81` tests).
- `npm run build` from `web/` — passed on 2026-05-15. Existing unrelated warnings remained in `src/routes/policies/+page.svelte` and `src/routes/settings/+page.svelte`.

Observed Bahia evidence includes:

- Relay bus protocol handling in `internal/soulfactory/relay_bus_test.go`.
- Runtime capability discovery and `38384`/`38386` correlation in `internal/soulfactory/runtime_adapter_test.go`.
- Draft-backed provisioning/read-model ordering in `internal/soulfactory/reactor_test.go`.
- Lifecycle `1950 -> 6950/7950` compatibility in `internal/soulfactory/lifecycle_handler_test.go`.
- Souls UI capability-gated draft/provision/lifecycle tracking in `web/tests/unit/souls-page.test.js`, `web/tests/unit/souls-store.test.js`, and `web/tests/unit/nostr-client-parsing.test.js`.

### Metiq / Swarmstr

- `go test ./internal/nostr/runtime` from `/Users/bizarro/Documents/Projects/swarmstr` — passed on 2026-05-15.
- `go test ./cmd/metiqd -run 'SoulFactory|CapabilityAnnouncement'` from `/Users/bizarro/Documents/Projects/swarmstr` — passed on 2026-05-15.

Observed Metiq evidence includes:

- `internal/nostr/runtime/capability.go` and `capability_test.go` build/parse SoulFactory `30317` capability content with `soulfactory-runtime-capability/v1`, `soulfactory-runtime-control/v1`, methods, controller pubkeys, and relay hints.
- `cmd/metiqd/soulfactory_bridge.go` validates `soulfactory.*` `38384` envelopes, required tags, controller trust, schema/method/idempotency consistency, target runtime, spec hash, and method params before side effects.
- `cmd/metiqd/soulfactory_bridge.go` executes `soulfactory.provision`, `update`, `suspend`, `resume`, `redeploy`, and `revoke`, and returns documented result envelopes.
- `cmd/metiqd/soulfactory_bridge_test.go` covers provision contract envelope, all lifecycle methods, spec hash mismatch, exact replay without side effects, idempotency conflict, missing params, and capability controller advertisement.

### OpenClaw

Observed but **not accepted as completion evidence**:

- Correct root: `/Users/bizarro/Documents/Dev/openclaw`.
- Commits exist:
  - `b2865dd279` — `feat(nostr): add SoulFactory capability bridge`
  - `5988fb4f7e` — `Implement OpenClaw SoulFactory runtime execution`
- Files exist:
  - `extensions/nostr/src/soulfactory-bridge.ts`
  - `extensions/nostr/src/soulfactory-execution.ts`
  - `extensions/nostr/src/soulfactory-bridge.test.ts`
  - `extensions/nostr/src/soulfactory-execution.test.ts`

These local OpenClaw files show a plausible `30317` capability bridge, `38384` validation, `38386` results, and runtime execution dispatch. They are blocked from product acceptance because direct upstream OpenClaw maintenance is non-viable without a decision to maintain a fork, maintain a separate adapter/sidecar, or remove OpenClaw from the first vertical slice.

A local OpenClaw focused test attempt failed before execution due sandboxed Vite temp-file permissions under `node_modules/.vite-temp`; it was not rerun with escalation because OpenClaw is no longer valid completion evidence for this verification pass.

## Beads updates

- Created `bahia-nrjg` — OpenClaw SoulFactory runtime ownership decision blocker.
- Closed `bahia-5558` as invalid/wrong-root; superseded by `bahia-nrjg`.
- Added dependency: `bahia-i0rk.3` depends on `bahia-nrjg`.
- Left `bahia-i0rk.3` open/in progress; do not close until OpenClaw ownership is resolved or acceptance criteria are changed.

## Open dependencies / risks

- `SFRL-D-004` / `bahia-nrjg` is the blocking product decision for final vertical-slice completion.
- `SFRL-D-002` remains a schema drift risk until shared cross-language fixtures are formalized for the chosen runtime shape.
- `SFRL-D-003` remains a no-REST audit risk for the final ownership/deployment shape.
