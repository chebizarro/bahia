# BAHIA_NOSTR_AUDIT_PARITY verification

## Scope

Work item A for Beads `bahia-s6qp` and `bahia-5dlr`.

## Observed changes

- Expanded `DefaultInboundKinds` to cover the canonical control-plane request namespace: `5961-5968`, `5971-5976`, `5978-5979`, `5981-5989`, `5991-5996`, `38390-38394`, `7977`, and assistant prompt/approval request kinds.
- Added scoped subscriber author configuration for default, adoption, and direct-runtime operator pubkeys.
- Split generic subscriber filters into open, default control-plane, direct-runtime action, and adoption request filters.
- Wired app subscriber author scopes without folding adoption or direct-runtime-only pubkeys into the default scope.
- Wired `controlplane.WithNostrEventRepository` independently of package registry feature enablement.

## Verification

- `go test ./internal/adapters/nostr ./internal/app` — passed.

## Remaining issue scope

No remaining work is known for `bahia-s6qp` or `bahia-5dlr` in the touched scope.

## Bead bahia-piy8 — Single-source Nostr kind constants

### Observed changes

- Canonicalized backend Nostr kind constants through `internal/kinds/kinds.go` aliases in adapter catalog/publisher paths and control-plane DNS/backup/ML paths.
- Added deprecated `31100-31105` legacy command values to the canonical catalog and generated frontend constants so compatibility traffic is still tracked from the single source.
- Added `TestGeneratedFrontendKindsMatchCanonicalGoKinds`, which parses `internal/kinds/kinds.go` and `web/src/lib/nostr/kinds.gen.js` and fails on missing or mismatched generated constants.

### Verification

- `GOCACHE=/tmp/bahia-go-build go test ./internal/kinds ./internal/adapters/nostr ./internal/controlplane` — passed.
- Targeted production-scope literal scan for focused files found no Bahia catalog kind literals outside `web/src/lib/nostr/kinds.gen.js`. `web/src/lib/nostr/encrypted-controlplane.js` remains sibling-owned by the lifecycle agent and was not changed in this slice.

### Remaining issue scope

No remaining work is known for `bahia-piy8` in the touched scope.

## Bead bahia-a84g — Governance/PSTF route transport matrix

### Observed changes

- Added `route_transport_matrix.json` as the durable PSTF artifact for route/control-surface classification using the orchestration taxonomy: `nostr_native`, `nostr_request_result_facade`, `rest_to_nostr_bridge`, `rest_compatibility`, and `http_native`.
- Classified audited browser routes and backend/API control surfaces with repository-backed evidence from `docs/investigations/fake-nostr-routes-audit-2026-06-01.md`.
- Explicitly allowlisted the only route files that currently import REST/API helpers: artifact Blossom/SBOM HTTP surfaces. ML is now classified as Nostr-native by `bahia-gkg7`; continuity is Nostr-native by `bahia-giw6`.
- Added `web/tests/unit/route-transport-matrix.test.js`, a static guard that fails if pure signer-first route files import REST/API clients for command ingress, if a new route-level `$lib/api/*` import lacks exact matrix classification, if a matrix route path does not exist, or if a live SvelteKit `+page` route/loader lacks matrix coverage.
- Updated user-facing Nostr integration documentation and the investigation report to point operators and agents at the PSTF matrix instead of relying on transient audit prose.

### Verification

- `npm run test:unit -- --run tests/unit/route-transport-matrix.test.js` — passed.

### Remaining issue scope

- `bahia-dg3t` stale-verification cleanup is recorded below; AI Fabric fake/no-network harness evidence is now explicitly labeled non-production.
- Route-semantic follow-ups remain owned by sibling beads: org encrypted CRUD facade (`bahia-sv0j`), ML signer-first versus REST compatibility policy (`bahia-jxm3`), request/result lifecycle (`bahia-qtoq`), incomplete EOSE degraded read models (`bahia-zui7`), and kind constants (`bahia-piy8`).

## Bead bahia-wbgi — Canonical resource E2E fixtures

### Observed changes

