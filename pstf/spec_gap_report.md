# Bahia Spec Gap Report

## Scope

This report groups contradictions, omissions, untested behavior, and risky behavior found across selected docs, backend handlers/domain/control-plane code, frontend routes/stores, tests, and `docs/investigations/pstf-discovery-2026-05-03.md`.

Evidence language:
- **high**: direct code and/or test evidence
- **medium**: direct code evidence but lighter tests or docs
- **low**: primarily documentation-level observation

## Missing specification

### 1. `/api/v1/system/info` is a critical contract but is under-documented
The web app depends on `/api/v1/system/info` for relay discovery, service pubkey discovery, encrypted-request availability, feature gating, registry discovery, and control-plane kind discovery.

Evidence:
- `internal/api/handlers/system.go`
- `web/src/lib/stores/system.svelte.js`
- `web/src/lib/stores/controlplane.svelte.js`
- `docs/api.md` (omission)

Assessment: high

### 2. Relay-read-model-first frontend behavior is not clearly specified
The current web app loads shared state from relay read models rather than mainly from REST list endpoints.

Evidence:
- `web/src/lib/stores/controlplane.svelte.js`
- `web/src/lib/stores/index.svelte.js`
- `web/src/routes/+page.svelte`
- `README.md`
- `docs/api.md`

Assessment: high

### 3. Encrypted operation catalog is incomplete in docs
Sensitive encrypted operations are documented unevenly. Selected code proves encrypted support not just for notifications but also payment history and org/member/invite flows.

Evidence:
- `internal/controlplane/encrypted_domain_handlers.go`
- `internal/controlplane/encrypted_route_handlers.go`
- `web/src/lib/stores/payments.svelte.js`
- `docs/control-planes.md`

Assessment: high

### 4. Newer public request kinds are not fully documented
Current web code uses public request kinds beyond the simplified `5961-5968` tables, including additional service/environment/policy/artifact write paths.

Evidence:
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/lib/nostr/controlplane-requests.js`
- `docs/control-planes.md`
- `docs/nostr-commands.md`

Assessment: medium

### 5. Soul Factory product boundary is not clearly specified
Selected evidence supports a real relay-backed Souls UI and provisioning tracking, but the full lifecycle boundary remains unclear.

Evidence:
- `internal/domain/soul.go`
- `web/src/lib/stores/souls.svelte.js`
- `web/src/routes/souls/+page.svelte`
- `docs/soul-factory.md`

Assessment: medium

## Conflicting specification

### 1. NIP-46 status is internally inconsistent
One section calls NIP-46 “stubbed”, while another says the Signet client effectively implements full NIP-46 bunker behavior.

Evidence:
- `docs/protocol-compatibility.md`

Assessment: high

### 2. `311xx` command status conflicts with current control-plane guidance
Compatibility docs still present `31100-31105` as implemented inbound commands, while current control-plane docs treat them as deprecated/quarantined and require canonical `596x` for new integrations.

Evidence:
- `docs/protocol-compatibility.md`
- `docs/control-planes.md`
- `docs/nostr-commands.md`

Assessment: high

### 3. Product framing is older than implementation reality
Older docs mostly describe Bahia as a REST deployment registry. Current frontend and control-plane code show a relay-read-model-first, signer-first, partly encrypted control plane.

Evidence:
- `README.md`
- `docs/architecture.md`
- `docs/control-planes.md`
- `web/src/lib/stores/controlplane.svelte.js`

Assessment: high

### 4. Soul lifecycle status conflicts across docs
`docs/soul-factory.md` reads as fairly complete, while `docs/protocol-compatibility.md` still lists Soul Lifecycle as an incomplete known gap.

Evidence:
- `docs/soul-factory.md`
- `docs/protocol-compatibility.md`

Assessment: medium

### 5. Deployment docs and example config do not line up cleanly
Deployment docs discuss runtime endpoint and compatibility settings not reflected in the selected root `config.yaml` example.

Evidence:
- `docs/deployment.md`
- `config.yaml`

Assessment: medium

## Untested behavior

### 1. CI-to-registry-to-artifact flow is not proven end-to-end by the selected “e2e” integration file
The named integration file is mostly scaffold with placeholder `t.Log(...)` cases.

Evidence:
- `test/integration/e2e_ci_registry_test.go`

Assessment: high

### 2. Full page-level coverage for encrypted domains is thinner than store-level coverage
Notifications, payments, and org/member encrypted operations have solid unit coverage, but selected evidence does not prove their full browser journey through E2E tests.

Evidence:
- `web/tests/unit/notifications-store.test.js`
- `web/tests/unit/encrypted-domain-stores.test.js`
- `web/src/routes/notifications/+page.svelte`
- `web/src/routes/payments/+page.svelte`

Assessment: medium

### 3. LLM route control plane has limited selected flow coverage
The domain and handlers are concrete, but selected tests mainly prove read-model parsing and application, not a full route→release→deploy→rollback product slice.

Evidence:
- `internal/domain/llm.go`
- `internal/api/handlers/llm.go`
- `web/tests/unit/controlplane-store.test.js`

Assessment: medium

### 4. Soul Factory full lifecycle is only partly proven by selected evidence
Selected tests strongly cover soul stores and provisioning tracking, but not the full backend lifecycle claims from docs.

Evidence:
- `web/tests/unit/souls-store.test.js`
- `web/src/lib/stores/souls.svelte.js`
- `docs/soul-factory.md`

Assessment: medium

### 5. `observed_not_specified`: web/operator surfaces with no route-level docs or explicit feature spec
- `/api/v1/system/info` as a hard bootstrap contract
- relay-read-model-first store behavior
- encrypted payment history in the current UI
- public write kinds beyond simplified docs
- relay-backed Souls UI with no matching REST route

Evidence:
- `internal/api/handlers/system.go`
- `web/src/lib/stores/index.svelte.js`
- `web/src/lib/stores/payments.svelte.js`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/routes/souls/+page.svelte`

