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
