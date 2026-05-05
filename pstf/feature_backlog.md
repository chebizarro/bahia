# Bahia PSTF Feature Backlog

Generated: 2026-05-04  
Agent: BacklogExpansionAgent

## Scope and method

Inputs reviewed:
- `pstf/product_map.md`
- `pstf/planning_docs_index.md`
- existing PSTF feature directories under `pstf/features/`
- `pstf/spec_gap_report.md`
- targeted repo docs, handlers, domain models, stores, and tests tied to candidate gaps

Existing PSTF coverage already present:
- `CORE_SERVICE_TO_DEPLOYMENT`
- `ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS`
- `SIGNER_FIRST_OPERATOR_ADOPTION_RUNTIME`

Ranking dimensions:
- **User value** — how directly the slice affects a meaningful user or operator journey
- **Centrality** — how many other flows depend on the capability
- **Risk** — security, data integrity, transport correctness, or concurrency sensitivity
- **Coverage gap** — `unmapped` > `partially_mapped` > `fully_mapped`

## Ranked candidates

| Rank | Feature ID | Title | Coverage | Score | Why it matters now |
|---|---|---|---|---:|---|
| 1 | `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` | System discovery and relay bootstrap contract | partially mapped | 19 | Every relay-backed browser flow depends on `/api/v1/system/info`, relay discovery, EOSE bootstrap, and the live-subscription handoff. |
| 2 | `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL` | Encrypted service secrets CRUD and reveal safety | unmapped | 18 | Sensitive secret flows exist now, with both encrypted transport logic and user-visible reveal-safety behavior. |
| 3 | `LLM_ROUTE_RELEASE_DEPLOYMENT` | LLM route/release/deploy/approval/rollback control plane | unmapped | 18 | High-confidence product capability with its own kinds, reconcile loop, and rollback path, but no PSTF slice. |
| 4 | `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN` | Organization membership, invites, and role management over encrypted transport | unmapped | 18 | Security-sensitive admin flows exist over encrypted transport and are explicitly outside the current notifications slice. |
| 5 | `HIVECI_RESULT_INGESTION_PIPELINE` | Hive-CI workflow run/result ingestion into the build-artifact pipeline | unmapped | 17 | CI/OCI bridge is a major product capability, but proof is thinner than the public control-plane slices. |
| 6 | `PAYMENTS_HISTORY_FILTER_EXPORT` | Encrypted payment history load, filtering, and CSV export | unmapped | 17 | Real payment UX exists now, including export hardening, but it is not spec-mapped. |
| 7 | `ENCRYPTED_DEPLOYMENT_RUN_LOGS` | Encrypted deployment run-log retrieval | partially mapped | 16 | Already used in deployment detail UX and explicitly excluded from the core deployment PSTF slice. |
| 8 | `SOUL_FACTORY_PROVISIONING_TRACKING` | Soul Factory template bootstrap, provisioning tracking, and soul actions | unmapped | 16 | Strong relay-backed store and E2E evidence exists, but lifecycle boundaries remain only partly specified. |

## Detailed candidate notes

### 1) `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
**Why it is a gap**
- Existing slices depend on `/api/v1/system/info`, but none verify it as a first-class product contract.
- `pstf/spec_gap_report.md` separately flags both `/api/v1/system/info` and relay-read-model-first frontend behavior as under-specified.

**Small testable slice**
- `/api/v1/system/info` payload contract
- system-info caching and concurrent-load dedupe
- public relay discovery
- EOSE-bounded bootstrap and live-subscription transition
- fail-closed behavior when relay-read-model capability is absent

**Source evidence**
- `pstf/product_map.md` — known flow: system discovery and relay bootstrap
- `pstf/spec_gap_report.md` — Missing specification #1 and #2
- `internal/api/handlers/system.go:47-126,229-245,256-318`
- `internal/api/handlers/system_test.go`
- `web/src/lib/stores/system.svelte.js:13-45`
- `web/src/lib/stores/controlplane.svelte.js:535-568`
- `web/tests/unit/system-store.test.js`
- `web/tests/unit/controlplane-store.test.js`

### 2) `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`
**Why it is a gap**
- Notifications covers the encrypted transport family, but secrets are a separate sensitive route domain.
- Secrets add user-visible safety requirements that deserve their own acceptance criteria.

**Small testable slice**
- encrypted list/create/update/delete/reveal
- never render plaintext in list views
- explicit reveal-only behavior
- reveal-after-update and clipboard-copy workflow

