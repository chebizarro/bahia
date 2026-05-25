# DNS Orchestration Layer — Design Doc

> **Status**: Draft
> **Date**: 2026-05-21
> **Depends on**: `nostr-native-system-discovery.md` (kind 31974 bootstrap)
> **Prerequisite**: Phase 0 of system discovery (shared builder, kind 31974 publication)

---

## 1. Context & Scope

### Problem

Bahia already owns the canonical infrastructure graph — services, environments, workers, ML inference endpoints, runtime observations, drift state, and deployment topology. DNS records for these resources are today managed manually or not at all. When a deployment completes, when a worker comes online, or when an ML endpoint becomes healthy, the corresponding DNS records must be created or updated by hand. This is error-prone, slow, and disconnects DNS from the source of truth.

### Goal

Make DNS a **derived projection** of Bahia's infrastructure graph, not a manually managed configuration system. Bahia should:

- Automatically synthesize DNS records from deployment state, worker topology, and endpoint health
- Publish DNS mutations as signed, replayable Nostr events
- Sync projected records to pluggable DNS backends (CoreDNS, PowerDNS, dnsmasq, etc.)
- Detect and remediate drift between projected and actual DNS state
- Support split-horizon policies, edge routing, and environment-scoped zones
- Expose service catalogs that unify DNS, API discovery, runtime discovery, and MCP discovery

### What Bahia should NOT do

- Recursively resolve internet DNS
- Implement a DNS protocol stack
- Replace CoreDNS / Unbound / PowerDNS / BIND
- Directly serve DNS queries
- Become a service mesh proxy

### Key insight

Bahia already has service topology, deployment state, worker capability state, runtime endpoints, health status, route state, and the full infrastructure graph. DNS is **derived infrastructure state** — one projection of that graph among many (gateway config, deployment topology, service catalogs, runtime routing). The architecture treats it that way.

### Data flow

```
Nostr event graph (workers, services, endpoints, observations)
    ↓
Materialized state (EnvironmentServiceState, MLInferenceState, Worker)
    ↓
DNS projection layer (derives records from topology)
    ↓
DNS backend adapters (sync to actual DNS servers)
    ↓
CoreDNS / PowerDNS / dnsmasq / Consul / etcd / filesystem mock
```

---

## 2. Relationship to Existing Systems

### System discovery (kind 31974) is the bootstrap layer

The Nostr-native system discovery design (`docs/designs/nostr-native-system-discovery.md`) establishes how clients find Bahia itself — relay URLs, service pubkeys, feature flags. DNS orchestration builds on top of this: once a client has discovered Bahia via kind 31974, it can subscribe to DNS topology events for runtime endpoint resolution.

**Dependency**: DNS orchestration should ship after system discovery Phase 0 (shared builder, kind 31974/30002 publication). The DNS projection layer consumes the same publisher infrastructure.

### Existing reconcilers provide the pattern

The `Reconciler` (`internal/reconcile/reconciler.go`) and `LLMRouteReconciler` (`internal/reconcile/llm_reconciler.go`) already implement the exact loop DNS needs:

1. List all materialized state entries
2. Observe actual state via a backend adapter
3. Compare desired vs. observed
4. Remediate drift (upsert/delete records)
5. Emit observation events

The DNS reconciler follows this pattern exactly.

### Existing domain types are the data source

| Existing type | DNS relevance |
|---|---|
| `Worker` | Host address, geohash, accelerators, runtime target, health → edge/worker DNS records |
| `EnvironmentServiceState` | Desired artifact + observed state → service DNS records |
| `MLInferenceEndpoint` | Endpoint name, environment, protocol → inference endpoint DNS records |
| `MLInferenceState` | Backend endpoint, health, gateway status → ML DNS records with health gating |
| `LLMRouteState` | Route name, environment, backend endpoint, gateway target → LLM route DNS records |
| `Service` | Name, runtime type, runtime config → service DNS names |
| `Environment` | Name, worker selector, runtime config → zone scoping |
| `RuntimeObservation` | Observed host, health status → health-gated DNS updates |

---

## 3. DNS Event Model

### Design principle

Follow the established Bahia Nostr contract pattern:
- **Replaceable read models** (3197x series) for projected DNS state — clients subscribe once and get live updates
- **Request/status/result** (599x/699x/799x series) for DNS mutation commands — operators issue signed requests, Bahia processes and replies
- **Audit events** (310xx series) for DNS activity logging

### 3.1 Kind allocations

#### Replaceable read-model kinds (continuing from 31974)

| Kind | Name | d-tag pattern | Purpose |
|---|---|---|---|
| 31975 | `DNSZoneState` | `zone:<zone-name>` | Projected zone definition with SOA-level metadata |
| 31976 | `DNSEndpointState` | `endpoint:<service>.<route>.<env>` | Canonical runtime endpoint projection |
| 31977 | `DNSPolicyState` | `dnspolicy:<policy-id>` | Active DNS policy (split-horizon, TTL, routing) |
| 31978 | `DNSBackendState` | `dnsbackend:<backend-id>` | DNS backend health and sync status |

#### Audit event kinds (continuing from 31019)

| Kind | Name | Purpose |
|---|---|---|
| 31020 | `dns.zone_synced` | Zone sync completed to backend |
| 31021 | `dns.record_changed` | Individual record upsert/delete |
| 31022 | `dns.drift_detected` | Projected DNS does not match actual DNS |
| 31023 | `dns.endpoint_registered` | New endpoint materialized from topology |
| 31024 | `dns.endpoint_deregistered` | Endpoint removed (service stopped, worker offline) |

