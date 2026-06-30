# Bahia: Distributed Continuity & Degraded-Mode Orchestration Fabric

> **Status**: Design proposal
> **Date**: 2026-05-23
> **Author**: bizarro

## Philosophy

Bahia is **not**:
- Kubernetes HA
- Cloud autoscaling
- Enterprise DR theater

Bahia **is**:
- Small-cluster, heterogeneous, edge-aware, event-driven, graceful degradation orchestration

**Core insight**: Your infrastructure is heterogeneous, resource-constrained, hardware-diverse, edge-oriented, and service-tiered. Traditional HA assumptions (identical replacement nodes) are wrong for this environment. **Degraded continuity is more important than perfect redundancy.**

Failover is not "identical replacement."
Failover is **"continuity-preserving degraded topology."**

---

## Core Concept: Capability-Aware Failover

The fundamental primitive. Instead of:

> "service X has replica Y"

Bahia models:

> "service X has continuity profiles"

### Continuity Profiles

| Profile    | Meaning                       |
|------------|-------------------------------|
| `full`     | Normal operation              |
| `degraded` | Reduced functionality         |
| `emergency`| Minimum survivable state      |
| `offline`  | Unavailable intentionally     |

---

## Nostr Event Model

### Canonical Continuity Profile (kind 31400)

Replaceable event defining how a service degrades:

```json
{
  "kind": 31400,
  "tags": [
    ["d", "continuity:lnd-main"],
    ["service", "lnd"],
    ["primary", "t7610"],
    ["standby", "optiplex-9020"],
    ["profile", "full"],
    ["requires", "watchtower"],
    ["requires", "autopilot"],
    ["requires", "routing"],
    ["profile", "degraded"],
    ["disables", "autopilot"],
    ["disables", "watchtower-server"],
    ["limits", "max-channels=32"],
    ["profile", "emergency"],
    ["mode", "receive-only"]
  ]
}
```

### Event Kind Registry

| Kind  | Purpose                    |
|-------|----------------------------|
| 31400 | Continuity profile         |
| 31401 | Failover policy            |
| 31402 | Standby node definition    |
| 31403 | Replication policy         |
| 31404 | Recovery workflow          |
| 30315 | Heartbeat observation (NIP-38 status, `#domain=continuity`) |
| 30351 | Continuity status          |
| 30352 | Degraded mode activation   |
| 30353 | Recovery progress          |
| 38398 | Failover request           |
| 38399 | Recovery request           |

---

## Failover Recipes

Failover should **never** be hardcoded shell scripts, hidden automation, or opaque cluster behavior. Instead, failover is expressed as **declarative recipes**.

### Example: LND Optiplex Failover

```yaml
name: lnd-optiplex-failover
trigger:
  type: heartbeat-loss
  target: t7610
  timeout: 120s
steps:
  - action: wake_on_lan
    target: optiplex-9020
  - action: wait_for_heartbeat
    timeout: 180s
  - action: mount_volume
    source: /backup/lnd
  - action: restore_scb
  - action: deploy_service
    service: lnd
    profile: degraded
  - action: publish_endpoint
    dns: lnd-backup.cascadia
  - action: emit_event
    type: FAILOVER_COMPLETED
```

### Example: Restore Primary

```yaml
name: restore-primary-t7610
steps:
  - action: sync_relay_state
  - action: stop_degraded_services
  - action: restore_dns_routes
  - action: move_lnd_back
  - action: re-enable_agents
```

---

## Service Profiles

Every service defines:
- Hardware requirements
- Continuity profiles
- Minimum survivable capabilities
- Resource envelopes
- Startup ordering
- Data dependencies

### Lightning Node

| Profile     | Capabilities |
|-------------|-------------|
| **Full**    | Routing, watchtower server, autopilot, large channel table, full gossip sync |
| **Degraded**| Routing enabled, autopilot disabled, watchtower client only, reduced peer count, limited gossip |
| **Emergency**| Receive-only, SCB recovery, minimal peer set |

### Nostr Relay

| Profile     | Capabilities |
|-------------|-------------|
| **Full**    | High throughput, full indexing, archive retention, heavy filtering |
| **Degraded**| Reduced connections, short retention, reduced indexing, memory limits |
| **Emergency**| Ephemeral relay, minimal persistence, rate limited |

---

## DNS Integration

Failover mutates:
- Service endpoints
- Route topology
- Discovery catalogs
- Runtime capability announcements

Example:
```
relay.prod.cascadia
  → t7610 normally
  → optiplex during degraded mode
```

Bahia synthesizes DNS automatically from:
- Continuity state
- Deployment graph
- Worker heartbeats

---

## Worker Capability Topology

Workers publish their capabilities:

```json
[
  ["host", "optiplex-9020"],
  ["continuity-role", "standby"],
  ["supports", "lnd"],
  ["supports", "relay"],
  ["supports", "signer"],
  ["profile", "degraded-only"],
  ["power-state", "cold"]
]
```

Bahia reasons over:
- Capability graph
- Continuity graph
- Dependency graph