**Source evidence**
- `pstf/spec_gap_report.md` — Missing specification #3
- `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/feature_spec.json`
- `internal/controlplane/encrypted_route_handlers.go:18-41`
- `internal/controlplane/encrypted_route_handlers_test.go`
- `web/src/lib/stores/service-secrets.svelte.js:10-16,100-126`
- `web/tests/unit/encrypted-route-stores.test.js:35-43`
- `web/tests/e2e/service-secrets-smoke.spec.js:162-190,260-317,319-390`

### 3) `LLM_ROUTE_RELEASE_DEPLOYMENT`
**Why it is a gap**
- Product map marks LLM control plane as a high-confidence capability.
- `/system/info` advertises dedicated LLM kinds and read models.
- Existing PSTF slices do not cover route/release/deploy/rollback flows.

**Small testable slice**
- route creation
- release registration
- deployment intent creation
- approval or rejection
- route-state publication and rollback correlation

**Source evidence**
- `pstf/product_map.md`
- `pstf/spec_gap_report.md` — Untested behavior #3
- `internal/api/handlers/system.go:288-300`
- `docs/control-planes.md:72,96-100,132-133`
- `internal/domain/llm.go:1-140`
- `internal/controlplane/reactor.go:352-361,944-1118`
- `internal/controlplane/reactor_llm_test.go`
- `internal/reconcile/llm_reconciler_test.go`
- `internal/api/handlers/llm.go:28-318`

### 4) `ORG_MEMBERSHIP_INVITES_ENCRYPTED_ADMIN`
**Why it is a gap**
- The current encrypted notifications PSTF slice explicitly excludes org/member/invite behavior.
- Repo evidence shows a real encrypted org admin surface with routes, stores, handlers, and tests.

**Small testable slice**
- org list/detail bootstrap
- create/revoke invite
- accept invite
- update member role
- remove member with RBAC expectations

**Source evidence**
- `pstf/spec_gap_report.md` — Missing specification #3
- `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/feature_spec.json`
- `docs/investigations/payments-orgs-private-transport-2026-05-03.md`
- `internal/controlplane/encrypted_domain_handlers.go:19-38`
- `internal/controlplane/encrypted_domain_handlers_test.go`
- `web/src/lib/stores/orgs.svelte.js:56-58,107-125`
- `web/tests/unit/encrypted-domain-stores.test.js:68-99`
- `web/src/routes/orgs/[id]/+page.svelte:6-12,79-123`

### 5) `HIVECI_RESULT_INGESTION_PIPELINE`
**Why it is a gap**
- CI/OCI bridge is in the product map, but current PSTF coverage does not prove the event ingestion path.
- The spec-gap report calls out thin end-to-end proof.

**Small testable slice**
- subscribe to `5401` and `5402`
- signature and trusted-publisher checks
- orphan result handling
- repository CI lookup state after ingestion

**Source evidence**
- `pstf/product_map.md`
- `pstf/spec_gap_report.md` — Untested behavior #1
- `internal/domain/hiveci.go:23-79,112-114`
- `internal/adapters/hiveci/subscriber.go:17-136,205-284`
- `internal/adapters/hiveci/subscriber_test.go:98-116`
- `internal/api/handlers/repository_ci.go`
- `test/integration/e2e_ci_registry_test.go`

### 6) `PAYMENTS_HISTORY_FILTER_EXPORT`
**Why it is a gap**
- Payments are explicitly present in the product map.
- Current behavior mixes encrypted history retrieval with separate REST-backed summaries.
- Export hardening is real behavior that should be preserved intentionally.

**Small testable slice**
- worker-scoped encrypted history load
- missing-worker fail-closed behavior
- filtering without mutating source data
- CSV escaping and filename stability

**Source evidence**
- `pstf/product_map.md`
- `pstf/spec_gap_report.md` — Risky behavior #3
- `pstf/features/ENCRYPTED_CONTROL_PLANE_NOTIFICATIONS/feature_spec.json`
- `docs/investigations/payments-orgs-private-transport-2026-05-03.md`
- `internal/controlplane/encrypted_domain_handlers.go:19-20`
- `web/src/lib/stores/payments.svelte.js:25-39`
- `web/tests/unit/encrypted-domain-stores.test.js:31-50`
- `web/src/routes/payments/+page.svelte:11-21,126-190`
- `web/tests/unit/payments-list-utils.test.js:55-65`

