# Bahia Product Map

## Product summary

Bahia is a deployment and runtime control plane. The stable core is service/build/artifact/deployment tracking plus runtime observation and drift detection (`README.md`, `internal/domain/models.go`).

**Observed current shape (2026-05-05):** Bahia is also a **Nostr-native, sidecar-first, signer-first** control plane. The browser bootstraps from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`, reads shared state from relay-backed read models, performs many writes as signed Nostr request events, and uses encrypted Nostr request/result flows for sensitive domains (`docs/control-planes.md`, `internal/adapters/nostr/projector.go`, `web/src/lib/stores/controlplane.svelte.js`, `web/src/lib/stores/auth.svelte.js`).

**Primary evidence**
- `README.md`
- `docs/control-planes.md`
- `internal/domain/models.go`
- `internal/api/router/router.go`
- `internal/adapters/nostr/projector.go`
- `web/src/lib/stores/controlplane.svelte.js`

## User types / actors

| Actor | Role | Evidence |
|---|---|---|
| Web signer / operator | Uses the web UI, authenticates with NIP-07 or NIP-46, signs control-plane requests | `web/src/lib/stores/auth.svelte.js`, `web/src/routes/` |
| Backend service (Bahia) | Publishes read models, audit events, status/results, encrypted results, and serves REST/MCP compatibility surfaces | `internal/adapters/nostr/projector.go`, `internal/controlplane/`, `internal/mcp/server.go` |
| Deployment operator | Creates deployment intents, approvals, rollbacks, adoption/import requests, and direct runtime actions | `docs/adoption-production-rollout.md`, `internal/controlplane/operator_actions.go` |
| CI / Hive-CI publisher | Emits workflow events that Bahia ingests into builds/artifacts/deployments | `docs/event-spec.md`, `internal/adapters/hiveci/` |
| Runtime target / worker | Executes or hosts workloads (Docker/Compose/Kubernetes/Podman; Loom workers for jobs) | `docs/architecture.md`, `internal/service/runtime_lifecycle.go`, `internal/rollout/` |
| LLM operator | Manages LLM routes, releases, intents, runs, and route state | `internal/domain/llm.go`, `internal/api/handlers/llm.go`, `web/src/routes/llm/+page.svelte` |
| Soul operator | Provisions and manages Souls / Soul Factory resources | `internal/domain/soul.go`, `web/src/routes/souls/`, `docs/soul-factory.md` |

## Major capabilities

| Capability | What it does | Main implementation areas | Test surface |
|---|---|---|---|
| Core registry | CRUD for services, environments, builds, artifacts, deployment intents/runs, observations, state | `internal/domain/models.go`, `internal/api/handlers/`, `internal/service/registry.go` | Go unit tests, DB integration test, web CRUD tests |
| System discovery | Advertises relays, service pubkey, feature flags, kind maps, registries, runtime info | `internal/adapters/nostr/projector.go` | `web/tests/unit/system-store.test.js` |
| Relay-backed shared state | Browser bootstraps via EOSE, then stays live on subscriptions for services, environments, state, workers, activity, LLM models | `web/src/lib/stores/controlplane.svelte.js` | `web/tests/unit/controlplane-store.test.js`, `web/tests/e2e/controlplane-nostr-smoke.spec.js` |
| Public signer-first writes | Signed Nostr requests for service/deployment/policy actions and correlated result handling | `web/src/lib/stores/public-controlplane.svelte.js`, `web/src/lib/nostr/controlplane-requests.js`, `internal/controlplane/reactor.go` | `web/tests/unit/controlplane-requests.test.js`, public smoke E2E |
| Encrypted control plane | Sensitive request/result flows for notifications, payments, orgs, secrets, run logs, signatures | `internal/controlplane/encrypted_transport.go`, `internal/controlplane/encrypted_domain_handlers.go`, `internal/controlplane/encrypted_route_handlers.go` | Go unit, Vitest, notifications E2E |
| Deployment workflow | Intent creation, approval/rejection, execution, rollback, logs, state updates | `internal/api/handlers/deployments.go`, `internal/api/router/router.go`, `internal/rollout/`, `internal/reconcile/` | Go unit, Playwright deployment specs |
| Adoption / direct runtime | Signer-first scan/import of existing workloads plus direct deploy/restart/stop for adopted services | `internal/api/handlers/adoption.go`, `internal/controlplane/operator_actions.go` | Go handler/reactor tests |
| LLM control plane | Route/release/intent/run/state APIs, Nostr kinds, projections, UI | `internal/domain/llm.go`, `internal/api/handlers/llm.go`, `internal/controlplane/` | Go unit, Vitest, Playwright |
| Notifications | Notification channels, tests, logs; current UI path is encrypted transport | `internal/api/handlers/notification.go`, `internal/controlplane/notification_encrypted_handlers.go`, `web/src/routes/notifications/` | Go unit, Vitest, Playwright |
| Payments | Cost estimate/run cost REST + encrypted payment history in current UI | `internal/api/handlers/payments.go`, `web/src/lib/stores/payments.svelte.js`, `web/src/routes/payments/+page.svelte` | Go unit + store tests |
| Soul Factory | Soul templates, souls, provisioning runs, live status, signing flows | `internal/domain/soul.go`, `internal/soulfactory/`, `web/src/routes/souls/` | Go unit, Vitest, Playwright |
| OCI registry + Hive-CI bridge | OCI distribution API plus ingestion of 5401/5402 workflow events into build/artifact pipeline | `internal/api/handlers/registry.go`, `internal/adapters/hiveci/`, `docs/event-spec.md` | Handler tests; weak true integration coverage |
| Native MCP | JSON-RPC tools at `/mcp` and `/api/v1/mcp` with async correlation metadata | `internal/mcp/server.go`, `internal/api/router/router.go` | `internal/mcp/server_*_test.go` |

## Data entities

### Deployment core
- `Service`
- `Environment`
- `Build`
- `Artifact`
- `DeploymentIntent`
- `DeploymentRun`
- `RuntimeObservation`
- `EnvironmentServiceState`

Source: `internal/domain/models.go`

### LLM control plane
- `LLMRoute`
- `LLMRelease`
- `LLMDeploymentIntent`
- `LLMDeploymentRun`
- `LLMRouteObservation`
- `LLMRouteState`

Source: `internal/domain/llm.go`

### Soul Factory
- `SoulTemplate`
- `AgentSoul`
- `ProvisioningRun`
- `SoulAction`

Source: `internal/domain/soul.go`

### Tenant / encrypted domains
- Organizations, members, invites
- Notification channels and logs
- Service secrets
- Payment records
- Artifact signatures / verification records

Sources: `internal/domain/tenant.go`, `internal/domain/notification.go`, `internal/domain/secret.go`, `internal/domain/payment.go`, `internal/domain/signature.go`

## External integrations

| Integration | Purpose | Evidence |
|---|---|---|
| Nostr relays / relay sidecar | Canonical realtime/read-model transport | `docs/control-planes.md`, `web/src/lib/stores/controlplane.svelte.js` |
| NIP-07 / NIP-46 | Browser signer identity and event signing | `web/src/lib/stores/auth.svelte.js` |
| NIP-98 | Direct HTTP auth for REST/MCP compatibility surfaces | `docs/api.md`, `web/src/lib/stores/auth.svelte.js`, `internal/auth/` |
| Loom workers | Remote execution path for deployment jobs | `README.md`, `docs/event-spec.md`, `internal/workflow/` |
| Docker / Compose / Podman / Kubernetes | Runtime observation and direct runtime lifecycle | `docs/deployment.md`, `internal/service/runtime_lifecycle.go` |
| Hive-CI | CI workflow event ingestion | `docs/event-spec.md`, `internal/adapters/hiveci/` |
| OCI registry | Artifact storage / distribution | `docs/api.md`, `internal/api/handlers/registry.go` |
| Blossom | Blob/log storage | `docs/deployment.md`, `internal/adapters/blossom/` |
| Cashu | Worker payment surface | `internal/service/payments.go`, `internal/adapters/cashu/` |
| Signet | NIP-46 / bunker and soul-related signing support | `internal/adapters/signet/` |

## Known flows

### 1. System discovery and relay bootstrap
1. Browser loads `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.
2. Browser discovers relays, service pubkey, feature flags, and kind maps.
3. Browser connects to relays.
4. Browser queries read models until EOSE.
5. Browser keeps subscriptions open for live updates.

