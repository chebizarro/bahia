# FIPS ↔ Bahia DNS Integration Design

> **Status**: Draft
> **Date**: 2026-05-23
> **Depends on**: `dns-orchestration-layer.md` (Bahia DNS Phases 0–3), FIPS architecture docs
> **Scope**: Design analysis — no implementation

---

## 1. Executive Summary

FIPS (Free Internetworking Peering System) is a Rust-based self-organizing encrypted mesh network built on Nostr identities. Bahia is a Go-based Nostr-native infrastructure orchestration platform whose DNS layer projects DNS records from infrastructure state. Both systems use Nostr keypairs as identity primitives and publish structured Nostr events to relays.

This document analyzes how the two systems can be integrated, identifies concrete touch points in both codebases, and proposes a phased integration plan. The integration spans four surfaces: **DNS resolution bridging**, **Nostr event bridging**, **transport-layer mesh connectivity**, and **gateway interoperation**.

### Key Finding

The strongest near-term integration is a **read-only Nostr event bridge** — Bahia subscribes to FIPS Kind 37195 overlay adverts to auto-discover mesh nodes, and FIPS nodes subscribe to Bahia Kind 31976 endpoint events to populate their `.fips` host aliases. This requires no protocol changes in either system and leverages the shared Nostr relay infrastructure both already use.

---

## 2. System Inventory

### 2.1 FIPS Identity and Addressing

| Concept | Value | Derivation |
|---|---|---|
| Identity | secp256k1 keypair (same as Nostr) | Generated locally |
| Human-readable | `npub1xxx...xxx` (bech32) | Encoded from pubkey |
| Routing address | `node_addr` (16 bytes) | `SHA-256(pubkey)[0..16]` |
| IPv6 overlay | `fd00::/8` ULA | `fd` + `node_addr[0..15]` |
| DNS name | `<npub>.fips` or alias via `/etc/fips/hosts` | Static config or DNS resolver |

**Key file**: `fips/docs/design/fips-architecture.md` — identity derivation diagram

### 2.2 FIPS Nostr Event Surface

| Kind | Name | Purpose | Storage |
|---|---|---|---|
| 37195 | Overlay advert | Transport endpoints for a node | Parameterized replaceable |
| 21059 | Traversal signaling | NAT hole-punch offer/answer | Ephemeral |
| 10050 | Inbox relay list | DM relay preferences | Replaceable |

**Key file**: `fips/docs/reference/nostr-events.md`

**Kind 37195 content shape** (`OverlayAdvert`):
```json
{
  "identifier": "fips-overlay-v1",
  "version": 1,
  "endpoints": [
    {"transport": "udp", "addr": "203.0.113.45:2121"},
    {"transport": "tor", "addr": "xxxxx.onion:8443"},
    {"transport": "udp", "addr": "nat"}
  ],
  "signalRelays": ["wss://relay.damus.io"],
  "stunServers": ["stun:stun.l.google.com:19302"]
}
```

The event is signed by the node's FIPS identity key (= Nostr key). The `d` tag is `fips-overlay-v1`. A `protocol` tag carries the configured `app` namespace. NIP-40 `expiration` tag enforces TTL (default 3600s).

### 2.3 Bahia DNS Event Surface

| Kind | Name | Purpose | Storage |
|---|---|---|---|
| 31975 | `DNSZoneState` | Zone definitions | Parameterized replaceable |
| 31976 | `DNSEndpointState` | Canonical endpoint projection | Parameterized replaceable |
| 31977 | `DNSPolicyState` | Active DNS policies | Parameterized replaceable |
| 31978 | `DNSBackendState` | Backend health/sync | Parameterized replaceable |

**Key file**: `bahia/docs/designs/dns-orchestration-layer.md` §3, `bahia/internal/adapters/nostr/projector.go`

**Kind 31976 content shape** (published by `publishDNSEndpoint` in `projector.go`):
```json
{
  "service": "drydock",
  "route": "review",
  "env": "prod",
  "proto": "http",
  "addr": "10.0.1.44",
  "port": 8000,
  "runtime": "vllm",
  "hardware": "l40s",
  "health": "healthy",
  "capabilities": ["llm", "code-review"]
}
```

Tags include: `d` (endpoint coordinate), `service`, `route`, `env`, `health`, `addr`, `dns` (FQDN), `capability`, `runtime`, `t` (type markers).

### 2.4 Bahia DNS Backend Interface

```go
// bahia/internal/adapters/dns/backend.go
type Backend interface {
    BackendType() domain.DNSBackendType
    Health(ctx context.Context) error
    ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error)
    SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}
```

Current adapters: `filesystem` (Phase 0). Planned: `coredns` (etcd), `powerdns`, `dnsmasq`. The reconciler (`dns_reconciler.go`) calls `SyncZone()` atomically per zone after diffing projected vs actual records.

### 2.5 Bahia DNS Domain Types

```go
// bahia/internal/domain/dns.go (abbreviated)
type DNSEndpoint struct {
    Name, Environment, Zone, FQDN, Address string
    Port          *int
    Protocol      string
    Runtime       string
    Hardware      string
    Capabilities  []string
    Health        HealthStatus
    Family        DNSEndpointFamily  // service | llm | ml | worker
    WorkerPubkey  string
    // ...
}

type DNSRecord struct {
    Zone, Name, FQDN string
    Type      DNSRecordType  // A | AAAA | CNAME | SRV
    Value     string
    TTL       int
    // ...
}
```

The projector (`dns_projector.go`) materializes `DNSEndpoint` from four sources: `EnvironmentServiceState`, `LLMRouteState`, `MLInferenceState`, and `Worker`. Health gating ensures only healthy endpoints produce records.