### 7) `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
**Why it is a gap**
- Core deployment PSTF coverage explicitly excludes encrypted run-log retrieval.
- The notifications slice does not include deployment detail logs.

**Small testable slice**
- encrypted run-log request for a specific run
- tail/stream parameter handling
- only encrypted relays used
- decrypted log rendering on the run detail page

**Source evidence**
- `pstf/features/CORE_SERVICE_TO_DEPLOYMENT/feature_spec.json`
- `internal/controlplane/encrypted_route_handlers.go:18-41`
- `internal/controlplane/encrypted_route_handlers_test.go:342-344`
- `web/src/lib/stores/deployment-run-logs.svelte.js:10-12,39-49`
- `web/tests/e2e/deployment-history-and-run-details.spec.js:177-179,309-311`
- `pstf/spec_gap_report.md`

### 8) `SOUL_FACTORY_PROVISIONING_TRACKING`
**Why it is a gap**
- Soul Factory is a named capability in the product map.
- The spec-gap report says the current product boundary and lifecycle proof are incomplete.
- Strong frontend/store evidence exists now and is enough for a bounded PSTF slice.

**Small testable slice**
- template bootstrap from relays
- provisioning-run correlation across `5950/6950/7950`
- `onEose` and relay-closure behavior
- soul action publish flow (`1950`)

**Source evidence**
- `pstf/product_map.md`
- `pstf/spec_gap_report.md` — Missing specification #5, Untested behavior #4, Risky behavior #4
- `docs/soul-factory.md:19-24`
- `internal/domain/soul.go:15-104`
- `web/src/lib/stores/souls.svelte.js:106-131,174-289,345-391`
- `web/tests/unit/souls-store.test.js:223-300,492-723`
- `web/tests/e2e/soul-signing-smoke.spec.js`

## Recommended promotion set

If only three slices move next, the strongest set is:
1. `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
2. `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`
3. `LLM_ROUTE_RELEASE_DEPLOYMENT`

Reason:
- they cover the highest-centrality bootstrap contract,
- the highest-risk encrypted secret domain not yet spec-mapped,
- and a large high-confidence product vertical with its own kinds/state model.

## 2026-05-04 — HITL policy update after cross-feature reasoning

Two portfolio-level HITL decisions were captured after `cross_feature_analysis` and `coverage_heatmap`:

### Shared transport policy
- Question: How should transport policy be governed across public browser flows, encrypted browser flows, and compatibility-only operator/browser fallback?
- Decision: **Create one dedicated shared transport-policy slice now**
- Backlog impact:
  - add inferred candidate `TRANSPORT_POLICY_GOVERNANCE`
  - raise it near the top of the next promotion set
  - stop treating transport governance as only per-slice documentation debt

### Rollback policy across deployment domains
- Question: How should rollback target-selection and user-visible semantics be handled across core service deployments and LLM deployments?
- Decision: **Keep rollback domain-specific and verify each slice separately**
- Backlog impact:
  - do **not** add a shared rollback-governance slice
  - keep rollback follow-up work under `CORE_SERVICE_TO_DEPLOYMENT` and `LLM_ROUTE_RELEASE_DEPLOYMENT`
  - continue treating LLM rollback as deferred until its domain-specific semantics are approved

## Decision-adjusted priority override

If the next backlog pass is updated for current portfolio reality, the strongest order is now:
1. `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`
2. `TRANSPORT_POLICY_GOVERNANCE`
3. `HIVECI_RESULT_INGESTION_PIPELINE`
4. `ENCRYPTED_DEPLOYMENT_RUN_LOGS`
5. `SOUL_FACTORY_PROVISIONING_TRACKING`
6. `ENCRYPTED_SERVICE_SECRETS_CRUD_REVEAL`

Reason:
- system discovery is still the highest-centrality shared contract,
- transport policy is now explicitly approved as a shared slice rather than remaining implicit cross-feature debt,
- Hive-CI and encrypted deployment run logs are both direct blockers on the deployment product story,
- Soul Factory remains user-promoted,
- encrypted secrets remain a high-risk unmapped encrypted domain but are now behind the shared-policy and deployment-handoff blockers.
- `LLM_ROUTE_RELEASE_DEPLOYMENT` should no longer be treated as an unmapped full-slice candidate; only its deferred rollback follow-up remains open, and rollback stays domain-specific by HITL decision.
