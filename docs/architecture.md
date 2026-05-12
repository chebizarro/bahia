# Bahia Architecture

## Overview

Bahia is a **deployment and runtime control plane**. Its core responsibilities are still familiar:
- register and track builds and artifacts
- manage deployment intents, approvals, execution, and rollback
- observe runtime state and detect drift
- coordinate execution on remote workers or direct runtime targets

What changed is the **shape of the control plane**.

Bahia is now:
- **Nostr-native** — control-plane operations are modeled as signed events
- **Sidecar-first** — the relay sidecar is the primary public/realtime event boundary
- **Signer-first** — browser and operator actions are tied to Nostr signer identity
- **Relay-read-model-first** — the browser bootstraps shared state from relay projections rather than primarily from REST lists
- **Encrypted for sensitive domains** — notifications, payments history, org/member flows, secrets, logs, and similar domains use encrypted Nostr request/result events where configured

The HTTP API and MCP server still matter, but they are now **narrowed compatibility/query/tooling surfaces**, not the entire product contract.

---

## Control-plane hierarchy

### 1. System discovery
`Nostr discovery events (kind 31974 + NIP-51 kind 30002)` advertises:
- browser relay URLs
- sidecar URL
- service pubkey
- control-plane kind mappings
- registry/runtime/blossom metadata
- feature flags such as `relay_read_models`, `direct_nostr_http_auth`, and `encrypted_nostr_requests`

This endpoint is the browser and tooling bootstrap contract.

### 2. Public Nostr control plane
Bahia's canonical public control-plane contract is the signed Nostr request/status/result/read-model family described in `docs/control-planes.md` and implemented in `internal/controlplane/reactor.go`.

Examples:
- request kinds: `5961-5989`
- status kinds: `6961-6978`
- result kinds: `7961-7979`
- read models: `31961-31970`

### 3. Encrypted Nostr request/result plane
Sensitive browser-facing domains use encrypted request/result events (`5980/7980`) on separate encrypted-request relays where configured.

### 4. Native MCP transport
Bahia exposes JSON-RPC tools over HTTP at `/mcp` and `/api/v1/mcp`. Tool responses include correlation metadata so clients can follow async truth on relays.

### 5. REST API compatibility surface
REST remains for narrowed CRUD, query, logs, registry, and operational compatibility routes. It is no longer the best single description of overall product behavior.

---

## Major components

### Browser / CLI / MCP clients
Clients discover capabilities from `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`, then interact with Bahia through a mix of:
- public relay traffic
- encrypted relay traffic
- MCP JSON-RPC
- selected REST endpoints

### Relay sidecar and public relays
The relay sidecar is the primary realtime/public boundary for:
- browser read models
- public control-plane requests
- status/result replies
- activity feed events

This keeps browser state event-driven and avoids REST polling as the primary shared-state mechanism.

### Control-plane reactor
The reactor validates signed inbound Nostr requests, authorizes pubkeys, executes domain logic, publishes status/result replies, and drives read-model projection.

Key responsibilities:
- service/deployment actions
- LLM route/release/deployment flows
- adoption/import and direct runtime operator flows
- encrypted result dispatch hooks

Primary implementation:
- `internal/controlplane/reactor.go`
- `internal/controlplane/operator_actions.go`
- `internal/controlplane/encrypted_transport.go`

### Domain / service layer
Core business logic still lives in the service/domain layer:
- registry services for services/builds/artifacts/deployments/state
- runtime lifecycle and rollout handling
- payment, policy, notification, and LLM coordination
- Soul Factory lifecycle/provisioning logic

### OCI registry server
Bahia serves as an OCI-compatible internal registry.

```text
┌─────────────────────────────────────────────────────┐
│                  OCI Registry                       │
├─────────────────────────────────────────────────────┤
│  /v2/* endpoints                                    │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐ │
│  │  Manifests  │  │    Blobs    │  │    Tags     │ │
│  │ (PostgreSQL)│  │  (Blossom)  │  │ (PostgreSQL)│ │
│  └─────────────┘  └─────────────┘  └─────────────┘ │
└─────────────────────────────────────────────────────┘
```

Authentication includes NIP-98, service accounts, and anonymous pull from allowed CIDRs.

### Hive-CI bridge
The Hive-CI bridge subscribes to workflow events and turns them into build/artifact/deployment state.

```text
Hive-CI (5401)     Hive-CI (5402)
Workflow Run  ────▶  Workflow Result
     │                    │
     └────────┬───────────┘
              ▼
       ┌─────────────┐
       │   Bridge    │
       │  (Bahia)    │
       └──────┬──────┘
              │
    ┌─────────┼─────────┐
    ▼         ▼         ▼
 Build    Artifact   Intent
              │
              ▼
         OCI Registry
```

### Runtime and reconcile layer
Bahia executes deployments through Loom workers and/or direct runtime targets, then reconciles desired vs observed state.

Runtime targets may include:
- Docker
- Compose
- Kubernetes
- Podman
- adopted direct-runtime targets

### Persistence
Bahia persists canonical state in PostgreSQL and stores selected blobs/logs in Blossom-backed storage.

---

## Key browser/runtime flows

### Browser bootstrap flow
1. Browser requests `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`
2. Browser discovers relay topology and feature flags
3. Browser connects to advertised relays
4. Browser queries read models until EOSE
5. Browser subscribes live for ongoing updates

### Public action flow
1. User/operator signs a public Nostr request
2. Bahia validates and processes the event
3. Bahia publishes status and terminal result replies
4. Bahia projects updated read models
5. Browser state updates from relays

### Encrypted action flow
1. Browser encrypts a request to Bahia's service pubkey
2. Request is published to encrypted-request relays
3. Bahia decrypts, authorizes, and executes the operation
4. Bahia encrypts the terminal result back to the requester

### Deployment flow
1. Build/artifact state exists or is ingested from CI
2. Deployment intent is created
3. Approval occurs when required
4. Deployment run executes
5. Runtime observation updates desired/observed state
6. Drift is tracked and published

---

## Source of truth

| Concern | Primary home |
|---------|--------------|
| Desired deployment/runtime state | PostgreSQL |
| Shared browser state projection | Nostr read models projected from persisted state |
| Runtime observations | PostgreSQL (from runtime queries / action results) |
| Workflow history | PostgreSQL |
| Public audit/activity trail | Nostr relays |
| Container image distribution | Bahia OCI registry and/or configured image registries |
| Logs / blobs | Blossom-backed storage where configured |

---

## Key design decisions

1. **Nostr for control-plane coordination** — requests, status/results, and read models are event-driven
2. **Intent-based deployments** — request and execution are separate, enabling approvals and auditability
3. **Signer-first identity** — users and operators act through signed Nostr identities
4. **Encrypted sensitive domains** — not all browser state belongs on public relays
5. **REST as narrowed compatibility** — HTTP remains important, but is no longer the whole system model
6. **Drift detection as first-class state** — runtime truth is continuously compared with desired state