---

## 3. Integration Surfaces

### 3.1 Surface Map

```
┌──────────────────────────────────────────────────────────────────┐
│                     Shared Nostr Relays                          │
│                                                                  │
│   FIPS publishes:              Bahia publishes:                  │
│   • Kind 37195 (adverts)       • Kind 31976 (endpoints)          │
│   • Kind 10050 (inbox relays)  • Kind 31975 (zones)              │
│   • Kind 21059 (signaling)     • Kind 31977 (policies)           │
│                                • Kind 31978 (backends)           │
└──────────┬──────────────────────────────────┬────────────────────┘
           │                                  │
           ▼                                  ▼
┌─────────────────────┐          ┌─────────────────────────────────┐
│     FIPS Node        │          │        Bahia                    │
│                      │          │                                 │
│  fips daemon         │          │  DNS Projector                  │
│  ├─ .fips DNS        │◄────────►│  ├─ ProjectZoneRecords()        │
│  ├─ /etc/fips/hosts  │          │  DNS Reconciler                 │
│  ├─ identity cache   │          │  ├─ ReconcileOnce()             │
│  └─ fips0 TUN        │          │  DNS Backend Adapters           │
│                      │          │  ├─ filesystem                  │
│  fips-gateway        │          │  ├─ coredns/etcd                │
│  ├─ DNS proxy        │          │  ├─ dnsmasq                     │
│  ├─ virtual IP pool  │          │  └─ (fips?)                     │
│  └─ nftables NAT     │          │                                 │
└─────────────────────┘          │  Nostr Projector                │
                                  │  ├─ publishDNSEndpoint()        │
                                  │  ├─ publishDNSZone()            │
                                  │  └─ publishDNSBackend()         │
                                  │                                 │
                                  │  Discovery Resolver             │
                                  │  └─ pkg/discovery/resolver.go   │
                                  └─────────────────────────────────┘
```

### 3.2 Natural Connection Points

**A. FIPS adverts → Bahia worker discovery**

FIPS Kind 37195 overlay adverts publish transport endpoints for mesh nodes. Bahia already tracks workers by `PubKey` and discovers them via kind 10100 heartbeats. A FIPS node running on a worker machine publishes its overlay advert with the same Nostr identity. Bahia can subscribe to Kind 37195 events and correlate them with existing workers by pubkey match, or create new worker entries for unknown FIPS nodes.

**B. Bahia endpoint events → FIPS host aliases**

Bahia publishes Kind 31976 `DNSEndpointState` events with FQDN, address, health, and capabilities. A FIPS node (or a bridge daemon) can subscribe to these events and write corresponding entries to `/etc/fips/hosts`, making Bahia-managed services reachable as `<service>.fips` from any FIPS mesh node.

**C. Bahia DNS backend → FIPS hosts file**

Bahia's `Backend` interface (`SyncZone()`) could target FIPS's `/etc/fips/hosts` file directly, treating it as a lightweight DNS backend. This is a natural extension of the existing `dnsmasq` adapter pattern (write config file + reload).

**D. FIPS gateway → Bahia DNS backends**

The `fips-gateway` DNS proxy resolves `.fips` names via the FIPS daemon's built-in resolver. Bahia's `dnsmasq` adapter could generate forwarding rules that send `.fips` queries to the FIPS daemon, making FIPS mesh destinations reachable from Bahia-managed LAN infrastructure.

**E. FIPS as transport for Bahia workers**

Workers behind NAT or on disconnected networks could use the FIPS mesh as their connectivity layer. Bahia would address them by npub (via `.fips` DNS) rather than by IP. The DNS projector would emit `fd00::/8` AAAA records pointing to FIPS overlay addresses rather than traditional IPv4 A records.

---

## 4. FIPS as a DNS Backend Adapter

### 4.1 Concept

A new `fips` backend adapter would implement Bahia's `Backend` interface and sync projected DNS records to FIPS's resolution layer. FIPS resolves `.fips` names through two mechanisms:

1. **`/etc/fips/hosts`** — Static name→npub mappings (human-friendly aliases)
2. **Built-in DNS resolver** — Direct `<npub>.fips` resolution via identity cache priming

The `hosts` file is the only externally writable resolution surface. The built-in resolver requires the identity cache to be primed (via DNS lookup or inbound session), and there is no API to inject entries externally.

### 4.2 `/etc/fips/hosts` as a Backend Target

**How it works**: The FIPS hosts file maps human-friendly names to npubs:

```
# /etc/fips/hosts
drydock.fips  npub1abc...xyz
embeddings.fips  npub1def...uvw
```

When a FIPS node resolves `drydock.fips`, the daemon:
1. Looks up `drydock.fips` in the hosts file → finds the npub
2. Derives the `fd00::/8` IPv6 address from the npub
3. Primes the identity cache
4. Returns the IPv6 address

**Adapter implementation**:

```go
// bahia/internal/adapters/dns/fips.go (proposed)
package dns

import (
    "context"
    "os"
    "github.com/openagentsinc/bahia/internal/domain"
)

type FIPSBackend struct {
    hostsPath string  // default: "/etc/fips/hosts"
}

func (b *FIPSBackend) BackendType() domain.DNSBackendType {
    return "fips"  // new DNSBackendType constant
}

func (b *FIPSBackend) SyncZone(ctx context.Context, zone domain.DNSZone,
    records []domain.DNSRecord) error {
    // Convert DNSRecords to FIPS hosts entries
    // Only records whose Value is an npub or whose metadata
    // contains a worker_pubkey can be mapped
    // Write atomically to hostsPath
}
```