Assessment: high

## Risky behavior

### 1. Mixed transport model raises onboarding and acceptance risk
Bahia currently mixes relay read models, public signed Nostr writes, encrypted request/result flows, narrowed REST compatibility endpoints, and direct HTTP auth for some browser/API use. Each feature needs an explicit transport contract.

Evidence:
- `docs/control-planes.md`
- `web/src/lib/stores/controlplane.svelte.js`
- `web/src/lib/stores/public-controlplane.svelte.js`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `internal/api/router/router.go`

Assessment: high

### 2. Public vs encrypted relay separation is a security-critical boundary
Sensitive encrypted operations must never be published to public sidecar/browser relays. This boundary is clearly implemented but must remain explicit in acceptance criteria.

Evidence:
- `docs/control-planes.md`
- `internal/controlplane/encrypted_transport.go`
- `web/src/lib/nostr/encrypted-controlplane.js`

Assessment: high

### 3. Payments are split across encrypted and REST surfaces
The payments page uses encrypted `payments.history`, while dashboard summaries still use direct API payment history calls. The split is real but under-explained.

Evidence:
- `web/src/lib/stores/payments.svelte.js`
- `web/src/routes/payments/+page.svelte`
- `web/src/routes/+page.svelte`
- `internal/api/handlers/payments.go`

Assessment: high

### 4. Soul provisioning tracking still uses timeout-based failure behavior
Soul provisioning tracking includes local timeout failure behavior, which is weaker than the repo’s strict event-driven guidance for signer-first operator flows.

Evidence:
- `web/src/lib/stores/souls.svelte.js`
- `docs/control-planes.md`
- `docs/adoption-production-rollout.md`

Assessment: medium

### 5. Repository CI enrichment appears incomplete
The repository store seeds CI state to `empty` or `unsupported` and does not appear to perform real enrichment in the selected code.

Evidence:
- `web/src/lib/stores/repositories.svelte.js`

Assessment: high

### 6. Example config may cause rollout confusion
The selected root config looks like a simplified example rather than a full current feature surface.

Evidence:
- `config.yaml`
- `docs/deployment.md`
- `docs/adoption-production-rollout.md`

Assessment: medium

## Suggested first feature slice

### Encrypted Control Plane: notifications + transport contract

Why first:
- strongest alignment across docs, backend code, frontend code, and tests
- bounded scope
- high product relevance
- includes discovery, auth, relay separation, encrypted envelopes, correlation, and error handling

Primary evidence:
- `docs/control-planes.md`
- `internal/api/handlers/system.go`
- `internal/controlplane/encrypted_transport.go`
- `internal/controlplane/notification_encrypted_handlers.go`
- `web/src/lib/nostr/encrypted-controlplane.js`
- `web/src/lib/stores/notifications.svelte.js`
- `web/tests/unit/encrypted-controlplane.test.js`
- `web/tests/unit/notifications-store.test.js`

Suggested acceptance concerns:
1. `/api/v1/system/info` must advertise encrypted-request capability clearly.
2. Sensitive requests must go only to encrypted-request relays.
3. Publish OK false and terminal error results must surface cleanly.
4. Correlation by request event id and requester pubkey must be explicit.
5. Browser and backend docs must agree on the encrypted operation catalog.