#### Request/status/result kinds

| Kind | Type | Name | Purpose |
|---|---|---|---|
| 5941 | Request | `DNSZoneCreate` | Create or update a DNS zone definition |
| 5942 | Request | `DNSPolicyApply` | Apply a DNS routing/split-horizon policy |
| 5943 | Request | `DNSRecordOverride` | Manual record override (escape hatch) |
| 5944 | Request | `DNSDriftRemediate` | Request remediation of detected DNS drift |
| 5945 | Request | `DNSBackendRegister` | Register a DNS backend adapter |
| 6941 | Status | `DNSOperationStatus` | Progress updates for DNS mutations |
| 7941 | Result | `DNSZoneCreateResult` | Zone creation terminal result |
| 7942 | Result | `DNSPolicyApplyResult` | Policy application terminal result |
| 7943 | Result | `DNSRecordOverrideResult` | Manual override terminal result |
| 7944 | Result | `DNSDriftRemediateResult` | Drift remediation terminal result |
| 7945 | Result | `DNSBackendRegisterResult` | Backend registration terminal result |

**Kind 38397 is NOT used** — it is already allocated to `KindMLInferenceApprovalResult`. The original vision document proposed it for "DNS mutation request" but this conflicts with the existing ML inference control plane.

### 3.2 Why these ranges

The 594x/694x/794x request/status/result range is unallocated in the current reactor. It sits between the core deployment series (596x) and the package series (599x), leaving room for future DNS sub-commands. The 3197x read-model range continues sequentially from `KindSystemDiscovery = 31974`. The 310xx audit range continues from `31019` (the last LLM audit kind).

---

## 4. Domain Types

### 4.1 Canonical endpoint — the core primitive

The canonical endpoint is the atomic unit of DNS projection. It is **derived**, not manually created — materialized by the DNS projection layer from the combination of service state, worker state, and runtime observations.

```go
// DNSEndpoint is a materialized endpoint derived from the infrastructure graph.
// It is the source of truth for DNS record projection and service catalog entries.
type DNSEndpoint struct {
    ID            uuid.UUID      `json:"id"`
    ServiceID     *uuid.UUID     `json:"service_id,omitempty"`
    EndpointID    *uuid.UUID     `json:"endpoint_id,omitempty"`     // MLInferenceEndpoint or LLMRoute
    WorkerPubkey  string         `json:"worker_pubkey,omitempty"`
    
    // Identity
    Name          string         `json:"name"`                      // e.g. "drydock-review"
    Route         string         `json:"route,omitempty"`           // e.g. "review"
    Environment   string         `json:"environment"`               // e.g. "prod"
    Zone          string         `json:"zone"`                      // e.g. "prod.cascadia"
    FQDN          string         `json:"fqdn"`                     // e.g. "drydock-review.prod.cascadia"
    
    // Network
    Protocol      string         `json:"protocol"`                  // "http", "https", "grpc"
    Address       string         `json:"address"`                   // IP or hostname
    Port          int            `json:"port,omitempty"`
    
    // Topology
    Runtime       string         `json:"runtime,omitempty"`         // "vllm", "docker", "compose"
    Hardware      string         `json:"hardware,omitempty"`        // "l40s", "rk3588", "cpu"
    Geohash       string         `json:"geohash,omitempty"`
    Capabilities  []string       `json:"capabilities,omitempty"`    // "llm", "code-review", "speech"
    
    // State
    Health        HealthStatus   `json:"health"`
    DriftStatus   DriftStatus    `json:"drift_status"`
    Source        string         `json:"source"`                    // what produced this: "reconciler", "observation", "manual"
    
    Metadata      map[string]any `json:"metadata,omitempty"`
    MaterializedAt time.Time     `json:"materialized_at"`
    ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
}
```

### 4.2 DNS zone

```go
// DNSZone defines a managed DNS zone and its backend binding.
type DNSZone struct {
    ID          uuid.UUID      `json:"id"`
    Name        string         `json:"name"`          // e.g. "prod.cascadia"
    BackendID   uuid.UUID      `json:"backend_id"`
    TTL         int            `json:"ttl"`           // default TTL in seconds
    Visibility  ZoneVisibility `json:"visibility"`    // "internal", "external", "edge"
    Metadata    map[string]any `json:"metadata,omitempty"`
    CreatedAt   time.Time      `json:"created_at"`
    UpdatedAt   time.Time      `json:"updated_at"`
}

type ZoneVisibility string

const (
    ZoneVisibilityInternal ZoneVisibility = "internal"  // local network only
    ZoneVisibilityExternal ZoneVisibility = "external"  // public DNS
    ZoneVisibilityEdge     ZoneVisibility = "edge"      // edge-node scoped
)
```

### 4.3 DNS record (projected)

```go
// DNSRecord is a single projected DNS record, derived from a DNSEndpoint.
type DNSRecord struct {
    ID          uuid.UUID  `json:"id"`
    ZoneID      uuid.UUID  `json:"zone_id"`
    EndpointID  uuid.UUID  `json:"endpoint_id"`   // source DNSEndpoint
    Name        string     `json:"name"`           // relative to zone, e.g. "drydock-review"
    Type        string     `json:"type"`           // "A", "AAAA", "CNAME", "SRV", "TXT"
    Value       string     `json:"value"`          // IP, hostname, or structured value
    TTL         int        `json:"ttl"`
    Priority    *int       `json:"priority,omitempty"`  // for SRV/MX
    Weight      *int       `json:"weight,omitempty"`    // for SRV
    Port        *int       `json:"port,omitempty"`      // for SRV
    
    // Projection metadata
    ProjectedAt time.Time  `json:"projected_at"`
    Source      string     `json:"source"`          // "projection", "manual_override"
}
```