- Updated `web/tests/e2e/environments-crud-smoke.spec.js` to seed canonical relay read models instead of REST CRUD mocks: environment registry, service registry, service state, and deployment intent rows are all canonical `30900` observables with `domain`, `schema`, `d`, and resource tags.
- Environment create/update/delete assertions now inspect encrypted ContextVM kind `25910` request transport, relay `OK`, correlated result events, and canonical `30900` projections instead of legacy REST `POST`/`PUT`/`DELETE` calls.
- Extended the public E2E relay harness to project environment create/update/delete and service-state read models through event-driven subscriptions, without sleeps or broad local filtering.

### Verification

- `cd web && pnpm test:e2e --reporter=line tests/e2e/environments-crud-smoke.spec.js tests/e2e/sbom-workflow.spec.js` — passed, 14 passed.
- Initial sandboxed Playwright run failed before test logic with macOS Chromium Mach-port permission errors; the verification command passed when rerun outside the sandbox.

### Remaining issue scope

No remaining work is known for `bahia-wbgi` in the `/environments` route transport scope.

## Bead bahia-1ywi — Relay sidecar HTTP surface inventory

### Observed changes

- Updated `docs/investigations/rest-api-audit-2026-06-01.md` so the relay sidecar is listed with permanent HTTP-native surfaces and separately classified as Nostr relay infrastructure rather than REST CRUD.
- Updated `docs/user-guide/nostr-integration.md` and `docs/relay-sidecar.md` to name the sidecar as a standalone HTTP/NIP-11/WebSocket Khatru server started by `cmd/relay/main.go` and served by `internal/relaysidecar/server.go`.
- Added operator references for `nostr.sidecar.*`, `BAHIA_NOSTR__SIDECAR__*`, `listen_addr`, `public_url`, `backend_url`, and `/relay` proxy exposure.
- Added a `relay-sidecar-http-websocket` PSTF matrix entry with `http_native` classification and no route files, preserving sibling ownership of `/settings/profile` route files.

### Verification

- `python3 -m json.tool pstf/features/BAHIA_NOSTR_AUDIT_PARITY/{acceptance_criteria,test_matrix,feature_spec,route_transport_matrix,defects}.json` — passed.
- Documentation review confirmed the three requested docs explicitly distinguish relay sidecar HTTP/NIP-11/WebSocket traffic from REST CRUD and include file/config/operator references.
- `npm run test:unit -- --run tests/unit/route-transport-matrix.test.js` — failed because `web/src/routes/settings/relays/+page.svelte` is missing from the route transport matrix. This is tracked as defect `D-ROUTE-MATRIX-SETTINGS-RELAYS-001` / Bead `bahia-4rwk` and is separate from the relay sidecar documentation acceptance criteria.

### Remaining issue scope

No remaining work is known for `bahia-1ywi` in the touched documentation/PSTF scope. Separate PSTF matrix route coverage work is tracked by `bahia-4rwk`.

## Bead bahia-4rwk — Settings route matrix classification

### Observed changes

- Added `settings-relay-policy-controlplane` to `route_transport_matrix.json` for `web/src/routes/settings/relays/+page.svelte`, backed by `RelaySettingsPanel`, `relay-settings-controlplane.js`, and backend relay-settings handlers/hydrator evidence.
- Recorded the route as `nostr_request_result_facade`: relay policy/admin mutations use encrypted ContextVM request/result operations, while durable relay-policy truth arrives through scoped canonical `30900` subscriptions with EOSE/CLOSED/AUTH callbacks.
- Added PSTF-only classification for concurrently added `web/src/routes/settings/profile/+page.svelte` because the same route matrix guard requires every live `+page` route to be classified. No profile route source files were edited.

### Verification

- `python3 -m json.tool pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json` — passed.
- `npm --prefix web run test:unit -- --run tests/unit/route-transport-matrix.test.js` — passed, 1 file / 5 tests.

### Remaining issue scope

No remaining work is known for `bahia-4rwk` in the PSTF route transport matrix scope.

## Bead bahia-dg3t — PSTF stale verification cleanup

