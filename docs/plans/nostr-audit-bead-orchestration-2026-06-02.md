# Nostr Audit Bead Orchestration Plan — 2026-06-02

Source audit: `docs/investigations/fake-nostr-routes-audit-2026-06-01.md`.

## Shared taxonomy
- `nostr_native`
- `nostr_request_result_facade`
- `rest_to_nostr_bridge`
- `rest_compatibility`
- `http_native`

## Work Items

### [x] Item 1 — Governance/PSTF route transport matrix
**Beads:** `bahia-a84g`; provide input to `bahia-dg3t`.
**Goal:** Make the route/control-surface classification durable in PSTF and tests.
**Done when:** Matrix covers audit routes/surfaces; signer-first route guard exists or is specified/implemented; Bead has verification evidence.
**Key files:** `docs/investigations/fake-nostr-routes-audit-2026-06-01.md`, `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/*`, `web/src/routes/**`, `web/src/lib/api/client.js`, `web/src/lib/nostr/**`.
**Dependencies:** Starts first.
**Status notes:** Completed by session `AC3826C6-11AF-4661-81E9-BFD2C486697B`. `bahia-a84g` closed. Verification: `npm run test:unit -- --run tests/unit/route-transport-matrix.test.js` passed (4 tests). `bahia-dg3t` left open for broader stale PSTF cleanup.

### [x] Item 2 — Shared request/result lifecycle contract
**Beads:** `bahia-qtoq`.
**Goal:** Standardize public/encrypted/operator Nostr command lifecycle semantics.
**Done when:** Tests prove status cannot satisfy terminal completion, correlation is required, OK/CLOSED/AUTH/zero-accepted failures are explicit, lifecycle contract is documented/PSTF-backed.
**Key files:** `web/src/lib/stores/public-controlplane.svelte.js`, `web/src/lib/nostr/controlplane-requests.js`, `web/src/lib/nostr/dns-controlplane.js`, `web/src/lib/nostr/encrypted-controlplane.js`, `pkg/client/operator_nostr.go`, `internal/controlplane/*_command_publisher.go`, `internal/controlplane/encrypted_transport.go`.
**Dependencies:** Coordinate with Item 1 taxonomy; blocks domain semantic changes in Item 3.
**Status notes:** Completed by session `BBFE3761-8C36-428C-9CEB-58EB2E37DEA2`. `bahia-qtoq` closed. Verification: `npm test -- --run tests/unit/controlplane-requests.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/encrypted-controlplane.test.js` (36 tests passed) and `go test ./pkg/client ./internal/controlplane` passed.

### [x] Item 3 — Domain ingress semantics: orgs and ML
**Beads:** `bahia-sv0j`, `bahia-jxm3`.
**Goal:** Resolve/document org encrypted CRUD facade semantics and migrate ML browser ingress to Nostr-native commands where required.
**Done when:** HITL/PSTF decisions are recorded; UI/docs/tests match chosen classifications; org and ML no longer imply stronger Nostr-native semantics than implemented.
**Key files:** `web/src/lib/stores/orgs.svelte.js`, `internal/controlplane/encrypted_domain_handlers.go`, `internal/controlplane/encrypted_transport.go`, `web/src/routes/ml/+page.svelte`, `web/src/lib/api/client.js`, `internal/api/handlers/ml.go`, `internal/controlplane/ml_command_publisher.go`, relevant PSTF.
**Dependencies:** Item 1 taxonomy and Item 2 lifecycle contract are complete; ready to dispatch.
**Status notes:** Completed by session `0D77D13D-1AEE-424F-8A98-0833D8768AC6`. `bahia-sv0j` and `bahia-jxm3` closed. Verification: `go test ./internal/controlplane ./internal/api/router` passed; `npm run test:unit -- --run tests/unit/route-transport-matrix.test.js` passed (5 tests); Oracle review findings addressed.