### 4.4 DNS policy

```go
// DNSPolicy defines routing rules, split-horizon behavior, and TTL overrides.
type DNSPolicy struct {
    ID              uuid.UUID          `json:"id"`
    Name            string             `json:"name"`
    ZoneID          *uuid.UUID         `json:"zone_id,omitempty"`      // scope to zone, or nil for global
    EnvironmentID   *uuid.UUID         `json:"environment_id,omitempty"`
    Rules           []DNSPolicyRule    `json:"rules"`
    Enabled         bool               `json:"enabled"`
    Metadata        map[string]any     `json:"metadata,omitempty"`
    CreatedAt       time.Time          `json:"created_at"`
    UpdatedAt       time.Time          `json:"updated_at"`
}

// DNSPolicyRule is one rule within a policy.
type DNSPolicyRule struct {
    Match       DNSPolicyMatch  `json:"match"`
    Action      DNSPolicyAction `json:"action"`
}

// DNSPolicyMatch selects which endpoints a rule applies to.
type DNSPolicyMatch struct {
    Capabilities []string `json:"capabilities,omitempty"` // e.g. ["llm"]
    Hardware     []string `json:"hardware,omitempty"`     // e.g. ["l40s", "rk3588"]
    Geohash      string   `json:"geohash,omitempty"`      // prefix match
    Environment  string   `json:"environment,omitempty"`
    Runtime      string   `json:"runtime,omitempty"`
}

// DNSPolicyAction defines what to do with matched endpoints.
type DNSPolicyAction struct {
    Visibility   ZoneVisibility `json:"visibility,omitempty"`   // override zone visibility
    TTLOverride  *int           `json:"ttl_override,omitempty"`
    WeightBias   *int           `json:"weight_bias,omitempty"`  // for weighted routing
    Exclude      bool           `json:"exclude,omitempty"`      // suppress DNS for matched endpoints
}
```

### 4.5 DNS backend configuration

```go
// DNSBackendConfig describes a registered DNS backend.
type DNSBackendConfig struct {
    ID          uuid.UUID      `json:"id"`
    Name        string         `json:"name"`          // "coredns-prod", "dnsmasq-edge"
    Type        DNSBackendType `json:"type"`
    Config      map[string]any `json:"config"`        // backend-specific connection config
    Health      HealthStatus   `json:"health"`
    LastSyncAt  *time.Time     `json:"last_sync_at,omitempty"`
    CreatedAt   time.Time      `json:"created_at"`
    UpdatedAt   time.Time      `json:"updated_at"`
}

type DNSBackendType string

const (
    DNSBackendTypeCoreDNS        DNSBackendType = "coredns"
    DNSBackendTypePowerDNS       DNSBackendType = "powerdns"
    DNSBackendTypeDNSMasq        DNSBackendType = "dnsmasq"
    DNSBackendTypeConsul         DNSBackendType = "consul"
    DNSBackendTypeEtcd           DNSBackendType = "etcd"
    DNSBackendTypeK8sExternalDNS DNSBackendType = "k8s_external_dns"
    DNSBackendTypeFilesystem     DNSBackendType = "filesystem"  // dev/test
)
```

---

## 5. DNS Backend Adapter Interface

### 5.1 Interface definition

```go
// Backend is the pluggable boundary for DNS zone snapshots.
// Adapters translate Bahia's projected zone snapshots into backend-specific state.
type Backend interface {
    // BackendType identifies the backend adapter family.
    BackendType() domain.DNSBackendType

    // Health verifies backend reachability and readiness.
    Health(ctx context.Context) error

    // ListRecords returns all records in a zone, for drift comparison.
    ListRecords(ctx context.Context, zone domain.DNSZone) ([]domain.DNSRecord, error)

    // SyncZone replaces the backend zone snapshot with the projected record set.
    // Backends that support transactional updates should use them.
    SyncZone(ctx context.Context, zone domain.DNSZone, records []domain.DNSRecord) error
}
```

The shipped backend model is snapshot-sync only. Earlier drafts considered record-level CRUD methods (`CreateZone`, `DeleteZone`, `UpsertRecord`, `DeleteRecord`), but those are not implemented backend primitives. Zone definitions and operator overrides are persisted separately; backend adapters receive the complete projected zone through `SyncZone`.

### 5.2 Planned adapters

| Adapter | Backend | Mechanism | Priority |
|---|---|---|---|
| `coredns` | CoreDNS | etcd key writes (CoreDNS watches etcd) | Phase 0 — recommended production stack |
| `filesystem` | Flat files | Write zone files to disk | Phase 0 — dev/test mock |
| `powerdns` | PowerDNS | HTTP API | Phase 1 |
| `dnsmasq` | dnsmasq | Rewrite `/etc/dnsmasq.d/*.conf` + SIGHUP | Phase 1 |
| `consul` | Consul | Consul catalog/KV API | Phase 2 |
| `etcd` | etcd (standalone) | etcd KV writes | Phase 2 |
| `k8s_external_dns` | ExternalDNS | CRD annotations | Phase 2 |

### 5.3 Why CoreDNS + etcd first

