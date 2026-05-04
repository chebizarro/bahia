# Bahia Product Map

## Product summary

Bahia is a deployment and runtime control plane with a Nostr-native control surface.

At the core, Bahia tracks services, builds, artifacts, deployment intents/runs, runtime observations, and drift. The current product shape is sidecar-first, signer-first, and relay-read-model-first for shared UI state, with encrypted Nostr request/result flows for sensitive browser operations and signer-first operator flows for adoption/import and direct runtime actions.

**Primary evidence**
- `internal/domain/models.go`
- `internal/api/handlers/system.go`
- `docs/control-planes.md`
- `web/src/lib/stores/controlplane.svelte.js`

## User types / actors

| Actor | Role | Confidence |
|---|---|---|
| Web user / signer | Uses the browser UI, signs control-plane events, reads relay-backed state | high |
| Operator | Runs signer-first adoption/import and direct-runtime actions, optionally via compatibility HTTP fallback | high |
| Bahia service | Publishes system discovery metadata, read models, status/results, and encrypted results | high |
| CI dispatcher / Hive-CI publisher | Emits build/artifact/workflow events into Bahia’s pipeline | medium |
| Runtime target | Docker / Compose / Kubernetes / Podman destination for deployments or adoption | high |
| LLM operator | Manages LLM routes, releases, deploys, approvals, rollbacks | high |
| Soul provisioner | Creates and manages Soul Factory agents via Nostr-native flows | medium |

## Major capabilities

| Capability | Description | Main implementation areas | Confidence |
|---|---|---|---|
| Core deployment registry | Tracks services, environments, builds, artifacts, deployment intents, deployment runs, runtime observations, and desired/observed state | `internal/domain/models.go`, `internal/api/handlers/services.go`, `internal/api/handlers/deployments.go` | high |
| Relay read-model UI | Loads and maintains shared UI state from relay read models, using EOSE bootstrap and live subscriptions | `web/src/lib/stores/controlplane.svelte.js`, `web/src/lib/stores/index.svelte.js` | high |
| System discovery / feature gating | `/api/v1/system/info` advertises relays, service pubkey, registries, feature flags, and control-plane kind maps | `internal/api/handlers/system.go`, `web/src/lib/stores/system.svelte.js` | high |
| Public signer-first writes | Publishes signed public Nostr request events and waits for correlated result events | `web/src/lib/nostr/controlplane-requests.js`, `web/src/lib/stores/public-controlplane.svelte.js` | high |
| Encrypted browser operations | Sensitive flows over encrypted Nostr request/result kinds `5980/7980` | `internal/controlplane/encrypted_transport.go`, `internal/controlplane/encrypted_domain_handlers.go`, `internal/controlplane/encrypted_route_handlers.go`, `web/src/lib/nostr/encrypted-controlplane.js` | high |
| Notifications | Channel CRUD/test/log retrieval via encrypted transport | `web/src/lib/stores/notifications.svelte.js`, `web/src/routes/notifications/+page.svelte`, `internal/controlplane/notification_encrypted_handlers.go` | high |
| Payments | REST estimate/run-cost plus encrypted payment history in the current web UI | `internal/api/handlers/payments.go`, `web/src/lib/stores/payments.svelte.js`, `web/src/routes/payments/+page.svelte` | medium |
| Adoption / import | Signer-first scan/import of existing workloads on managed runtime endpoints | `internal/api/handlers/adoption.go`, `internal/controlplane/operator_actions.go`, `docs/adoption-production-rollout.md` | high |
| Direct runtime actions | Signer-first deploy/restart/stop for direct-runtime workloads | `internal/controlplane/operator_actions.go`, `internal/api/router/router.go` | high |
| LLM control plane | Route/release/intent/run/state APIs and relay-backed read models | `internal/domain/llm.go`, `internal/api/handlers/llm.go`, `web/src/lib/stores/controlplane.svelte.js` | high |
| Soul Factory | Relay-backed soul/template/provisioning/action flows and Souls UI | `internal/domain/soul.go`, `web/src/lib/stores/souls.svelte.js`, `web/src/routes/souls/+page.svelte` | medium |
| OCI registry + CI bridge | Registry integration and Hive-CI ingestion into build/artifact pipeline | `docs/event-spec.md`, `docs/protocol-compatibility.md`, `internal/app/app.go` | medium |

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

Primary source: `internal/domain/models.go`

### LLM control plane
- `LLMRoute`
- `LLMRelease`
- `LLMDeploymentIntent`
- `LLMDeploymentRun`
- `LLMRouteObservation`
- `LLMRouteState`

Primary source: `internal/domain/llm.go`

### Soul Factory
- `SoulTemplate`
- `AgentSoul`
- `SoulDraft`
- `ProvisioningRun`
- `SoulAction`

Primary source: `internal/domain/soul.go`

