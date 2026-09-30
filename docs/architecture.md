# Bahia Architecture

## Virtualization resource control plane

Typed virtualization resources are PostgreSQL-authoritative today. This is a
known gap against the [charter](#charter-relay-canonical-bahia-normative):
the resources should become addressable relay state with Postgres as a derived
index (epic `bahia-irsry`, Phase 3). The public API, the current ContextVM reads
and canonical projections share an explicit DTO allowlist; none serializes
private resource documents directly. New code must not add ContextVM reads
for these resources. C/D admission services are
injected through `internal/app/virtualization.go`; until wired, mutations return
unavailable rather than invoking repositories or providers from handlers.

The existing constructor (`internal/app/app.go`) composes queries, startup journal
recovery, event subscriptions and metrics. REST registration is in
`internal/api/router/router.go` plus `virtualization.go`; ContextVM methods use
`EncryptedRequestTransport.RegisterContextVMHandler` in
`internal/controlplane/encrypted_transport.go`. Committed in-process events wake
journal replay, with durable author/tenant cursors and the signed-event outbox;
no PostgreSQL notification producer or polling queue is introduced.

Persistent VM ownership is distinct from both Loom ephemeral lifecycle classes.
See [Virtual machines and execution planes](user-guide/features/virtual-machines.md)
for public contracts, approval gates, capability rules and integration status.


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

Several of these properties are targets rather than current behaviour. The [charter](#charter-relay-canonical-bahia-normative) below is normative; the [source-of-truth table](#source-of-truth) records where today's code still falls short.

---

## Charter: relay-canonical Bahia (normative)

This charter was first written as the goal of `docs/plans/reconstructible-bahia-2026-05-23.md` and moved here on 2026-09-30 (bahia-irsry.8) so that it has normative status. When code, docs or reviews disagree with it, the charter wins; if the charter is wrong, change it here first.

**Goal.** Bahia is a reconstructible, relay-canonical orchestration fabric. Nostr relays hold canonical state as signed events. Any database is a disposable, rebuildable cache. A fresh Bahia instance cold-starts by replaying the event graph, which makes Bahia restartable, replaceable infrastructure rather than a sacred cluster brain.

**Invariants.**
1. **Relays are the source of truth.** Shared state is addressable/replaceable events (kind `30900` CAS state, `30315` status, `30078` app data, and the relevant standard NIPs); history and attestations are regular events (`4903`). Latest-wins by coordinate replaces row updates.
2. **Postgres is optional, derived and rebuildable.** It may index or cache relay state. It is never authoritative, never the trigger for republishing, and losing it loses no control-plane truth. The daemon boots and serves relay state without it.
3. **Reads are REQ subscriptions against addressable state.** Clients (web, CLI, MCP, sidecars, the daemon itself) read by subscribing with narrow filters, treat `EOSE` as "caught up", and keep the subscription open for live updates. A local event store (IndexedDB in the browser, an event store per Go process) holds what has been seen, so rendering and restart never wait on a re-fetch.
4. **ContextVM is interactive RPC only, never a read path.** ContextVM `25910` (optionally wrapped in CEP-4/NIP-59 `1059`/`21059`) is for interactions that are inherently request/response and not state: assistant turns, secret reveal and log fetch. A ContextVM reply is never the answer to "what is the current state of X"; that answer is a REQ.
5. **Writes are signed events verified by `OK`.** The author signs a canonical event and publishes it; relay `OK` (per relay) is delivery, not business completion. Durable progress and terminal truth come from subscriptions to canonical observables.
6. **Identity and authorization are event state.** Addressable `d` tags are minted by the author, not by a database sequence or UUID column, and membership/roles/trust lists are events relays can serve and clients can verify.
7. **Deletion is the definition of done.** Each migration slice removes the projector leg, reconciler ticker, REST route and ContextVM handler it replaces in the same change. Dead but exported code is removed rather than kept "for later".

**Enforcement.** The architecture ratchet (`make lint-arch`, also part of `go test ./...` and `pnpm run test:unit`) fails on new legacy-kind use outside `internal/nostrmigration`, new direct library relay subscriptions outside the relay pool and SoulFactory bus, new unannotated poll tickers in `internal/service`/`internal/reconcile` (`//nostr:allow-poll <reason>` to justify one), new test-only exported symbols in `internal/`, new `setInterval`/`$lib/api/client.js` use in web stores, and any route a DB-less daemon exposes without a tier gate. Pending acceptance tests name the issue that un-skips them (`bahia-irsry.11`: services visible from relays with no DB; `bahia-irsry.12`: protected routes render relay state with the daemon offline).

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
Bahia's canonical public control-plane contract is the set of canonical observable events described in `docs/control-planes.md`: clients read them with REQ subscriptions and write by publishing signed events. ContextVM (`25910`, normally wrapped by CEP-4/NIP-59 `1059`/`21059`) is for **interactive RPC only**: assistant turns, secret reveal and log fetch. Reads are never ContextVM.

Current gap: most mutations (and some reads, for example CLI/MCP/DNS-agent queries and the web's encrypted domains) still travel as ContextVM request/response, and the daemon mints entity identity in Postgres before replying (audit RC-1, RC-4). These handlers are legacy transport being replaced by client-signed intent events and REQs against addressable state (`bahia-irsry` Phases 3-5). Do not add new ContextVM read or CRUD methods.

Production examples:
- ContextVM interactive RPC (assistant, secret reveal, log fetch; legacy mutations until migrated): `25910`
- canonical state/app data: `30900`, `30078`
- canonical audit/status: `4903`, `30315`
- continuity heartbeat observations: NIP-38 status `30315` with `#domain=continuity` and heartbeat schema/d/worker tags (not a separate `30350` kind)
- ContextVM discovery: `11316`-`11320`
- relay sets and deletes: `30002`, `5`

Legacy Bahia request/status/result/read-model/encrypted kinds are migration inventory only and are not production runtime contracts.

### 3. Encrypted Nostr request/result plane
Sensitive browser-facing domains use encrypted ContextVM events (`25910` inside `1059`/`21059` where supported) on configured encrypted-request relays. Under the charter this plane carries interactive RPC only (secret reveal, log fetch, assistant); sensitive *state* belongs in encrypted events readable by REQ. Today several encrypted domains (notifications, payments, org/member flows) still read and write through it, which is part of the RC-4 gap above.

### 4. Native MCP transport
Bahia exposes JSON-RPC tools over HTTP at `/mcp` and `/api/v1/mcp`. Tool responses include correlation metadata so clients can follow async truth on relays.

### 5. REST API compatibility surface
REST remains for narrowed CRUD, query, logs, registry, and operational compatibility routes. It is no longer the best single description of overall product behavior.

Managed runtime observation and policy-bounded exact-target recovery are operated according to the [managed-instance supervision runbook](runbooks/managed-instance-supervision.md).

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
Target: canonical state lives on relays as signed events, and PostgreSQL is an optional, derived index/cache that can be dropped and rebuilt from relays (see the charter). Blobs and logs live in Blossom-backed storage.

Today: most domain state is still written to PostgreSQL first and projected to relays, and a daemon without PostgreSQL caps itself at tier1 (no services, environments, deployments or DNS). The [source-of-truth table](#source-of-truth) lists each gap and the issue that closes it.

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

Normative rule: **relays (addressable events) are canonical; PostgreSQL is an optional, derived index or cache.** The "Today" column is the honest current state; every row where it differs from the canonical home is a gap tracked under epic `bahia-irsry` (findings from `docs/investigations/nostr-first-architecture-audit-2026-09-29.md`).

| Concern | Canonical home (normative) | Today | Gap / tracking |
|---------|----------------------------|-------|----------------|
| Desired deployment/runtime state (services, environments, intents, runs, policies, DNS) | Addressable `30900` CAS state on relays, `d` minted by the author | PostgreSQL rows are authoritative; the projector republishes them to `30900` every 10 minutes; `d` tags are DB UUIDs | B-1, B-8, C-40; daemon authority inversion `bahia-irsry.11` |
| Daemon without PostgreSQL | Boots and serves relay state | Boots, but caps at tier1: tier2/tier3 routes return 503 and services/environments/DNS are unavailable | B-11; ratchet `TestArchitectureDBLessDaemonBootCapsTierAndGatesNilRepositoryRoutes`, pending acceptance test un-skipped by `bahia-irsry.11` |
| Shared browser state | Browser local event store (IndexedDB) kept current by REQ subscriptions (`since` cursor or NIP-77) | Relay `30900` projections of DB state; IndexedDB holds derived snapshots; protected routes are gated on a REST `/orgs` probe | A-1, A-3, A-4; web tier `bahia-irsry.12` (pending test "protected routes render relay state with the daemon offline") |
| Runtime observations | `30315` status and `30900` state published by the observer | PostgreSQL (from runtime queries and action results), then projected | Phase 3 `bahia-irsry.11` |
| Workflow history / audit trail | `4903` audit events, retained on at least one archival relay | PostgreSQL workflow history; `4903` is published, but relay retention is 7 days | C-19, C-39; relay tier `bahia-irsry.9` |
| Membership, roles, org and trust lists | Events relays can serve and clients can verify (encrypted where sensitive) | PostgreSQL only, reached through REST or ContextVM | RC-6, B-27; `bahia-irsry.11`/`.12` |
| Secrets, notifications, payments | Encrypted events for state; ContextVM only for interactive secret reveal | PostgreSQL, read and written through REST/ContextVM | RC-4; `bahia-irsry.11`/`.12` |
| Pending service-authored Nostr delivery | Per-relay `OK` tracking in the publisher; local event store per process | PostgreSQL `nostr_events` publish-state outbox (also used for replay cursors and dedup) | B-3, B-13, C-2; Go client tier `bahia-irsry.10` |
| Kind model | Canonical kinds only (`30900`, `4903`, `30315`, `25910`, `11316`-`11320`, `30002`, `30078`, standard NIPs) | Legacy kinds still defined in `internal/kinds` and referenced by runtime code | C-43, C-44; frozen by the legacy-kind ratchet, removed in `bahia-irsry.9` |
| Container image distribution | Bahia OCI registry and/or configured image registries | Same | — |
| Logs / blobs | Blossom-backed storage where configured | Same | — |

---

## Key design decisions

1. **Relays are canonical** — state is addressable events, reads are REQ subscriptions, writes are signed events verified by `OK`; PostgreSQL is an optional derived cache (see the charter; current gaps in the source-of-truth table)
2. **ContextVM for interactive RPC only** — assistant, secret reveal and log fetch; never a read path
3. **Intent-based deployments** — request and execution are separate, enabling approvals and auditability
4. **Signer-first identity** — users and operators act through signed Nostr identities
5. **Encrypted sensitive domains** — not all browser state belongs on public relays
6. **REST as narrowed compatibility** — HTTP remains important, but is no longer the whole system model
7. **Drift detection as first-class state** — runtime truth is continuously compared with desired state
8. **Durable outbound publication** — service-authored events are persisted before relay delivery and retried without treating transport acceptance as business completion