- Modern, cloud-native, lightweight
- Plugin architecture with dynamic config
- etcd-backed service discovery is a first-class CoreDNS feature
- Works naturally with internal service discovery
- Same etcd cluster can back multiple zones
- Bahia writes to etcd; CoreDNS watches etcd — no coupling between Bahia and CoreDNS processes

### 5.4 Backend resolver

Follows the same pattern as `internal/adapters/runtime/resolver.go`:

```go
// DNSBackendResolver selects the appropriate DNS backend for a zone.
type DNSBackendResolver interface {
    Resolve(zone *domain.DNSZone) (DNSBackend, error)
}
```

---

## 6. DNS Projection Layer

### 6.1 Architecture

The projection layer is the heart of the system. It watches Bahia's materialized state and derives DNS records.

```
                          ┌─────────────────────┐
                          │  Infrastructure      │
                          │  State Sources       │
                          │                      │
                          │  • EnvironmentService │
                          │    State             │
                          │  • Worker            │
                          │  • MLInferenceState  │
                          │  • LLMRouteState     │
                          │  • RuntimeObservation│
                          └──────────┬───────────┘
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │  DNS Projector       │
                          │                      │
                          │  • Materializes      │
                          │    DNSEndpoints      │
                          │  • Derives           │
                          │    DNSRecords        │
                          │  • Applies           │
                          │    DNSPolicies       │
                          └──────────┬───────────┘
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │  DNS Reconciler      │
                          │                      │
                          │  • Compares projected│
                          │    vs actual records │
                          │  • Syncs to backends │
                          │  • Detects drift     │
                          │  • Emits events      │
                          └──────────┬───────────┘
                                     │
                                     ▼
                          ┌─────────────────────┐
                          │  DNS Backend         │
                          │  Adapters            │
                          │                      │
                          │  • CoreDNS/etcd      │
                          │  • PowerDNS          │
                          │  • filesystem mock   │
                          └─────────────────────┘
```

### 6.2 Projection rules

The projector applies deterministic rules to derive DNS records from infrastructure state. Each rule maps one state source to one or more DNS records.

#### Rule 1: Service deployment → A/CNAME record

When `EnvironmentServiceState.DriftStatus == InSync` and the latest `RuntimeObservation.HealthStatus == Healthy`:

```
Input:
  service.Name         = "drydock"
  environment.Name     = "prod"
  observation.Host     = "10.0.1.44"
  zone                 = "prod.cascadia"

Output:
  DNSEndpoint.FQDN     = "drydock.prod.cascadia"
  DNSRecord.Name        = "drydock"
  DNSRecord.Type        = "A"
  DNSRecord.Value       = "10.0.1.44"
```

#### Rule 2: LLM route deployment → A/CNAME record

When `LLMRouteState.GatewayStatus == Synced` and `BackendHealth == Healthy`:

```
Input:
  route.Name           = "drydock-review"
  environment.Name     = "prod"
  state.BackendEndpoint = "http://10.0.1.44:8000"
  zone                 = "prod.cascadia"

Output:
  DNSEndpoint.FQDN     = "drydock-review.prod.cascadia"
  DNSEndpoint.Runtime   = "vllm"
  DNSEndpoint.Capabilities = ["llm", "code-review"]
  DNSRecord.Name        = "drydock-review"
  DNSRecord.Type        = "A"
  DNSRecord.Value       = "10.0.1.44"
```

#### Rule 3: ML inference endpoint → SRV + A record

When `MLInferenceState.BackendHealth == Healthy`:

```
Input:
  endpoint.Name        = "embeddings"
  environment.Name     = "prod"
  state.BackendEndpoint = "http://10.0.1.50:8080"
  zone                 = "prod.cascadia"

Output:
  DNSEndpoint.FQDN     = "embeddings.prod.cascadia"
  DNSRecord[0].Type     = "A"
  DNSRecord[0].Value    = "10.0.1.50"
  DNSRecord[1].Type     = "SRV"
  DNSRecord[1].Port     = 8080
  DNSRecord[1].Priority = 10
  DNSRecord[1].Weight   = 100
```

#### Rule 4: Worker advertisement → edge DNS record

When `Worker.Status == Online` and worker has a `RuntimeTarget`:

```
Input:
  worker.Name          = "t7920-l40s"
  worker.Geohash       = "c23nb"
  worker.Accelerators  = [{Model: "L40S"}]
  worker.RuntimeTarget.PublicBaseURL = "http://10.0.1.44"
  zone                 = "edge.cascadia"

Output:
  DNSEndpoint.FQDN     = "t7920-l40s.edge.cascadia"
  DNSEndpoint.Hardware  = "l40s"
  DNSEndpoint.Capabilities = ["llm", "gpu"]
  DNSRecord.Type        = "A"
  DNSRecord.Value       = "10.0.1.44"
```

#### Rule 5: Capability-based aliases

When policy enables capability routing, the projector also creates alias records:

```
Output:
  "llm.prod.cascadia"      → CNAME → "drydock-review.prod.cascadia"
  "speech.local.cascadia"  → A     → <nearest edge runtime with speech capability>
  "gpu.edge.cascadia"      → A     → <nearest online worker with GPU>
```

### 6.3 Health gating

DNS records are only projected for endpoints in a healthy state. The projector applies these gates:

| Source | Gate condition | Effect when gate fails |
|---|---|---|
| `EnvironmentServiceState` | `DriftStatus == InSync` AND observation `HealthStatus == Healthy` | Record removed from projection |
| `LLMRouteState` | `GatewayStatus == Synced` AND `BackendHealth == Healthy` | Record removed from projection |
| `MLInferenceState` | `BackendHealth == Healthy` | Record removed from projection |
| `Worker` | `Status == Online` | Record removed from projection |

