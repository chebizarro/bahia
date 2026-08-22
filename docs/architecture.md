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
ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) advertise:
- browser relay URLs
- sidecar URL
- service pubkey
- control-plane kind mappings
- registry/runtime/blossom metadata
- feature flags such as `relay_read_models`, `direct_nostr_http_auth`, and `encrypted_nostr_requests`

This endpoint is the browser and tooling bootstrap contract.

### 2. Public Nostr control plane
Bahia's canonical public control-plane contract is ContextVM CRU (`25910`, normally wrapped by CEP-4/NIP-59 `1059`/`21059`) plus canonical observable events described in `docs/control-planes.md`.

Production examples:
- ContextVM mutation/request-response: `25910`
- canonical state/app data: `30900`, `30078`
- canonical audit/status: `4903`, `30315`
- continuity heartbeat observations: NIP-38 status `30315` with `#domain=continuity` and heartbeat schema/d/worker tags (not a separate `30350` kind)
- ContextVM discovery: `11316`-`11320`
- relay sets and deletes: `30002`, `5`

Legacy Bahia request/status/result/read-model/encrypted kinds are migration inventory only and are not production runtime contracts.

### 3. Encrypted Nostr request/result plane
Sensitive browser-facing domains use encrypted ContextVM events (`25910` inside `1059`/`21059` where supported) on configured encrypted-request relays.

### 4. Native MCP transport
Bahia exposes JSON-RPC tools over HTTP at `/mcp` and `/api/v1/mcp`. Tool responses include correlation metadata so clients can follow async truth on relays.

### 5. REST API compatibility surface
REST remains for narrowed CRUD, query, logs, registry, and operational compatibility routes. It is no longer the best single description of overall product behavior.

---

## Major components

### Browser / CLI / MCP clients
Clients discover capabilities from ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`), then interact with Bahia through a mix of:
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
The Hive-CI bridge subscribes to workflow events and registers verified build artifacts. Deployment promotion is a separate authority: CI success cannot create an intent or change desired state.

```text
Hive-CI (canonical dispatch)     Hive-CI (canonical result)
Workflow Run  ────▶  Workflow Result
     │                    │
     └────────┬───────────┘
              ▼
       ┌─────────────┐
       │   Bridge    │
       │  (Bahia)    │
       └──────┬──────┘
              │
         ┌────┴────┐
         ▼         ▼
       Build    Artifact
                   │
                   ▼
              OCI Registry

A separately authorized promotion intent may later reference the registered
digest; it is not emitted by the Hive-CI bridge.
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

The main service-authored Nostr publisher also uses the `nostr_events` table as a durable outbound outbox. It records a signed event as `pending` before relay delivery, marks it `published` after at least one accepted (or duplicate) relay `OK`, and retries pending rows with backoff after transient failures. This outbox protects service-authored operational/audit publication; it does not turn a caller's ContextVM acknowledgment into terminal business truth.

---

## Key browser/runtime flows

### Browser bootstrap flow
1. Browser subscribes to ContextVM discovery (`11316`-`11320`) and relay sets (`30002`)
2. Browser discovers relay topology and feature flags
3. Browser connects to advertised relays
4. Browser queries canonical observables until EOSE
5. Browser subscribes live for ongoing updates

### Public action flow
1. User/operator signs a public Nostr request
2. Bahia validates and processes the event
3. Bahia publishes status and terminal result replies
4. Bahia projects updated canonical observables
5. Browser state updates from relays

### Encrypted action flow
1. Browser encrypts a request to Bahia's service pubkey
2. Request is published to the ContextVM request relay policy
3. Bahia decrypts, verifies the inner signer, authorizes, and executes the operation
4. Bahia publishes the encrypted response through an isolated response pool on the same ContextVM relay policy
5. Stored `1059` requests receive stored `1059` replies; ephemeral `21059` requests receive `21059` replies, correlated to the outer request with an `e` reply tag

### Deployment flow
1. Build/artifact state exists or is ingested from CI
2. A signed `service/deploy` request is policy-evaluated and creates a deployment intent with a desired-state snapshot
3. Approval occurs when required
4. Approved native deployments create a run and execute through the runtime lifecycle service, including deployment-unit targeting
5. Runtime observation updates desired/observed state; failed apply or completion is recorded as failure rather than success
6. Drift and operational status are projected to canonical Nostr observables

---

## Source of truth

| Concern | Primary home |
|---------|--------------|
| Desired deployment/runtime state | PostgreSQL |
| Shared browser state projection | Nostr canonical observables projected from persisted state |
| Runtime observations | PostgreSQL (from runtime queries / action results) |
| Workflow history | PostgreSQL |
| Public audit/activity trail | Nostr relays (`4903` audit projections remain relay-queryable) |
| Pending service-authored Nostr delivery | PostgreSQL `nostr_events` publish-state outbox |
| Container image distribution | Bahia OCI registry and/or configured image registries |
| Logs / blobs | Blossom-backed storage where configured |

---

## Key design decisions

1. **Nostr for control-plane coordination** — ContextVM requests, statuses/audits, and canonical state observables are event-driven
2. **Intent-based deployments** — request and execution are separate, enabling approvals and auditability
3. **Signer-first identity** — users and operators act through signed Nostr identities
4. **Encrypted sensitive domains** — not all browser state belongs on public relays
5. **REST as narrowed compatibility** — HTTP remains important, but is no longer the whole system model
6. **Drift detection as first-class state** — runtime truth is continuously compared with desired state
7. **Durable outbound publication** — service-authored events are persisted before relay delivery and retried without treating transport acceptance as business completion