### 4.3 The Mapping Problem

Here is the fundamental challenge: **Bahia DNS records contain IP addresses, not npubs.** The projection pipeline (`dns_projector.go`) extracts addresses from `RuntimeObservation.ObservedHost`, `LLMRouteState.BackendEndpoint`, `Worker.RuntimeTarget.PublicBaseURL`, etc. These are all IP addresses or hostnames.

FIPS's hosts file needs npub→name mappings, not IP→name mappings. The translation requires knowing which npub corresponds to which IP address — information that only exists when:

1. The worker has a `WorkerPubkey` field (available for `DNSEndpointFamilyWorker` endpoints)
2. The endpoint was materialized from a source that carries a pubkey

**Today's data flow**:
```
Worker.PubKey = "npub1abc..."
Worker.RuntimeTarget.PublicBaseURL = "http://10.0.1.44"
  → DNSEndpoint { WorkerPubkey: "npub1abc...", Address: "10.0.1.44" }
    → DNSRecord { Value: "10.0.1.44" }  // pubkey is lost here
```

The `DNSEndpoint` type already carries `WorkerPubkey`, but `DNSRecord` does not. For a FIPS backend, the adapter would need to work at the `DNSEndpoint` level rather than the `DNSRecord` level, or the record would need to carry the pubkey in metadata.

### 4.4 Feasibility Assessment

| Aspect | Assessment |
|---|---|
| Worker endpoints | **Feasible** — `DNSEndpoint.WorkerPubkey` provides the npub directly |
| Service endpoints | **Partial** — only if the backing worker's pubkey is known (requires join through `EnvironmentServiceState` → `Worker`) |
| LLM route endpoints | **Partial** — `LLMRouteState` doesn't carry worker pubkey today |
| ML inference endpoints | **Partial** — same limitation as LLM routes |
| Non-worker endpoints | **Not feasible** — no npub available for external services |

### 4.5 Recommendation

A FIPS backend adapter is **feasible but limited** to worker-sourced endpoints. Rather than implementing a full `Backend` adapter that participates in the reconciler loop, a better approach is a **standalone bridge process** that subscribes to Bahia's Kind 31976 endpoint events and writes matching entries to `/etc/fips/hosts`. This:

- Decouples the FIPS hosts file lifecycle from Bahia's reconciler
- Works over Nostr (no direct filesystem access needed between systems)
- Can run on any FIPS node, not just the machine running Bahia
- Can filter by capability, environment, or health before writing