When an endpoint transitions from healthy → unhealthy, the projector removes the record. When it returns to healthy, the record is re-projected. The TTL determines how quickly clients see the change.

### 6.4 Endpoint Nostr event (kind 31976)

The canonical endpoint is published as a replaceable read-model event:

```json
{
  "kind": 31976,
  "content": "{\"service\":\"drydock\",\"route\":\"review\",\"env\":\"prod\",\"proto\":\"http\",\"addr\":\"10.0.1.44\",\"port\":8000,\"runtime\":\"vllm\",\"hardware\":\"l40s\",\"health\":\"healthy\",\"capabilities\":[\"llm\",\"code-review\"]}",
  "tags": [
    ["d", "endpoint:drydock.review.prod"],
    ["service", "drydock"],
    ["route", "review"],
    ["env", "prod"],
    ["host", "t7920-l40s"],
    ["runtime", "vllm"],
    ["proto", "http"],
    ["port", "8000"],
    ["health", "healthy"],
    ["addr", "10.0.1.44"],
    ["dns", "drydock-review.prod.cascadia"],
    ["capability", "llm"],
    ["capability", "code-review"],
    ["t", "dns-endpoint"],
    ["t", "bahia"]
  ],
  "pubkey": "<bahia-service-pubkey>",
  "created_at": 1747843200
}
```

**Tag design**: Single-letter tags (`d`, `t`) are relay-filterable. Multi-character tags (`service`, `route`, `env`, `health`, `capability`) are for client-side filtering and indexer discoverability — they are NOT relay-filterable per the Nostr spec. This is intentional; clients subscribe by `kind + author` and filter locally.

---

## 7. DNS Reconciler

### 7.1 Reconciler design

Follows the established `Reconciler` and `LLMRouteReconciler` patterns:

```go
// DNSReconciler compares projected DNS records with actual backend state.
type DNSReconciler struct {
    projector        *DNSProjector
    zones            repository.DNSZoneRepository
    records          repository.DNSRecordRepository
    backends         DNSBackendResolver
    policies         repository.DNSPolicyRepository
    publisher        events.Publisher
    interval         time.Duration
    logger           *zap.Logger
}

func (r *DNSReconciler) Name() string { return "dns-reconciler" }

func (r *DNSReconciler) Run(ctx context.Context) error {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    for {
        if err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
            r.logger.Warn("DNS reconcile failed", zap.Error(err))
        }
        select {
        case <-ctx.Done():
            return nil
        case <-ticker.C:
        }
    }
}
```

### 7.2 Reconciliation loop

```
For each zone:
  1. Projector materializes DNSEndpoints from all state sources
  2. Policies filter/transform endpoints (split-horizon, exclusions, weight bias)
  3. Projector derives DNSRecords from filtered endpoints
  4. Backend adapter lists actual records (ListRecords)
  5. Diff: projected snapshot vs actual snapshot
     - Any projected/actual mismatch is drift
     - Record-level create/update/delete operations were considered but are not implemented backend methods
     - The reconciler remediates drift by calling SyncZone with the complete projected record set
     - Identical snapshots require no backend write
  6. Emit audit events for changes
  7. Update DNSBackendState read model (kind 31978) with sync timestamp
  8. If diff was non-empty, emit drift detection event (kind 31022)
```

### 7.3 Event-driven vs. polling

The reconciler runs on a configurable interval (default: 30 seconds) as the safety net. For faster convergence, the projector can also be triggered by internal events:

| Event | Trigger |
|---|---|
| `EventEnvironmentServiceStateChanged` | Service deployment completed |
| `EventDriftDetected` | Runtime drift detected (may need DNS update) |
| `llm_route_state.changed` | LLM route state changed |
| Worker kind 10100 received | New worker online or worker state change |

The reconciler coalesces rapid-fire triggers with a debounce window (default: 5 seconds) to avoid thrashing.

---

## 8. Split-Horizon & Edge Routing

### 8.1 Zone visibility model

Zones have a visibility scope that determines which clients see their records:

| Visibility | Scope | Example zone |
|---|---|---|
| `internal` | Local network only; records served only to internal clients | `prod.cascadia`, `staging.cascadia` |
| `external` | Public DNS; records resolvable from the internet | `api.cascadia.sh` |
| `edge` | Edge-node scoped; records specific to edge location | `edge.cascadia`, `local.cascadia` |

### 8.2 Split-horizon via DNS policies

A DNS policy can assign different endpoints to different zones based on topology:

```json
{
  "name": "edge-local-routing",
  "rules": [
    {
      "match": { "hardware": ["rk3588"], "capabilities": ["speech"] },
      "action": { "visibility": "edge" }
    },
    {
      "match": { "runtime": "vllm", "capabilities": ["llm"] },
      "action": { "visibility": "internal" }
    }
  ]
}
```

Result:

```
speech.local.cascadia   → nearby edge runtime (rk3588 with speech)
llm.prod.cascadia       → vLLM cluster (internal only)
drydock.prod.cascadia   → production deployment (internal only)
```

### 8.3 Hardware-aware routing

Bahia has full hardware topology via `Worker.Accelerators` and `Worker.MLCapabilities`. The projector can generate hardware-specific DNS:

```
rk3588.edge.cascadia     → local NPU runtime
l40s.gpu.cascadia        → L40S GPU workers
a100.gpu.cascadia        → A100 GPU workers (if any)
```