---

## Standby Orchestration

| Tier | Strategy | Examples |
|------|----------|----------|
| **Critical** (money/signing) | Warm-ish | LND SCB sync, signer backups, bunker services |
| **Content** (relays) | Eventually replicated | Losing some relay events is acceptable |
| **AI inference** | No failover | Or tiny fallback models only |

---

## Operational Continuity Graph

Bahia continuously derives:
- Can this service survive?
- How degraded would failover be?
- What dependencies are missing?
- Which standby nodes are stale?
- What was last replicated?

This is **infrastructure resilience intelligence**.

---

## Smart Degradation

Example scenario:
> "T7610 power consumption critical. Recommend degraded mode?"

Bahia:
1. Analyzes service graph
2. Computes survivability
3. Proposes continuity downgrade:
   - Move relay to degraded profile
   - Pause AI fleet
   - Reduce LND gossip sync
   - Disable Frigate transcoding

---

## Power-Aware Orchestration

Bahia models:
- Power envelopes
- Thermal constraints
- UPS runtime
- Generator runtime
- Solar availability
- Battery state

Then intelligently degrades to preserve critical systems. This is **edge-native infrastructure cognition**.

---

## Continuity Dashboard (UX)

### Status Display
```
NORMAL → DEGRADED → EMERGENCY → RECOVERING
```
Per service.

### Topology Map
- Primary nodes
- Standby nodes
- Cold nodes
- Replication state
- Data freshness
- Survivability score

### AI-Assisted Resilience Simulation
> "What happens if the T7610 dies?"

Bahia simulates topology, estimates survivability, identifies missing backups, computes degraded capacity.

---

## Ownership Boundaries

### Bahia owns:
- Continuity graph
- Failover recipes
- Orchestration
- Topology reasoning
- Event provenance
- Degraded-mode transitions
- Capability matching

### Existing systems own:
- Replication primitives
- Snapshots
- Actual storage
- Actual runtime serving
- Network transport

---

## Implementation Principles

1. **Embrace heterogeneous degraded continuity** — simpler, more realistic, lower power, lower maintenance
2. **Do NOT implement**: enterprise HA clusters, active-active databases, cloud failover abstraction
3. **Recipe-driven**: All failover and recovery expressed as declarative, auditable recipes
4. **Nostr-native**: All state communicated via Nostr events
5. **DNS-integrated**: Failover automatically mutates service discovery
6. **Power-aware**: Edge-native resource consciousness

---

## Gap Analysis

_Completed 2026-05-23 via automated codebase exploration._

### What Already Exists (Attachment Points)

| Area | Status | Details |
|------|--------|---------|
| **Worker model** | ✅ Rich | `domain.Worker` with capabilities, ML capabilities, scheduling states (active/cordoned/draining/maintenance/disabled), liveness (online/stale/offline). Keyed by Nostr pubkey. |
| **Worker capabilities** | ✅ Rich | Workload kinds, runtimes, artifact formats, accelerators, toolchains, features. Projected as Nostr read models (kind 31989). |
| **Worker liveness** | ⚠️ Passive | Derived from advertisement freshness (`LastAdvertisementAt`). Stale >5min, offline >30min. No active heartbeat protocol. |
| **Worker scheduling** | ✅ Rich | Cordon/drain/maintenance/labels/policy commands via Nostr control plane. Assignment tracking, drain blockers, eligibility previews. |
| **DNS projection** | ✅ Phase 0 | `DNSProjector` derives endpoints from service/LLM/ML/worker state. Reconciler compares desired vs actual. Filesystem backend only. |
| **DNS cutover** | ❌ Missing | No active DNS switching, weighted routing, or failover mutation. Reserved kinds exist but handlers not wired. |
| **Backup system** | ✅ Full | Kopia/Velero backends. Recipes, policies, runs, verification, restore with approval gates. Durable coordinator pattern. |
| **Reconciliation** | ✅ Core | Ticker-based desired-vs-observed loop for services, DNS, LLM routes. Drift detection and remediation. |
| **Rollout engine** | ✅ Framework | Replace/canary/blue-green strategies with health gates. Traffic shifting stubs (log only, not implemented). |
| **Event system** | ✅ Extensive | 100+ Nostr event kinds. In-process pub/sub bus. Nostr-reactive control plane. |
| **Service model** | ✅ Core | Services, environments, deployment intents/runs, runtime observations, drift status. |
| **Workflow** | ⚠️ Limited | Deployment workflow coordinator exists. ML recipe coordinator. Not a general recipe engine. |

### What's Missing (Gaps to Fill)