Evidence: `internal/adapters/nostr/projector.go`, `web/src/lib/stores/controlplane.svelte.js`

### 2. Core deployment flow
1. Build/artifact exist or are ingested from CI.
2. User/operator creates deployment intent.
3. Approval/rejection occurs when required.
4. Deployment run executes and updates status/results.
5. Runtime observation updates desired/observed state and drift.

Evidence: `internal/domain/models.go`, `internal/api/handlers/deployments.go`, `internal/api/router/router.go`

### 3. Public signer-first control-plane write flow
1. Browser/operator signs a request event.
2. Event is published to relays.
3. Bahia validates and processes the request.
4. Bahia publishes status and terminal result events.
5. Browser follows replies by correlation tags instead of polling.

Evidence: `docs/control-planes.md`, `web/src/lib/nostr/controlplane-requests.js`, `internal/controlplane/reactor.go`

### 4. Encrypted browser flow
1. Browser discovers encrypted-request capability from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.
2. Browser encrypts a request to Bahia's pubkey and publishes kind `5980`.
3. Bahia decrypts, authorizes, performs the operation, and emits encrypted kind `7980` reply.
4. Browser decrypts the result and updates local state.

Evidence: `docs/control-planes.md`, `internal/controlplane/encrypted_transport.go`, `web/src/lib/nostr/encrypted-controlplane.js`