---

## 9. Service Catalogs

### 9.1 Unified discovery

The DNS endpoint projection is also the source for service catalogs. The same `DNSEndpoint` produces:

| Output | Format | Consumer |
|---|---|---|
| DNS record | A/AAAA/CNAME/SRV | DNS clients |
| Service catalog entry | Kind 31976 Nostr event | Nostr subscribers |
| MCP tool discovery | MCP `resources/list` response | MCP agents |
| API discovery | REST endpoint | HTTP clients |

### 9.2 Service catalog event

Each `DNSEndpoint` is published as a kind 31976 event (Section 6.4). Clients can subscribe and build a live service catalog:

```
service:     drydock.review
capabilities: [llm, code-review]
endpoint:    https://drydock-review.prod.cascadia
runtime:     vllm
hardware:    l40s
health:      healthy
```

This provides DNS, service mesh, AI runtime catalog, and deployment registry in a single event substrate.

---

## 10. Security Model

### 10.1 Signed mutations

All DNS mutations flow through the Nostr control plane and are signed by the Bahia service key or an authorized operator key. This provides:

- **Attribution**: Every DNS change is traceable to a signed event
- **Replayability**: The full history of DNS mutations is stored in the `nostr_events` table
- **Reversibility**: Rolling back a DNS change means re-projecting from a previous state snapshot
- **Audit**: Kind 31020-31024 events create a complete audit trail

### 10.2 Authorization

DNS mutation requests (kind 5941-5945) follow the same authorization model as all other control-plane requests:

```go
func (r *Reactor) isAuthorized(pubkey string) bool
```

Operators must be in the `AuthorizedPubkeys` list. The projector's automatic record changes are signed by the Bahia service key itself (not an operator key), making them distinguishable in the audit trail.

### 10.3 Drift detection as security signal

DNS drift (projected state ≠ actual backend state) is a potential security signal — it may indicate unauthorized DNS modifications. The reconciler emits `dns.drift_detected` events (kind 31022) that can feed into alerting and incident response.

### 10.4 Manual override escape hatch

The `DNSRecordOverride` request (kind 5943) allows operators to pin a DNS record that the projector won't overwrite. Overrides are:

- Signed by an authorized operator
- Recorded in the audit trail
- Visible in the `DNSEndpointState` read model (marked `source: "manual_override"`)
- Subject to drift detection (override vs. actual backend state)

---

## 11. Configuration

### 11.1 Config structure

```yaml
dns:
  enabled: true
  default_ttl: 300                    # 5 minutes
  reconcile_interval: 30s
  debounce_window: 5s
  
  zones:
    - name: "prod.cascadia"
      visibility: internal
      backend: coredns-prod
    - name: "edge.cascadia"
      visibility: edge
      backend: dnsmasq-edge
    - name: "api.cascadia.sh"
      visibility: external
      backend: coredns-prod
  
  backends:
    - id: coredns-prod
      type: coredns
      config:
        etcd_endpoints: ["http://localhost:2379"]
        etcd_prefix: "/skydns/"
    - id: dnsmasq-edge
      type: dnsmasq
      config:
        config_dir: "/etc/dnsmasq.d"
        reload_command: "systemctl reload dnsmasq"
  
  projection:
    # Which state sources to project
    services: true
    llm_routes: true
    ml_endpoints: true
    workers: true
    # Health gating
    require_healthy: true
    # Capability aliases
    capability_aliases: true
```

---

## 12. Recommended File Touch Points

### New files

| File | Purpose |
|---|---|
| `internal/domain/dns.go` | Domain types: `DNSEndpoint`, `DNSZone`, `DNSRecord`, `DNSPolicy`, `DNSBackendConfig` |
| `internal/domain/dns_test.go` | Validation tests for DNS domain types |
| `internal/adapters/dns/backend.go` | `DNSBackend` interface definition + `DNSBackendResolver` |
| `internal/adapters/dns/coredns.go` | CoreDNS/etcd adapter |
| `internal/adapters/dns/coredns_test.go` | CoreDNS adapter unit tests |
| `internal/adapters/dns/filesystem.go` | Filesystem mock adapter for dev/test |
| `internal/adapters/dns/filesystem_test.go` | Filesystem adapter unit tests |
| `internal/reconcile/dns_projector.go` | DNS projection layer: materializes endpoints from state sources |
| `internal/reconcile/dns_projector_test.go` | Projection rule unit tests |
| `internal/reconcile/dns_reconciler.go` | DNS reconciler: compares projected vs actual, syncs backends |
| `internal/reconcile/dns_reconciler_test.go` | Reconciler loop unit tests |
| `internal/repository/dns.go` | Repository interfaces: `DNSZoneRepository`, `DNSRecordRepository`, `DNSEndpointRepository`, `DNSPolicyRepository` |
| `internal/controlplane/dns_handlers.go` | Reactor handlers for DNS request kinds (5941-5945) |
| `internal/controlplane/dns_kinds.go` | DNS kind constants |
| `docs/dns-orchestration.md` | Operator documentation |

### Modified files

| File | Change |
|---|---|
| `internal/controlplane/reactor.go` | Add DNS kind constants to subscription filters; wire DNS handlers |
| `internal/adapters/nostr/projector.go` | Add DNS read-model projection (kinds 31975-31978) |
| `internal/adapters/nostr/publisher.go` | Add DNS audit event publication (kinds 31020-31024) |
| `internal/config/config.go` | Add `DNS` config section |
| `internal/app/app.go` | Wire DNS reconciler, projector, backend resolver into app lifecycle |
| `docs/event-spec.md` | Document DNS event kinds |
| `docs/control-planes.md` | Document DNS control-plane contract |