### [x] Item 4 — Explicit incomplete EOSE / degraded read-model behavior
**Beads:** `bahia-zui7`.
**Goal:** Prevent incomplete EOSE from becoming normal-looking empty/partial success.
**Done when:** Critical paths fail closed or return explicit degraded metadata; tests inject timeout/abort/CLOSED/partial-event cases; WEB_NOSTR_VALIDATION_EOSE updated.
**Key files:** `web/src/lib/nostr/pool-query.js`, `web/src/lib/nostr/pool-errors.js`, `web/src/lib/nostr/pool-subscriptions.js`, `web/src/lib/nostr/subscriptions.js`, `web/src/lib/nostr/branches.js`, `web/src/lib/nostr/repositories.js`, Soul Factory callers, `pstf/features/WEB_NOSTR_VALIDATION_EOSE/*`.
**Dependencies:** Can run parallel with Item 2; avoid `controlplane-requests.js` / `encrypted-controlplane.js` unless coordinated.
**Status notes:** Completed by session `3212800C-0E24-44A1-A92E-F770C5310797`. `bahia-zui7` closed. Verification: `pnpm --dir web exec vitest run --config vitest.config.js tests/unit/sanity.test.js tests/unit/test-utils-and-fixtures.test.js tests/unit/nostr-client-parsing.test.js tests/unit/repositories-store.test.js tests/unit/souls-store.test.js` — 104 tests passed.

### [x] Item 5 — Single-source Nostr kind constants
**Beads:** `bahia-piy8`.
**Goal:** Eliminate drift-prone manual kind duplicates and add consistency guard.
**Done when:** Canonical catalog/source chosen; frontend/backend values cannot drift; production ad hoc constants are removed or isolated; tests/CI guard exists.
**Key files:** `internal/kinds/kinds.go`, `internal/adapters/nostr/catalog.go`, `internal/adapters/nostr/publisher.go`, `internal/controlplane/ml_command_publisher.go`, `web/src/lib/nostr/kinds.js`, `web/src/lib/nostr/bahia-kinds.js`, generated kind files/scripts if discovered.
**Dependencies:** Inventory can start now; final rewrites coordinate with Items 2/3.
**Status notes:** Completed by session `ADAA484D-9890-42F3-BCE4-2EC14E2744EA`. `bahia-piy8` closed. Verification: `GOCACHE=/tmp/bahia-go-build go test ./internal/kinds ./internal/adapters/nostr ./internal/controlplane` passed; drift guard added in `internal/kinds/generated_drift_test.go`.

### [x] Item 6 — PSTF stale verification cleanup
**Beads:** `bahia-dg3t`.
**Goal:** Correct stale/fake-harness PSTF verification claims.
**Done when:** Verification reports distinguish production vs fake harness evidence; Nostr audit parity wording matches current route matrix/reactor/subscriber ownership; remaining gaps tracked in Beads.
**Key files:** `pstf/features/BAHIA_NOSTR_AUDIT_PARITY/*`, `pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/*`, `pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/*`.
**Dependencies:** Item 1 matrix is complete; ready for broader stale PSTF cleanup.
**Status notes:** Completed by session `F61E99C2-F4E2-419F-AF3B-6620338AF9CA`. `bahia-dg3t` closed. Follow-ups created: `bahia-jicv` (real production HF/vLLM/artifact/gateway/live-relay verification) and `bahia-vn8o` (executable AI/ML signer-first protocol verification). Verification: scoped PSTF JSON validation loop passed.

## Coordination rules
- Each agent must run `bd prime`, claim owned Beads, and update/close with verification evidence.
- Agents must read `AGENTS.md` and follow Nostr/PSTF/Beads closeout requirements.
- Parallel agents must avoid sibling-owned files and report conflicts instead of pushing through.
- Do not bury remaining work in prose; create/update Beads.
- Agents may commit/push their own completed slice only if clean and coordinated; otherwise report exact changes and status.