| Gap | Priority | Notes |
|-----|----------|-------|
| **Continuity profiles** | 🔴 Core | No `ContinuityProfile` domain type. No full/degraded/emergency/offline modeling. |
| **Failover recipes** | 🔴 Core | Backup recipes exist but not general failover recipes with triggers and step sequences. |
| **Standby node model** | 🔴 Core | No cold/warm/hot standby concepts. Workers have scheduling states but not continuity roles. |
| **Active heartbeat protocol** | 🟡 Important | Currently passive (advertisement freshness). Need dedicated heartbeat for failover trigger reliability. |
| **Failover trigger engine** | 🔴 Core | No heartbeat-loss detection, no automatic failover initiation. |
| **DNS cutover engine** | 🟡 Important | Projection exists but no active endpoint mutation on failover. Need to wire DNS switching to continuity state changes. |
| **Recovery workflows** | 🟡 Important | Backup restore exists but not topology-aware recovery (re-enable agents, restore DNS routes, sync state). |
| **Replication policies** | 🟡 Important | No state replication modeling. No SCB sync, relay event replication, or signer backup scheduling. |
| **Continuity graph** | 🟠 Future | No survivability analysis, dependency-aware failover reasoning, or "what if" simulation. |
| **Power-aware orchestration** | 🟠 Future | No power envelope, thermal, UPS, battery, solar modeling. |
| **Smart degradation** | 🟠 Future | No automated degradation proposals based on resource constraints. |
| **Continuity dashboard (UX)** | 🟠 Future | No web UI for continuity status, topology maps, or resilience simulation. |

### Nostr Event Kind Conflicts

The design proposed kinds that conflict with existing allocations:

| Proposed Kind | Conflict | Resolution |
|---------------|----------|------------|
| 38398 (Failover request) | `KindMLInferenceRollbackResult` | Reassign to **38430** |
| 38399 (Recovery request) | `KindMLModelImportResult` | Reassign to **38431** |

**Additional known collisions in existing code:**
- `31974`: `KindSystemDiscovery` vs `KindWorkerState` — needs resolution
- `31991-31993`: Worker states vs Backup registries — needs resolution

### Revised Event Kind Registry

| Kind  | Purpose                    | Status |
|-------|----------------------------|--------|
| 31400 | Continuity profile         | 🆕 New |
| 31401 | Failover policy            | 🆕 New |
| 31402 | Standby node definition    | 🆕 New |
| 31403 | Replication policy         | 🆕 New |
| 31404 | Recovery workflow          | 🆕 New |
| 30315 | Heartbeat observation (NIP-38 status, `#domain=continuity`) | Reuses NIP-38 |
| 30351 | Continuity status          | 🆕 New |
| 30352 | Degraded mode activation   | 🆕 New |
| 30353 | Recovery progress          | 🆕 New |
| 38430 | Failover request           | 🆕 New (reassigned from 38398) |
| 38431 | Recovery request           | 🆕 New (reassigned from 38399) |

### Architecture Integration Map

```
┌─────────────────────────────────────────────────────────────┐
│                    CONTINUITY FABRIC (NEW)                    │
│                                                               │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐   │
│  │  Continuity   │  │  Failover    │  │  Recovery        │   │
│  │  Profiles     │  │  Recipes     │  │  Workflows       │   │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘   │
│         │                  │                    │              │
│  ┌──────┴──────────────────┴────────────────────┴─────────┐  │
│  │              Continuity Engine (new)                     │  │
│  │  - Heartbeat monitoring                                 │  │
│  │  - Failover trigger evaluation                          │  │
│  │  - Recipe execution                                     │  │
│  │  - Topology reasoning                                   │  │
│  └──────┬──────────────────┬────────────────────┬─────────┘  │
│         │                  │                    │              │
└─────────┼──────────────────┼────────────────────┼─────────────┘
          │                  │                    │
┌─────────┴───────┐ ┌───────┴──────┐ ┌──────────┴──────────┐
│  EXISTING        │ │  EXISTING     │ │  EXISTING            │
│                  │ │               │ │                      │
│  Worker Model    │ │  DNS          │ │  Backup/Recovery     │
│  - Capabilities  │ │  - Projector  │ │  - Kopia/Velero      │
│  - Scheduling    │ │  - Reconciler │ │  - Recipes           │
│  - Liveness      │ │  - Backends   │ │  - Restore coord     │
│  - Assignments   │ │               │ │                      │
│  Control Plane   │ │  Reconciler   │ │  Rollout Engine      │
│  - Nostr reactor │ │  - Drift      │ │  - Canary/B-G        │
│  - Events        │ │  - Remediate  │ │  - Health gates      │
└──────────────────┘ └───────────────┘ └──────────────────────┘
```

---

## Implementation Roadmap

See beads epic for tracked issues. Phased approach:

### Phase 1: Domain Foundation
- Continuity profile domain types
- Standby node model (extend Worker)
- Failover recipe domain types
- Active heartbeat protocol

### Phase 2: Orchestration Engine
- Failover trigger engine (heartbeat-loss detection)
- Recipe execution engine
- DNS cutover integration
- Recovery workflow execution

### Phase 3: Replication & State
- Replication policy model
- State sync primitives
- Continuity status projection (Nostr read models)

### Phase 4: Intelligence & UX
- Continuity graph / survivability analysis
- Power-aware orchestration
- Smart degradation proposals
- Continuity dashboard UI