## External integrations

| Integration | Role | Confidence |
|---|---|---|
| Nostr relays / relay sidecar | Canonical public control-plane and read-model transport | high |
| NIP-07 / NIP-46 signer | Browser signing/encryption identity | high |
| NIP-98 | Direct HTTP auth for REST/MCP compatibility surfaces when enabled | high |
| Hive-CI | Workflow event ingestion | medium |
| OCI registry | Artifact storage and retrieval | medium |
| Blossom | Blob/log storage | medium |
| Runtime adapters | Docker / Compose / Kubernetes / Podman | high |
| Loom | Job-style execution path | medium |
| Cashu | Payment-related domain surface | medium |
| Signet | NIP-46 / bunker-related functionality, especially around soul/agent flows | medium |

## Known flows

### 1. System discovery and relay bootstrap
1. Browser loads `/api/v1/system/info`
2. UI resolves browser relays and service pubkey
3. UI requires `features.relay_read_models`
4. UI queries read models until EOSE
5. UI subscribes live for ongoing state

Main files:
- `internal/api/handlers/system.go`
- `web/src/lib/stores/system.svelte.js`
- `web/src/lib/stores/controlplane.svelte.js`

### 2. Core service/deployment flow
1. User creates or updates a service through signed public Nostr commands
2. Deployment intents are created
3. Approval / rejection / rollback flows produce correlated result events
4. Dashboard and deployment views update via read models and activity events

Main files:
- `internal/api/handlers/services.go`
- `internal/api/handlers/deployments.go`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/lib/nostr/controlplane-requests.js`
- `web/src/routes/services/+page.svelte`
- `web/src/routes/deployments/+page.svelte`

### 3. Encrypted notifications flow
1. UI checks encrypted-request availability from `/api/v1/system/info`
2. Browser publishes encrypted request to configured encrypted-request relays
3. Backend decrypts, authorizes, dispatches operation
4. Backend publishes correlated encrypted result
5. UI updates channel/log state from decrypted payload

Main files:
- `internal/controlplane/encrypted_transport.go`
- `internal/controlplane/notification_encrypted_handlers.go`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `web/src/lib/stores/notifications.svelte.js`

### 4. Adoption/import and direct-runtime operator flow
1. Operator signs public request event
2. Backend validates pubkey authorization and endpoint refs
3. Backend performs scan/import/deploy/restart/stop
4. Backend emits status and terminal result events
5. HTTP fallback remains compatibility-only

Main files:
- `docs/adoption-production-rollout.md`
- `internal/api/handlers/adoption.go`
- `internal/controlplane/operator_actions.go`
- `internal/api/router/router.go`

### 5. Soul Factory relay-backed flow
1. UI queries soul/template events from relays
2. UI subscribes to live soul updates
3. Provisioning runs are tracked from status/result events
4. Lifecycle actions are published as signed soul action events

Main files:
- `internal/domain/soul.go`
- `web/src/lib/stores/souls.svelte.js`
- `web/src/routes/souls/+page.svelte`

## Candidate PSTF feature slices

### Slice 1 — Encrypted Control Plane: notifications + transport contract
Why first:
- strongest doc ↔ code ↔ test alignment
- bounded scope
- exercises system discovery, auth, relay separation, encrypted transport, and result correlation

Primary files:
- `docs/control-planes.md`
- `internal/controlplane/encrypted_transport.go`
- `internal/controlplane/encrypted_domain_handlers.go`
- `internal/controlplane/notification_encrypted_handlers.go`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `web/src/lib/stores/notifications.svelte.js`
- `web/tests/unit/encrypted-controlplane.test.js`
- `web/tests/unit/notifications-store.test.js`

### Slice 2 — Core service-to-deployment flow
Why second:
- central to product identity
- covers core entities and lifecycle
- has unit plus E2E evidence

Primary files:
- `internal/domain/models.go`
- `internal/api/handlers/services.go`
- `internal/api/handlers/deployments.go`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/routes/services/+page.svelte`
- `web/src/routes/deployments/+page.svelte`
- `web/tests/e2e/deployment-workflow-critical.spec.js`

### Slice 3 — Adoption/import and direct-runtime operator flow
Why third:
- strong backend/runbook evidence
- distinct operator surface
- good signer-first operational slice

Primary files:
- `docs/adoption-production-rollout.md`
- `internal/api/handlers/adoption.go`
- `internal/controlplane/operator_actions.go`
- `internal/api/router/router.go`
- `internal/api/handlers/adoption_handlers_test.go`
- `internal/controlplane/reactor_operator_actions_test.go`

## Notes on certainty
- Treat code and `docs/control-planes.md` as primary truth.
- Soul Factory and parts of the CI/OCI story are real but less uniformly documented and tested.
- The current product is not accurately described as only a REST deployment registry.