### 5. Adoption / direct runtime operator flow
1. Operator publishes signer-first scan/import/action request.
2. Bahia validates authorized pubkey and endpoint-ref input.
3. Bahia performs scan/import/deploy/restart/stop.
4. Bahia emits progress and terminal result events.

Evidence: `docs/adoption-production-rollout.md`, `internal/controlplane/operator_actions.go`

### 6. LLM route flow
1. Operator creates route.
2. Operator registers release.
3. Operator creates deploy intent / approval / rollback request.
4. Bahia emits status/results and replaceable route state projections.

Evidence: `internal/domain/llm.go`, `internal/api/handlers/llm.go`, `docs/event-spec.md`

### 7. Soul Factory flow
1. Browser loads soul/template events from relays.
2. Browser subscribes live for soul/provisioning updates.
3. User signs soul actions and provisioning events.
4. UI updates from relay events.

Evidence: `internal/domain/soul.go`, `web/src/lib/stores/souls.svelte.js`, `web/src/routes/souls/+page.svelte`

## Candidate PSTF feature slices

### Slice 1 — Encrypted control plane: notifications transport contract
**Why:** strongest doc ↔ backend ↔ frontend ↔ test alignment; bounded scope; security-sensitive.

**Primary files**
- `docs/control-planes.md`
- `internal/adapters/nostr/projector.go`
- `internal/controlplane/encrypted_transport.go`
- `internal/controlplane/notification_encrypted_handlers.go`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `web/src/lib/stores/notifications.svelte.js`
- `web/tests/unit/encrypted-controlplane.test.js`
- `web/tests/e2e/notifications-encrypted-smoke.spec.js`

### Slice 2 — Core service-to-deployment flow
**Why:** central product identity; covers core domain entities, write path, approvals, run state, logs, and drift.

**Primary files**
- `internal/domain/models.go`
- `internal/api/handlers/services.go`
- `internal/api/handlers/deployments.go`
- `internal/api/router/router.go`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/routes/services/`
- `web/src/routes/deployments/`
- `web/tests/e2e/service-deployment-public-smoke.spec.js`

### Slice 3 — Signer-first adoption / direct runtime operator flow
**Why:** distinct operator behavior with strong backend tests and clear architectural constraints.

**Primary files**
- `docs/adoption-production-rollout.md`
- `internal/api/handlers/adoption.go`
- `internal/controlplane/operator_actions.go`
- `internal/api/router/router.go`
- `internal/api/handlers/adoption_handlers_test.go`
- `internal/controlplane/reactor_operator_actions_test.go`

## Notes on certainty

- Use `internal/domain/*`, `internal/api/router/router.go`, `internal/adapters/nostr/projector.go`, and `docs/control-planes.md` as primary truth.
- Treat `README.md`, `docs/architecture.md`, `docs/protocol-compatibility.md`, `docs/WEB_APP_PRODUCTION_PLAN.md`, and `web/README.md` as lower-authority unless verified against code.
- `observed_not_specified` examples include `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` as a hard bootstrap contract, relay-read-model-first shared state, encrypted payment history in the UI, and relay-backed Souls UI without a REST route.
- `specified_not_observed` examples include adoption as a documented operator surface with no current web route and claimed CI/OCI end-to-end tests that are mostly placeholders.