### Observed changes

- Reused `route_transport_matrix.json` as the current source of route/control-surface classification instead of redoing route classification.
- Confirmed this PSTF feature should describe route audit parity as matrix-backed current audit evidence, not as a blanket claim that every route or backend compatibility endpoint is signer-first Nostr-native.
- Updated AI Fabric PSTF reports so fake/no-network harness evidence is labeled non-production and cannot be read as live Hugging Face, vLLM/GPU, gateway, artifact storage, or relay readiness evidence.
- Added defect mappings for remaining AI Fabric production/protocol verification gaps: `D-HFV-PROD-001` → `bahia-jicv` and `D-SFP-EXEC-001` → `bahia-vn8o`.
- Left org/ML route behavior policy unchanged because `bahia-sv0j` and `bahia-jxm3` are sibling-owned.

### Verification

- `bd prime` — completed.
- `bd update bahia-dg3t --claim` — completed.
- `bd search "AI fabric production"`, `bd search "Hugging Face vLLM real"`, `bd search "signer-first AI ML"`, `bd search "HF vLLM"`, `bd search "AI_FABRIC_SIGNER_FIRST_PROTOCOL"`, `bd search "AI_FABRIC_HF_VLLM_DEPLOYMENT"` — no duplicate gap Beads found before creating `bahia-jicv` and `bahia-vn8o`.
- `for f in pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/{acceptance_criteria,test_matrix,defects,feature_spec}.json pstf/features/AI_FABRIC_SIGNER_FIRST_PROTOCOL/{acceptance_criteria,test_matrix,defects,feature_spec}.json pstf/features/BAHIA_NOSTR_AUDIT_PARITY/route_transport_matrix.json; do python3 -m json.tool "$f" >/dev/null || exit 1; done` — passed.

### Remaining issue scope

- `bahia-jicv` tracks real production HF/vLLM/artifact/gateway/live-relay verification for `AI_FABRIC_HF_VLLM_DEPLOYMENT`.
- `bahia-vn8o` tracks executable signer-first AI/ML protocol verification for `AI_FABRIC_SIGNER_FIRST_PROTOCOL`.
- `bahia-sv0j` and `bahia-jxm3` retain ownership of org/ML ingress semantics; this cleanup did not decide product policy for those surfaces.

## Bead bahia-eqku — Continuity sidecar readable-kind policy

### Observed behavior

- Browser console logs showed `/continuity` subscriptions closing with `blocked: event kind 30351 is not readable from the Bahia sidecar`.
- Repository evidence showed `web/src/lib/nostr/continuity.ts` requests continuity status `30351`, recovery progress `30353`, and continuity definitions `31400`-`31404`, while `internal/service/continuity_status_projector.go` publishes `30351`-`30353` continuity projections.
- `internal/kinds/policy.go` did not classify continuity fabric kinds as canonical readable observables, so `internal/relaysidecar/policy.go` rejected otherwise scoped continuity REQ filters.

### Intended behavior

Continuity fabric observables `30350`-`30353` and definitions `31400`-`31404` are production-readable Nostr-native route inputs. The sidecar must admit kind-scoped REQ filters for those kinds while continuing to reject legacy signer-first status/result/read-model families.

### Verification

- `GOCACHE=/tmp/bahia-go-cache go test ./internal/kinds ./internal/relaysidecar` — passed.
- `internal/kinds/policy_test.go` covers continuity fabric kinds as canonical projection/readable kinds.
- `internal/relaysidecar/server_test.go` covers sidecar REQ admission for continuity fabric kinds and continued rejection of legacy signer-first status/result/read-model kinds.
- `npm --prefix web run test:unit -- --run tests/unit/route-transport-matrix.test.js` — passed after removing stale REST import allowlist evidence for `web/src/routes/artifacts/[id]/+page.svelte`; the route no longer imports `$lib/api/client.js`.

### Remaining issue scope

No remaining work is known for Bead `bahia-eqku`. A separate discovered heartbeat constant mismatch is tracked by Bead `bahia-i89o`.