---

## 13. Migration Sequencing

### Phase 0 — Foundation (no DNS serving, projection only)

**What ships:**
- Domain types in `internal/domain/dns.go`
- Repository interfaces in `internal/repository/dns.go`
- Filesystem mock backend adapter
- DNS projector that materializes `DNSEndpoint` records from existing state
- Kind 31976 endpoint events published to sidecar
- DNS config section (disabled by default)

**Risk:** Low — purely additive. No DNS backends connected, no records modified. The projector runs in observation mode, publishing endpoint events to the relay for visibility.

**Validates:**
- Projection rules produce correct endpoints from real state
- Endpoint events are well-formed and useful
- Event kind allocations don't conflict

### Phase 1 — CoreDNS integration

**What ships:**
- CoreDNS/etcd backend adapter
- DNS reconciler with drift detection
- Zone configuration
- Kind 31975 (zone state), 31978 (backend state) read models
- Audit events (kinds 31020-31024)
- DNS request handlers in reactor (kinds 5941-5945)

**Risk:** Medium — writes to etcd affect CoreDNS behavior. Requires operator opt-in via config. Reconciler runs with a safety mode that logs intended changes before applying them.

**Validates:**
- End-to-end flow: deployment → projection → etcd write → CoreDNS serves record
- Drift detection and remediation
- Health gating works correctly
- Audit trail is complete

### Phase 2 — Policies and split-horizon

**What ships:**
- DNS policy domain types and handlers
- Split-horizon zone support
- Capability-based alias records
- Edge routing
- Hardware-aware DNS
- Additional backend adapters (PowerDNS, dnsmasq)

**Risk:** Medium — policy evaluation adds complexity to the projection layer. Requires careful testing of policy interaction and precedence.

### Phase 3 — Service catalogs and AI-assisted UX

**What ships:**
- MCP tool discovery integration (endpoints as MCP resources)
- Natural language DNS commands via assistant ("Expose drydock staging internally only")
- Service catalog aggregation from endpoint events
- Web dashboard DNS view

**Risk:** Low — builds on stable projection layer. Assistant integration follows existing `AssistantOrchestrator` patterns.

---

## 14. Test Strategy

### Unit tests

| Test | Validates |
|---|---|
| `dns_test.go` — domain validation | Zone names, record types, policy rules, endpoint FQDN generation |
| `dns_projector_test.go` — Rule 1 | Service deployment → A record; health gate blocks unhealthy |
| `dns_projector_test.go` — Rule 2 | LLM route → A record; gateway gate blocks unsynced |
| `dns_projector_test.go` — Rule 3 | ML endpoint → SRV + A; backend health gates |
| `dns_projector_test.go` — Rule 4 | Worker → edge A record; offline workers excluded |
| `dns_projector_test.go` — Rule 5 | Capability aliases generated correctly |
| `dns_projector_test.go` — policies | Split-horizon filtering, TTL overrides, exclusion rules |
| `dns_reconciler_test.go` — sync | Projected set synced to backend; creates/updates/deletes correct records |
| `dns_reconciler_test.go` — drift | Detects backend records that don't match projection; emits drift event |
| `dns_reconciler_test.go` — debounce | Rapid triggers coalesced into single reconciliation |
| `coredns_test.go` — adapter | Writes correct etcd keys; reads records back; handles connection errors |
| `filesystem_test.go` — adapter | Writes zone files; reads them back; handles missing directories |
| `dns_kinds.go` — kind validation | All DNS kinds are unique and don't collide with existing controlplane kinds |

### Integration tests

| Test | Validates |
|---|---|
| Projection integration | Deploy service → reconciler materializes endpoint → backend gets record |
| LLM route integration | Create LLM route → deploy → gateway syncs → DNS record appears |
| Worker lifecycle | Worker comes online → edge DNS record created; worker goes offline → record removed |
| Drift remediation | Manually delete record from backend → reconciler detects and restores |
| Policy application | Apply split-horizon policy → internal records suppressed from external zone |

### E2E tests

| Test | Validates |
|---|---|
| Full deployment flow | CLI deploy → backend DNS record → DNS resolution succeeds |
| Health-gated removal | Kill service → observation unhealthy → DNS record removed → restart → DNS record restored |

---

## 15. Example Deployment Flow

### Scenario: "Deploy drydock.review to L40S"

```
1. Operator publishes KindDeployRequest (5961) or KindLLMDeployRequest (5973)
2. Bahia provisions runtime on L40S worker
3. Runtime registers endpoint observation
   → RuntimeObservation { Host: "10.0.1.44", HealthStatus: "healthy" }
4. LLMRouteReconciler observes backend healthy, gateway synced
   → LLMRouteState { BackendEndpoint: "http://10.0.1.44:8000", BackendHealth: "healthy", GatewayStatus: "synced" }
5. DNS Projector materializes endpoint
   → DNSEndpoint { FQDN: "drydock-review.prod.cascadia", Address: "10.0.1.44", Health: "healthy" }
6. DNS Projector derives records
   → DNSRecord { Name: "drydock-review", Zone: "prod.cascadia", Type: "A", Value: "10.0.1.44" }
7. DNS Reconciler compares the projected zone snapshot with backend records
   → Snapshot drift detected → SyncZone with the complete projected record set
8. CoreDNS adapter writes the synced snapshot to etcd
   → key: /skydns/cascadia/prod/drydock-review → {"host": "10.0.1.44"}
9. CoreDNS serves:
   drydock-review.prod.cascadia → 10.0.1.44
10. Bahia publishes:
    → Kind 31976 (DNSEndpointState) to sidecar
    → Kind 31021 (dns.record_changed) audit event
    → Kind 31020 (dns.zone_synced) after full zone sync
```