See [Phase A](#phase-a--read-only-nostr-event-bridge) for the concrete proposal.

---

## 5. FIPS as a Transport Layer for Bahia

### 5.1 Concept

Workers and services could communicate over the FIPS mesh instead of (or in addition to) the traditional IP network. This changes how Bahia addresses endpoints:

- **Traditional**: `drydock-review.prod.cascadia → A 10.0.1.44`
- **FIPS mesh**: `drydock-review.prod.cascadia → AAAA fd00:xx:xx:...` (FIPS overlay address)

### 5.2 How Workers Would Use FIPS

A Bahia worker running the FIPS daemon would:

1. Generate or load a FIPS keypair (same as its Nostr identity)
2. Connect to the mesh via configured peers or Nostr discovery
3. Get a `fd00::/8` overlay address on `fips0`
4. Publish a Kind 37195 advert with its transport endpoints
5. Report its FIPS overlay address as its `PublicBaseURL`

The worker's `RuntimeTarget.PublicBaseURL` would be `http://[fd00:xx:xx:...]:8000` rather than `http://10.0.1.44:8000`.

### 5.3 Impact on DNS Projection

The DNS projector (`dns_projector.go`) calls `parseEndpointTarget()` to extract protocol, address, and port from endpoint URLs. This function already handles IPv6 addresses (`net.SplitHostPort` with bracket stripping). The `dnsRecordType()` function already detects IPv6 and returns `DNSRecordTypeAAAA`.

**No projector changes needed** — if a worker reports a `fd00::/8` address as its public URL, the projector will naturally emit an AAAA record.

### 5.4 NAT Traversal Advantage

The strongest use case for FIPS transport is **workers behind NAT**. Today, Bahia workers must be reachable at their `PublicBaseURL`, which requires port forwarding, VPN, or public IP. With FIPS:

- The worker connects outbound to mesh peers or uses Nostr-mediated NAT hole-punching (Kind 21059)
- Once on the mesh, it's reachable at its npub/overlay address from any other mesh node
- Bahia's DNS records point to the overlay address
- Any client on the mesh (or behind a `fips-gateway`) can reach the worker

This eliminates the need for VPN, Tailscale, or manual port forwarding for worker connectivity.

### 5.5 Impact on DNS Resolution Path

For clients to resolve Bahia DNS names pointing to FIPS overlay addresses, one of these must be true:

1. The client is on the FIPS mesh (has `fips0` with `fd00::/8` route)
2. The client is behind a `fips-gateway` (gateway does DNS proxy + NAT)
3. A Bahia DNS backend (CoreDNS, dnsmasq) serves the AAAA record, and the client has mesh connectivity separately

Option 2 is the most practical for non-FIPS clients — the `fips-gateway` already provides exactly this bridging function.

### 5.6 Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Latency overhead from mesh routing | Medium | Mesh adds per-hop encryption overhead; measure before committing |
| MTU reduction (effective IPv6 MTU ~1395 for UDP/Ethernet) | Low | FIPS handles PMTUD automatically; may affect large model transfers |
| Single-transport dependency | Medium | FIPS supports multi-transport; workers can maintain both IP and mesh paths |
| Key rotation breaks connectivity | Low | FIPS identity change = new node_addr + IPv6 = DNS must update; reconciler handles this naturally |

---

## 6. Bidirectional Nostr Event Bridge

### 6.1 Architecture

The event bridge is the highest-value, lowest-risk integration. Both systems already publish structured events to Nostr relays. The bridge simply subscribes to one system's events and translates them for the other.

```
┌──────────────────┐     Nostr Relays     ┌──────────────────────┐
│    FIPS Nodes     │◄──────────────────►  │       Bahia          │
│                   │                      │                      │
│  Kind 37195       │─────subscribe────────│  FIPS Advert         │
│  (overlay advert) │                      │  Subscriber          │
│                   │                      │  (new component)     │
│                   │                      │       │               │
│                   │                      │       ▼               │
│                   │                      │  Worker/Endpoint      │
│                   │                      │  registration         │
│                   │                      │                      │
│  /etc/fips/hosts  │◄────subscribe────────│  Kind 31976           │
│  (written by      │                      │  (endpoint state)    │
│   bridge daemon)  │                      │                      │
└──────────────────┘                      └──────────────────────┘
```

### 6.2 Direction A: FIPS → Bahia (Advert Ingestion)

**What**: Bahia subscribes to Kind 37195 events from known FIPS relays and creates or updates worker entries.

**Subscription filter**:
```go
nostr.Filter{
    Kinds: []int{37195},
    Tags:  nostr.TagMap{"#d": {"fips-overlay-v1"}},
}
```

Optionally filter by `protocol` tag to scope to a specific `app` namespace (e.g., `fips-overlay-v1` or a custom value like `bahia-mesh-v1`).

**Event processing**:

1. Extract `pubkey` from the event (this is the FIPS node's Nostr identity)
2. Parse the `OverlayAdvert` JSON content for transport endpoints
3. Check if a worker with matching `PubKey` already exists in Bahia
4. If exists: update the worker's transport metadata, mark as mesh-reachable
5. If not exists: optionally create a new worker entry (configurable policy — auto-register vs allowlist)
6. Derive the FIPS overlay IPv6 address from the pubkey for DNS projection
7. Trigger DNS reconciler to project mesh-routable AAAA records

**Touch points in Bahia**:

| File | Change |
|---|---|
| `internal/adapters/nostr/fips_subscriber.go` | New — subscribes to Kind 37195, parses `OverlayAdvert` |
| `internal/domain/worker.go` | Add `MeshEndpoints []MeshEndpoint` field to `Worker` |
| `internal/domain/dns.go` | Add `DNSEndpointFamilyMesh` family constant |
| `internal/reconcile/dns_projector.go` | Add `projectMeshEndpoints()` — Rule 6 for FIPS-sourced endpoints |
| `internal/config/config.go` | Add `FIPS` config section (relay URLs, app namespace, auto-register policy) |
| `internal/app/app.go` | Wire FIPS subscriber into app lifecycle |

**New domain type**:
```go
type MeshEndpoint struct {
    Transport string `json:"transport"` // "udp", "tcp", "tor"
    Address   string `json:"address"`   // "1.2.3.4:2121" or "nat" or ".onion:8443"
}
```

### 6.3 Direction B: Bahia → FIPS (Endpoint Catalog)

**What**: A bridge daemon (running on FIPS nodes or as a standalone process) subscribes to Bahia's Kind 31976 endpoint events and writes matching entries to `/etc/fips/hosts`.

**This is NOT a Bahia-side change** — it's a new small daemon that could live in either codebase or as a standalone tool. It uses Bahia's existing Nostr publication (no modifications needed to Bahia).

**Bridge daemon logic**:

```
Subscribe to Kind 31976 from Bahia's pubkey
  → For each endpoint event:
    1. Extract FQDN, address, health, capabilities from content
    2. Check health == "healthy" (skip unhealthy)
    3. Look up worker_pubkey from event tags or metadata
    4. If worker_pubkey found:
       Write: "<service-name>.fips  <npub>" to /etc/fips/hosts
    5. If worker_pubkey NOT found but address is fd00::/8:
       Reverse-derive? Not possible (SHA-256 is one-way)
       → Skip or use address directly (not useful for hosts file)
```

**Key limitation**: The bridge can only write hosts entries for endpoints that have an associated npub. Bahia's Kind 31976 events currently include `host` and `addr` tags but not `worker_pubkey` as a tag. The content JSON doesn't include it either.

**Required Bahia change**: Add a `npub` or `worker_pubkey` tag to Kind 31976 events when the endpoint has one.

| File | Change |
|---|---|
| `internal/adapters/nostr/projector.go` | In `dnsEndpointTags()`, add `["npub", endpoint.WorkerPubkey]` tag when non-empty |

This is a one-line addition to the tag builder.

### 6.4 Shared Relay Infrastructure

Both systems use Nostr relays. For the event bridge to work, both must share at least one relay. Options:

1. **Shared public relays**: Both configure the same public relay (e.g., `wss://relay.damus.io`). Simple but exposes infrastructure events publicly.
2. **Dedicated infrastructure relay**: Run a private Nostr relay for Bahia + FIPS coordination. Better security, but adds infrastructure.
3. **Mixed**: FIPS overlay adverts on public relays (they're already public by design), Bahia endpoint events on a private relay with the bridge daemon having access to both.

**Recommendation**: Option 3 (mixed) aligns with the existing security models of both systems. FIPS adverts are intentionally public; Bahia endpoint events contain infrastructure addresses that should be private.

---

## 7. Gateway Integration

### 7.1 fips-gateway and Bahia DNS Backends

The `fips-gateway` bridges unmodified LAN clients to the FIPS mesh. Its outbound half works by:

1. LAN client queries `.fips` DNS → gateway's DNS proxy (`[::1]:5353`)
2. Gateway forwards to FIPS daemon resolver (`[::1]:5354`)
3. Daemon returns `fd00::/8` AAAA for the npub
4. Gateway allocates a virtual IP from `fd01::/112` pool
5. nftables DNAT rewrites destination from virtual IP to mesh address
6. Masquerade rewrites source to gateway's `fips0` address

**Key file**: `fips/docs/design/fips-gateway.md`

### 7.2 Integration Scenario: LAN Clients → Bahia Services via FIPS

A LAN client behind a `fips-gateway` should be able to reach Bahia-managed services that run on FIPS mesh nodes. The flow:

```
LAN client
  → resolves "drydock-review.fips" via fips-gateway DNS proxy
  → gateway forwards to fips daemon
  → daemon looks up /etc/fips/hosts (populated by Bahia→FIPS bridge)
  → finds npub for "drydock-review"
  → returns fd00::/8 address
  → gateway allocates virtual IP, installs NAT
  → traffic flows over mesh to the worker running drydock-review
```

This requires only the Bahia→FIPS bridge daemon (§6.3) to populate `/etc/fips/hosts` — no changes to `fips-gateway` itself.

### 7.3 Integration Scenario: Bahia dnsmasq → FIPS Gateway Forwarding

If Bahia manages a `dnsmasq` backend on a machine that also runs `fips-gateway`, Bahia's dnsmasq adapter could generate forwarding rules for `.fips` queries:

```
# /etc/dnsmasq.d/fips-forward.conf (generated by Bahia)
server=/fips/::1#5353
```

This tells dnsmasq to forward all `.fips` queries to the FIPS gateway's DNS proxy. Combined with Bahia's own zone records, a client could resolve both:

- `drydock-review.prod.cascadia` → traditional DNS (via Bahia's CoreDNS/dnsmasq)
- `drydock-review.fips` → FIPS mesh DNS (via dnsmasq forwarding to fips-gateway)

**Touch point**: The Bahia dnsmasq adapter (planned for Phase 1) could optionally emit a `.fips` forwarding rule when FIPS integration is enabled in config.

| File | Change |
|---|---|
| `internal/adapters/dns/dnsmasq.go` | Add optional `.fips` forwarding line in generated config |
| `internal/config/config.go` | Add `dns.fips_forwarding.enabled` and `dns.fips_forwarding.gateway_addr` config |

### 7.4 Port Forwarding: Bahia Services Exposed on Mesh

The `fips-gateway` inbound half exposes LAN services on the gateway's mesh-side address via `port_forwards[]` config. Bahia could generate these port-forward entries for services that should be mesh-reachable:

```yaml
# /etc/fips/fips.yaml gateway section (generated or augmented by Bahia)
gateway:
  port_forwards:
    - listen_port: 8000
      proto: tcp
      target: "[fd02::20]:8000"  # LAN service address
```

This is a stretch goal — it requires Bahia to write FIPS config, which crosses a significant operational boundary. Better to keep this as manual operator configuration informed by Bahia's service catalog.

---

## 8. Detailed Phase Plan

### Phase A — Read-Only Nostr Event Bridge

**Scope**: Bahia reads FIPS adverts; a standalone bridge reads Bahia endpoints and writes FIPS hosts.

**Duration**: Smallest useful integration. No protocol changes in either system.

#### A.1: Bahia subscribes to FIPS Kind 37195

**New files**:

| File | Purpose |
|---|---|
| `bahia/internal/adapters/nostr/fips_subscriber.go` | Subscribes to Kind 37195 on configured relays; parses `OverlayAdvert`; correlates with workers by pubkey |
| `bahia/internal/adapters/nostr/fips_subscriber_test.go` | Unit tests for advert parsing, pubkey matching, overlay address derivation |

**Modified files**:

| File | Change |
|---|---|
| `bahia/internal/config/config.go` | Add `FIPS` section: `enabled`, `relay_urls`, `app_namespace`, `auto_register_workers` |
| `bahia/internal/domain/worker.go` | Add `FIPSOverlayAddr string` and `FIPSEndpoints []FIPSTransportEndpoint` to `Worker` |
| `bahia/internal/app/app.go` | Start FIPS subscriber when `config.FIPS.Enabled` |

**Behavior**:

- On receiving a Kind 37195 event, extract pubkey and overlay advert
- Look up existing worker by `PubKey` match
- If found: update `Worker.FIPSOverlayAddr` with derived `fd00::/8` address, store transport endpoints
- If not found and `auto_register_workers` is true: create a minimal worker entry
- Workers with FIPS overlay addresses become eligible for mesh-routed DNS projection

**Does NOT change**: DNS projector, reconciler, backend adapters. The overlay address is stored on the worker but not yet used for DNS projection.

#### A.2: Standalone bridge populates FIPS hosts

**New project**: `fips-bahia-bridge` (could be a small Rust binary in the FIPS repo, or a Go binary in the Bahia repo)

**Behavior**:

1. Subscribe to Kind 31976 from Bahia's configured pubkey on shared relays
2. For each endpoint event with `health == "healthy"`:
   - Extract FQDN and worker npub (from the new `npub` tag, added in §6.3)
   - If npub present: write `<service-label>.fips  <npub>` to `/etc/fips/hosts`
3. On endpoint health → unhealthy or tombstone: remove from hosts file
4. Atomic rewrite of hosts file (write temp + rename)
5. No signal needed — FIPS daemon reads hosts file on each DNS query

**Required Bahia change for A.2**:

| File | Change |
|---|---|
| `bahia/internal/adapters/nostr/projector.go` | Add `["npub", endpoint.WorkerPubkey]` to `dnsEndpointTags()` when non-empty |

#### A.3: Tag Bahia endpoint events for FIPS discoverability

Add `["mesh", "fips"]` tag to Kind 31976 events for endpoints that have FIPS overlay connectivity. This lets the bridge daemon efficiently filter:

```go
nostr.Filter{
    Kinds:   []int{31976},
    Authors: []string{bahiaPubkey},
    Tags:    nostr.TagMap{"#mesh": {"fips"}},
}
```

**Validates**: Event schemas are correct, pubkey correlation works, hosts file entries produce working `.fips` resolution.

---

### Phase B — FIPS-Aware DNS Projection

**Scope**: Bahia's DNS projector emits AAAA records pointing to FIPS overlay addresses for mesh-connected workers.

**Depends on**: Phase A (FIPS overlay addresses stored on workers)

#### B.1: New projection rule — mesh-routed endpoints

**Modified files**:

| File | Change |
|---|---|
| `bahia/internal/reconcile/dns_projector.go` | Add projection Rule 6: Workers with `FIPSOverlayAddr` emit AAAA records pointing to `fd00::/8` overlay address in a mesh-specific zone |
| `bahia/internal/domain/dns.go` | Add `DNSEndpointFamilyMesh DNSEndpointFamily = "mesh"` |

**Projection Rule 6: Mesh-routed worker → AAAA record**:

```
Input:
  worker.Name              = "t7920-l40s"
  worker.FIPSOverlayAddr   = "fd00:ab:cd:..."
  zone                     = "mesh.cascadia"  (new zone type)

Output:
  DNSEndpoint.FQDN     = "t7920-l40s.mesh.cascadia"
  DNSRecord.Type        = "AAAA"
  DNSRecord.Value       = "fd00:ab:cd:..."
```

This is complementary to the existing worker Rule 4 (which emits A records for traditional IP addresses). A worker could have both:
- `t7920-l40s.edge.cascadia → A 10.0.1.44` (traditional)
- `t7920-l40s.mesh.cascadia → AAAA fd00:ab:cd:...` (FIPS mesh)

#### B.2: Zone visibility for mesh zones

Add a new `ZoneVisibility` value:

```go
ZoneVisibilityMesh ZoneVisibility = "mesh"  // reachable via FIPS mesh only
```

This lets policies filter mesh endpoints separately from traditional endpoints.

#### B.3: Config additions

```yaml
dns:
  zones:
    - name: "mesh.cascadia"
      visibility: mesh
      backend: filesystem  # or coredns if serving to mesh clients
  
  projection:
    mesh_endpoints: true       # enable Rule 6
    mesh_zone: "mesh.cascadia" # target zone for mesh endpoints
```

**Validates**: Workers with FIPS overlay get dual DNS records (traditional IP + mesh overlay). Clients on the mesh can resolve via the mesh zone.

---

### Phase C — Deep Integration

**Scope**: Shared identity model, mesh-aware health monitoring, bidirectional service discovery.

**Depends on**: Phase B, operational experience with mesh routing

#### C.1: Unified identity model

Today, Bahia workers have a `PubKey` (Nostr hex pubkey) and FIPS nodes have the same keypair. In Phase C, Bahia would:

- Accept FIPS node registration directly via Kind 37195 (already done in Phase A)
- Use the FIPS-derived IPv6 address as a first-class worker address
- Correlate FIPS mesh metrics (MMP reports — RTT, loss, jitter, goodput) with Bahia's worker health model

**Touch points**:

| File | Change |
|---|---|
| `bahia/internal/domain/worker.go` | Add `MeshHealth` struct with RTT, loss, jitter from MMP |
| `bahia/internal/reconcile/dns_projector.go` | Health gating considers mesh health for mesh-zone endpoints |

#### C.2: Mesh-aware DNS projection with topology hints

FIPS nodes have spanning-tree coordinates. If exposed (e.g., in an extended Kind 37195 advert), Bahia could use coordinates for topology-aware DNS:

- Route clients to the topologically nearest service instance
- Weight SRV records by mesh hop count
- Exclude endpoints with high mesh loss from DNS projection

This requires FIPS to expose coordinate information — currently coordinates are internal to the mesh routing layer and not published in adverts. This would require a FIPS protocol extension.

**Recommendation**: Defer until operational data validates the need. Mesh topology changes dynamically; baking it into DNS TTLs (which are inherently slow to propagate) may not be worth the complexity.

#### C.3: FIPS backend adapter (full implementation)

If Phase A/B validation shows that many endpoints need FIPS hosts entries, implement a proper `Backend` adapter:

```go
// bahia/internal/adapters/dns/fips.go
type FIPSBackend struct {
    hostsPath    string
    reloadSignal func() error // optional: signal fips daemon
}

func (b *FIPSBackend) BackendType() domain.DNSBackendType {
    return DNSBackendTypeFIPS
}

func (b *FIPSBackend) SyncZone(ctx context.Context, zone domain.DNSZone,
    records []domain.DNSRecord) error {
    // Filter records that have associated npubs (via SourceCoordinate → endpoint lookup)
    // Write to hostsPath atomically
}

func (b *FIPSBackend) ListRecords(ctx context.Context,
    zone domain.DNSZone) ([]domain.DNSRecord, error) {
    // Parse /etc/fips/hosts, return as DNSRecords
}
```

This is a Phase C item because:
1. The standalone bridge (Phase A) is simpler and sufficient for most cases
2. The adapter requires solving the npub→record mapping problem (§4.3)
3. The adapter couples Bahia to the FIPS hosts file format

---

## 9. Security Considerations

### 9.1 Identity Alignment

Both systems use secp256k1 keypairs, but with different trust models:

| Aspect | FIPS | Bahia |
|---|---|---|
| Key generation | Node-local | Node-local |
| Key storage | `/etc/fips/fips.key` | Bahia's key management |
| Trust establishment | FMP Noise IK handshake (peer-to-peer) | `AuthorizedPubkeys` list |
| Identity scope | Network address | Worker identity + event signing |

**Risk**: A compromised FIPS key allows an attacker to publish fake overlay adverts (Kind 37195), which would cause Bahia to register a rogue worker or update a legitimate worker's mesh address to point to the attacker.

**Mitigation**: 
- Bahia's FIPS subscriber should validate adverts against an allowlist of trusted npubs (not auto-register by default)
- The `auto_register_workers` config should default to `false`
- Adverts from unknown npubs should be logged but not acted upon without operator approval

### 9.2 Trust Boundaries

```
┌─────────────────────────────────────────────────────────────┐
│                    Trust Boundary 1                         │
│                 (Bahia's AuthorizedPubkeys)                 │
│                                                             │
│  Bahia control plane events (Kind 594x/694x/794x)           │
│  Only processed from authorized operators                   │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│                    Trust Boundary 2                         │
│                   (FIPS Peer ACL)                           │
│                                                             │
│  FIPS mesh connections (Noise IK handshake)                 │
│  Only accepted from peers in /etc/fips/peers.allow          │
│  (or all peers if ACL is not configured)                    │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│                    Trust Boundary 3                         │
│              (Nostr Relay — shared surface)                 │
│                                                             │
│  Both systems publish/subscribe here                        │
│  Events are signed but relay operators see metadata         │
│  Adverts (Kind 37195) are public by design                  │
│  Endpoint events (Kind 31976) contain infrastructure addrs  │
└─────────────────────────────────────────────────────────────┘
```

**Key insight**: The Nostr relay is the shared attack surface. Relay operators can observe which npubs are publishing adverts and endpoint events, and the timing of that traffic. They cannot forge events (signatures prevent this) but can selectively withhold them.

### 9.3 Endpoint Event Privacy

Bahia's Kind 31976 events contain IP addresses, port numbers, and service topology. Publishing these to public relays exposes infrastructure details. The FIPS event bridge should use a private relay or NIP-42 authenticated relay for Bahia endpoint events.

FIPS overlay adverts (Kind 37195) are intentionally public — they advertise how to reach a node. There is no privacy concern unique to the integration.

### 9.4 Key Management

If a FIPS node and a Bahia worker share the same keypair:
- Rotating the FIPS key changes the node's mesh address (`fd00::/8`) and requires DNS updates
- The Bahia worker's `PubKey` must be updated simultaneously
- The Kind 37195 advert will be re-published under the new key
- The reconciler will detect the address change and update DNS records

If they use different keypairs:
- Correlation is by convention (e.g., both stored in the same config)
- No cryptographic binding between "this worker" and "this mesh node"
- An operator could misconfigure the mapping

**Recommendation**: Use the same keypair. Both systems derive identity from secp256k1; using one key for both eliminates correlation risk.

---

## 10. Risks and Alternatives

### 10.1 Risks

| Risk | Severity | Likelihood | Phase | Mitigation |
|---|---|---|---|---|
| FIPS mesh adds latency that degrades service SLAs | Medium | Medium | B/C | Benchmark before committing; keep traditional IP path as fallback |
| Relay unavailability breaks event bridge | Medium | Low | A | Redundant relays; local cache of last-known-good state |
| `/etc/fips/hosts` file conflicts with manual entries | Low | Medium | A | Bridge daemon manages a marked section; preserves manual entries |
| Kind number collision if FIPS or Bahia evolve independently | Low | Low | A | Both systems use distinct ranges (371xx vs 319xx); document in shared NIP |
| FIPS protocol changes break advert parsing | Medium | Low | A | Pin `version` field; validate before processing |
| Scaling — too many endpoints overflow FIPS hosts file | Low | Low | A/B | Hosts file is simple text; 10K entries is ~500KB; not a real concern |
| DNS TTL vs mesh topology churn mismatch | Medium | Medium | B | Mesh topology changes faster than DNS TTL allows; use low TTL for mesh zone |

### 10.2 What's Not Worth Doing

**A. FIPS as a CoreDNS plugin**: Building a CoreDNS plugin that resolves from FIPS adverts would duplicate the bridge daemon's logic inside CoreDNS's plugin framework. The bridge daemon + standard zone file is simpler.

**B. Bahia publishing Kind 37195 adverts**: Bahia is an orchestrator, not a mesh node. It doesn't have transport endpoints to advertise. Publishing fake adverts would confuse the FIPS mesh.

**C. Implementing FIPS mesh routing in Go inside Bahia**: This would duplicate FIPS's Rust mesh stack. Use the FIPS daemon as-is; Bahia interacts only via Nostr events and DNS.

**D. Real-time DNS updates from mesh topology changes**: FIPS mesh topology changes (spanning tree reconvergence, peer loss) happen in seconds. DNS propagation (TTL-bounded) happens in minutes. Trying to make DNS track mesh topology in real-time is futile. Use FIPS's native routing for real-time reachability; use DNS only for service discovery (name → which npub).

**E. Encrypting FIPS hosts file entries**: The hosts file maps names to npubs. The npub is already public (it's the node's address). There's nothing to protect.

### 10.3 Alternative: Direct FIPS Integration Without DNS

Instead of bridging through DNS, applications could resolve services directly from Nostr events:

- Bahia's `pkg/discovery/resolver.go` already does this — it subscribes to Kind 31976 and maintains a live endpoint cache
- A FIPS-side equivalent could subscribe to Kind 31976 and resolve services by name to npub, then use FIPS's native datagram API

This bypasses DNS entirely and is the long-term vision mentioned in Bahia's DNS design doc (§16, "Nostr-native DNS resolution"). It's complementary to the DNS integration, not a replacement — traditional DNS remains necessary for unmodified applications.

---

## 11. Summary: Recommended Integration Sequence

| Phase | Scope | Effort | Risk | Value |
|---|---|---|---|---|
| **A** | Read-only event bridge | Small (1-2 weeks) | Low | FIPS nodes can resolve Bahia services; Bahia discovers mesh workers |
| **B** | FIPS-aware DNS projection | Medium (1-2 weeks) | Medium | Bahia emits mesh-routable AAAA records; dual-stack DNS for workers |
| **C** | Deep integration | Large (4+ weeks) | Medium | Unified identity, mesh health in DNS, full backend adapter |

**Start with Phase A.** It delivers the core value (bidirectional service discovery) with minimal code changes and no protocol modifications. Phase B adds DNS-native mesh routing. Phase C is only worth pursuing after operational validation of A and B.

---

## Appendix A: Nostr Kind Registry (Integration Additions)

No new Nostr kinds are needed for any phase. The integration uses existing kinds from both systems:

| Kind | System | Used For (Integration) |
|---|---|---|
| 37195 | FIPS | Bahia subscribes to discover mesh nodes |
| 31976 | Bahia | FIPS bridge subscribes to populate hosts file |
| 31975 | Bahia | FIPS bridge optionally reads zone definitions |
| 10050 | FIPS | Bahia could publish inbox relays for bidirectional signaling (future) |

## Appendix B: Configuration Shape (Phase A)

```yaml
# bahia config additions
fips:
  enabled: false
  relay_urls:
    - "wss://relay.damus.io"
    - "wss://nos.lol"
  app_namespace: "fips-overlay-v1"  # matches FIPS's protocol tag
  auto_register_workers: false       # require allowlist for new workers
  allowed_npubs: []                  # empty = accept all (when auto_register is true)
  overlay_address_prefix: "fd00"     # FIPS ULA prefix

# fips-bahia-bridge config (standalone daemon)
bridge:
  bahia_pubkey: "<bahia-service-npub>"
  relay_urls:
    - "wss://private-relay.example.com"
  hosts_path: "/etc/fips/hosts"
  managed_section_marker: "# bahia-managed"
  health_filter: true                # only write healthy endpoints
  capability_filter: []              # empty = all capabilities
  environment_filter: []             # empty = all environments
```

## Appendix C: FIPS Overlay Address Derivation (Go)

For Phase A's FIPS subscriber in Bahia, deriving the overlay address from a pubkey:

```go
import (
    "crypto/sha256"
    "encoding/hex"
    "net"
)

// FIPSOverlayAddress derives the fd00::/8 IPv6 address from a Nostr hex pubkey.
func FIPSOverlayAddress(hexPubkey string) (net.IP, error) {
    pubkeyBytes, err := hex.DecodeString(hexPubkey)
    if err != nil {
        return nil, fmt.Errorf("decode pubkey: %w", err)
    }
    if len(pubkeyBytes) != 32 {
        return nil, fmt.Errorf("pubkey must be 32 bytes, got %d", len(pubkeyBytes))
    }
    
    hash := sha256.Sum256(pubkeyBytes)
    nodeAddr := hash[:16] // truncated to 16 bytes
    
    // IPv6 = fd + nodeAddr[0..15]
    ip := make(net.IP, 16)
    ip[0] = 0xfd
    copy(ip[1:], nodeAddr[:15])
    return ip, nil
}
```

## Appendix D: File Touch Points Summary

### Bahia (Go)

| File | Phase | Change |
|---|---|---|
| `internal/adapters/nostr/fips_subscriber.go` | A | New — Kind 37195 subscription + advert parsing |
| `internal/adapters/nostr/projector.go` | A | Add `npub` tag to Kind 31976 for worker endpoints |
| `internal/domain/worker.go` | A | Add `FIPSOverlayAddr`, `FIPSEndpoints` fields |
| `internal/config/config.go` | A | Add `FIPS` config section |
| `internal/app/app.go` | A | Wire FIPS subscriber lifecycle |
| `internal/domain/dns.go` | B | Add `DNSEndpointFamilyMesh`, `ZoneVisibilityMesh` |
| `internal/reconcile/dns_projector.go` | B | Add projection Rule 6 (mesh endpoints → AAAA) |
| `internal/adapters/dns/dnsmasq.go` | B | Optional `.fips` forwarding rule generation |
| `internal/adapters/dns/fips.go` | C | Full FIPS backend adapter |

### FIPS (Rust) — No changes required for Phase A/B

The bridge daemon is a standalone process that reads from Nostr and writes to `/etc/fips/hosts`. It does not modify the FIPS daemon or protocol.

### Standalone Bridge

| File | Phase | Purpose |
|---|---|---|
| `fips-bahia-bridge/src/main.rs` (or Go equivalent) | A | Subscribe to Kind 31976, write `/etc/fips/hosts` |
| `fips-bahia-bridge/src/hosts.rs` | A | Atomic hosts file writer with managed-section support |