---

## 16. Open Questions

1. **Zone naming convention**: Should Bahia enforce a naming convention (e.g., `<env>.<cluster>`) or allow arbitrary zone names? Recommendation: allow arbitrary, but provide sensible defaults in config.

2. **Record TTL strategy**: Should TTL be static per-zone, or dynamic based on health check frequency? Lower TTL = faster failover but more DNS traffic. Recommendation: configurable per-zone with policy overrides.

3. **Multi-cluster DNS**: When Bahia manages multiple clusters, should cross-cluster DNS be automatic? Recommendation: defer to Phase 3; start with single-cluster zones.

4. **External DNS publication**: Should Bahia update public DNS (e.g., Cloudflare, Route53) for external-facing services? Recommendation: yes, but only via the `k8s_external_dns` adapter pattern in Phase 2.

5. **Negative caching**: When an endpoint becomes unhealthy, should the projector emit an explicit "this name does not exist" signal, or simply stop publishing the record? Recommendation: stop publishing; let TTL expiry handle removal.

6. **Nostr-native DNS resolution**: Should a future Nostr client be able to resolve endpoints purely from kind 31976 events without traditional DNS? This would make the Nostr relay itself a service discovery mechanism. Recommendation: yes, this is the long-term vision, but traditional DNS backends remain the primary resolution path.

---

## 17. Long-Term Vision

DNS becomes one projection of the Nostr event graph — automatically synthesized infrastructure topology, not static records. The same event substrate that powers DNS also powers:

- Gateway configuration
- Service mesh topology
- AI runtime catalogs
- MCP tool discovery
- Deployment routing

Bahia remains the **orchestration and projection layer** — it owns the topology graph, endpoint declarations, policy, observability, signed mutations, and drift detection. Existing systems (CoreDNS, PowerDNS, dnsmasq) own actual DNS serving, recursion, protocol implementation, and low-level resolution.

```
Bahia
  + CoreDNS (serving)
  + Nostr endpoint graph (state)
  + DNS projection layer (derivation)
  + Deployment-driven DNS synthesis (automation)
```

---

## Appendix A: Kind Number Registry (DNS additions)

| Kind | Name | Range | Series |
|---|---|---|---|
| 5941 | `DNSZoneCreate` | Regular | Request |
| 5942 | `DNSPolicyApply` | Regular | Request |
| 5943 | `DNSRecordOverride` | Regular | Request |
| 5944 | `DNSDriftRemediate` | Regular | Request |
| 5945 | `DNSBackendRegister` | Regular | Request |
| 6941 | `DNSOperationStatus` | Regular | Status |
| 7941 | `DNSZoneCreateResult` | Regular | Result |
| 7942 | `DNSPolicyApplyResult` | Regular | Result |
| 7943 | `DNSRecordOverrideResult` | Regular | Result |
| 7944 | `DNSDriftRemediateResult` | Regular | Result |
| 7945 | `DNSBackendRegisterResult` | Regular | Result |
| 31020 | `dns.zone_synced` | Regular | Audit |
| 31021 | `dns.record_changed` | Regular | Audit |
| 31022 | `dns.drift_detected` | Regular | Audit |
| 31023 | `dns.endpoint_registered` | Regular | Audit |
| 31024 | `dns.endpoint_deregistered` | Regular | Audit |
| 31975 | `DNSZoneState` | Parameterized replaceable | Read model |
| 31976 | `DNSEndpointState` | Parameterized replaceable | Read model |
| 31977 | `DNSPolicyState` | Parameterized replaceable | Read model |
| 31978 | `DNSBackendState` | Parameterized replaceable | Read model |

## Appendix B: Existing Kind Registry (for conflict reference)

| Range | Allocation |
|---|---|
| 5961-5989 | Core deployment + service + policy requests |
| 5991-5996 | Package registry requests |
| 5971-5975 | LLM control-plane requests |
| 5976-5979 | Tool provisioning + adoption requests |
| 6961-6991 | Status kinds |
| 7961-7992 | Result kinds |
| 31000-31019 | Audit events |
| 31961-31974 | Replaceable read models |
| 38390-38399 | ML inference commands/results |

**594x/694x/794x is unallocated** — chosen for DNS to maintain clear separation.

## Appendix C: CoreDNS etcd Key Format

CoreDNS with the etcd plugin expects keys in reverse-domain format under a configurable prefix:

```
/skydns/cascadia/prod/drydock-review → {"host": "10.0.1.44", "ttl": 300}
/skydns/cascadia/prod/embeddings    → {"host": "10.0.1.50", "port": 8080, "priority": 10}
/skydns/cascadia/edge/t7920-l40s    → {"host": "10.0.1.44", "ttl": 60}
```

The CoreDNS adapter translates Bahia's `DNSRecord` into this key format:

```go
func (a *CoreDNSAdapter) etcdKey(zone, name string) string {
    // "drydock-review" in zone "prod.cascadia" 
    // → /skydns/cascadia/prod/drydock-review
    parts := strings.Split(zone, ".")
    slices.Reverse(parts)
    return path.Join(a.prefix, path.Join(parts...), name)
}
```
