> **⚠️ ARCHIVED DOCUMENT**
>
> This planning document was written in early 2024. Many items listed as "gaps" or "planned"
> have since been implemented. For current implementation status, see:
> - [protocol-compatibility.md](../protocol-compatibility.md) — What's actually implemented
> - [architecture.md](../architecture.md) — Current system design
>
> This document is preserved for historical context showing the project's evolution.

---

# Bahia: Deep Review & World-Class Roadmap (Historical)

*A comprehensive analysis of what Bahia provided at the time of writing, what it needed to become a world-class nostr-first container registry and deployment service, and the detailed steps to get there.*

---

## Table of Contents

1. [Part I — What Bahia Provides Today](#part-i--what-bahia-provides-today)
2. [Part II — What Would Make Bahia World-Class](#part-ii--what-would-make-bahia-world-class)
3. [Part III — Implementation Roadmap](#part-iii--implementation-roadmap)

---

## Part I — What Bahia Provides Today

### Overview

Bahia is a **Nostr-native Deployment Registry Service** — the canonical control plane for build artifacts, deployment intents, execution, reconciliation, and runtime drift detection in a Nostr-based CI/CD system. It sits between Hive-CI (builds), Harbor (image storage), Loom (workers), Docker (runtime), and Nostr relays (audit trail).

```
┌─────────────┐     ┌──────────┐     ┌───────────┐
│  Hive-CI    │────▶│  Bahia   │────▶│   Loom    │
│  (builds)   │     │ Registry │     │ (workers) │
└─────────────┘     └────┬─────┘     └───────────┘
                         │
                    ┌────┴────┐
                    │         │
               ┌────▼───┐ ┌──▼──────┐
               │ Harbor  │ │ Docker  │
               │ (images)│ │ (runtime)│
               └────────┘ └─────────┘
                    │         │
               ┌────▼─────────▼──┐
               │   PostgreSQL    │
               │  (state store)  │
               └────────┬───────┘
                        │
               ┌────────▼───────┐
               │  Nostr Relays  │
               │ (audit trail)  │
               └────────────────┘
```

### 1.1 Domain Model

Bahia's data model is mature and well-structured with eight core entities:

| Entity | Purpose | Key Fields |
|--------|---------|------------|
| **Service** | Deployable application component | name, repo_url, artifact_repo, runtime_type (docker/compose/k8s) |
| **Environment** | Named deployment target | loom_worker_selector (JSONB), deploy_strategy (replace/blue-green/canary), protected flag |
| **Build** | CI build execution record | git_sha, git_ref, ci_system, loom_job_id, status lifecycle |
| **Artifact** | Immutable OCI image reference | image_repo, image_tag, image_digest, sbom_url, signature_ref, scan_status |
| **DeploymentIntent** | Request to deploy an artifact to an environment | approval workflow (pending→approved→deploying→deployed), source_kind (manual/auto-promote/rollback/scheduled/event-triggered), supersession chain |
| **DeploymentRun** | Concrete execution attempt of an intent | loom_job_id, worker_pubkey, exit_code, stdout/stderr refs |
| **RuntimeObservation** | Snapshot of actual container state | observed image digest/repo/container_id/host, health_status |
| **EnvironmentServiceState** | Desired vs observed state reconciliation | desired_artifact_id, current_observation_id, drift_status (unknown/in_sync/drifted/deploying) |

The schema includes useful extension points that are not yet fully utilized: `metadata JSONB` on most tables, `sbom_url` and `signature_ref` on artifacts, `loom_worker_selector JSONB` and `runtime_config JSONB` on environments.

### 1.2 REST API

Full CRUD REST API built on the chi router with 30+ endpoints:

- **Services**: Create, Get, List, Update, Delete
- **Environments**: Create, Get, List, Update, Delete
- **Builds**: Register, Get, List by service, Update status
- **Artifacts**: Register, Get, List by service
- **Deployment Intents**: Create, Get, List, Approve, Reject
- **Deployment Runs**: Create, Get, List by intent, Complete
- **Rollback**: Create rollback intent (automatically finds previous successful artifact)
- **State & Observations**: List all states, list drifted states, list by environment, get specific state, record observation
- **Health**: `/health` and `/ready` endpoints

All responses follow a consistent envelope: `{"data": ..., "error": "", "message": ""}` with pagination for list endpoints.

### 1.3 Deployment Workflow Engine

The `workflow.Coordinator` orchestrates the full deployment lifecycle:

1. An approved `DeploymentIntent` triggers `EventDeploymentIntentApproved` on the internal event bus
2. The Coordinator subscribes to this event and auto-executes deployment
3. It loads the intent, artifact, service, and environment from the registry
4. Submits a Loom `JobRequest` with image, digest, environment, and service info
5. Creates a `DeploymentRun` record (status: queued)
6. Spawns a tracked goroutine to poll Loom for job completion
7. On completion, calls `CompleteDeploymentRun` which cascades state updates

The Coordinator has proper graceful shutdown with `sync.WaitGroup` tracking of in-flight polls.

### 1.4 Reconciliation & Drift Detection

The `reconcile.Reconciler` runs a continuous loop:

1. Lists all `EnvironmentServiceState` records
2. For each (skipping those currently deploying):
   - Queries Docker Engine API for actual container state via `runtime.DockerObserver`
   - Records a `RuntimeObservation`
   - Compares observed image digest against desired artifact digest
   - Updates drift status (in_sync / drifted / unknown)
   - Publishes `EventDriftDetected` when drift is found
3. Publishes `EventReconcileCompleted` with count of checked states

### 1.5 Rollback

First-class rollback support:
- `RegistryService.Rollback` scans deployment history for the most recent *different* successfully-deployed artifact
- Creates a new `DeploymentIntent` with `SourceKind: rollback` and `SupersedesIntentID` pointing to the current intent
- Skips failed intents when searching history
- Returns the new rollback intent for tracking

### 1.6 Adapters & Integrations

| Adapter | Status | Capability |
|---------|--------|------------|
| **Harbor** | Functional | Tag→digest resolution, image existence check, scan status retrieval |
| **Loom** | Functional | Job submission (Kind 5100), status polling (Kind 30100/5101), cancellation (Kind 5102) |
| **Nostr Publisher** | Functional (outbound only) | Publishes signed events (kinds 31000-31005) for build/artifact/deployment/drift events |
| **Docker Observer** | Basic | Queries Docker Engine API for container state, extracts image digest, maps container health status |

### 1.7 Authentication & Security

- **JWT Authentication**: Optional HMAC-SHA256 JWT middleware; fails closed if enabled without secret
- **Rate Limiting**: Per-IP token bucket rate limiter (100 req/min reads, 30 req/min writes) with automatic cleanup of stale entries
- **CORS**: Configurable allowed origins (secure default: none)
- Auth can be disabled entirely for development

### 1.8 Nostr Event Publishing

Bahia publishes signed parameterized replaceable events (NIP-33):

| Kind | Label | Trigger |
|------|-------|---------|
| 31000 | `build.registered` | New build registered |
| 31001 | `artifact.registered` | New artifact registered |
| 31002 | `deployment.created` | Deployment intent created |
| 31003 | `deployment.completed` | Deployment run completed |
| 31004 | `drift.detected` | Drift detected |
| 31005 | `runtime.observation` | Runtime observation recorded |

### 1.9 Internal Event Bus

An in-process pub/sub system (`events.InProcessPublisher`) with panic recovery that drives internal coordination:

- `EventBuildRegistered`, `EventBuildStatusChanged`
- `EventArtifactRegistered`
- `EventDeploymentIntentCreated`, `EventDeploymentIntentApproved`
- `EventDeploymentRunCreated`, `EventDeploymentRunCompleted`
- `EventRuntimeObservation`, `EventDriftDetected`, `EventReconcileCompleted`

### 1.10 CLI & Client Library

- **CLI** (`cmd/cli`): Cobra-based CLI for services/environments/state/deploy/rollback
- **Go Client** (`pkg/client`): Programmatic HTTP client wrapping the REST API
- Both are thin wrappers over the REST API

### 1.11 Operational Infrastructure

- **PostgreSQL 16+** with embedded SQL migrations (auto-run on startup)
- **Docker Compose** for local development (postgres + bahia)
- **Dockerfile** for production builds
- **Makefile** with build, test, lint, docker, and deps targets
- **Structured logging** via `zap` (JSON or console format)
- **Configuration** via YAML + environment variables (`BAHIA_` prefix)

---

## Part II — What Would Make Bahia World-Class

### Current Gaps

Through the deep review, the following gaps are evident:

#### Identity & Auth Gaps
1. **No Nostr-native authentication** — JWT only; no NIP-98 HTTP auth, no NIP-42 relay auth
2. **No NIP-05 identity verification** — no way to attach verified nostr identities to actors
3. **Actor identity is client-supplied** — `requested_by` field in request body can be spoofed when auth is enabled
4. **No RBAC / multi-tenant** — single-tenant, no role-based access control

#### Protocol & Integration Gaps
5. ~~**Loom protocol misalignment**~~ — **FIXED** — Bahia now uses kinds 5100/30100/5101/5102 per the Loom protocol spec
6. **No Nostr event ingestion** — outbound audit only; no inbound event subscription
7. **No Hive-CI event integration** — can't ingest build results from Hive-CI (kinds 5401/5402)
8. **No Loom worker discovery** — no catalog of available workers, no advertisement ingestion (kind 10100)
9. **No Cashu payment integration** — no way to fund deployments with ecash
10. **No Blossom storage integration** — despite the ecosystem using Blossom for file storage

#### Artifact & Supply Chain Gaps
11. **Harbor-only OCI support** — no generic OCI registry, no GHCR/Docker Hub/custom registry support
12. **No image signing verification** — `signature_ref` field exists but cosign/sigstore verification is unimplemented
13. **No SBOM analysis** — `sbom_url` field exists but no parsing, storage, or policy enforcement
14. **No vulnerability policy gates** — `scan_status` tracked but no policy enforcement
15. **No artifact promotion workflows** — no staging→production promotion with audit trail

#### Deployment & Runtime Gaps
16. **No progressive delivery** — canary/blue-green are in the domain model but have no runtime implementation
17. **No multi-target runtime support** — Docker only; no Compose orchestration, no Kubernetes
18. **Docker observer is very basic** — only checks image digest via Docker API labels; no health check integration, no log streaming
19. **No container log streaming** — stdout/stderr refs exist but no live log access
20. **No secrets management** — no way to inject secrets into deployments

#### UX & Operational Gaps
21. **No real-time updates** — no WebSocket/SSE; clients must poll
22. **No webhook/notification system** — no way to notify external systems
23. **No web dashboard** — CLI-only management interface
24. **No OpenTelemetry** — `telemetry.go` is a stub (just logs a message)
25. **No metrics or alerting** — no Prometheus metrics, no health check alerting

#### Configuration & Foundation
26. **Env var parsing may be broken** — config uses `__` as separator but `.env.example` and docker-compose use `_`

### Vision: The World-Class Feature Set

To become the definitive nostr-first container registry and deployment platform, Bahia needs to offer capabilities in five categories that no traditional registry provides:

#### A. Nostr-Native Identity & Control Plane
- **NIP-98 HTTP Authentication** — sign API requests with your nostr key
- **Bidirectional Nostr control plane** — operate Bahia entirely through nostr events
- **NIP-05 verified identities** — attach human-readable names to deployment actors
- **Nostr-signed approvals** — approve deployments with cryptographic signatures
- **Web of Trust integration** — use WoT scores for deployment authorization

#### B. Decentralized Compute & Payments
- **Loom worker catalog** — auto-discover and rank available deployment workers
- **Cashu payment-gated deployments** — pay for compute with ecash
- **Cost estimation** — predict deployment cost before execution
- **Worker selection policies** — match deployments to workers by capability, price, reputation, location

#### C. Supply Chain Security
- **Multi-registry OCI support** — Harbor, GHCR, Docker Hub, any OCI-compliant registry
- **Cosign/Sigstore verification** — verify image signatures before deployment
- **SBOM ingestion & analysis** — parse, store, and query SBOMs
- **Vulnerability policy gates** — block deployments that fail policy checks
- **Provenance tracking** — full build→artifact→deployment provenance chain anchored in nostr events

#### D. Production Deployment Capabilities
- **Progressive delivery** — real canary and blue-green rollouts with health gates
- **Multi-runtime targets** — Docker, Docker Compose, Kubernetes
- **Secrets management** — encrypted secret injection via NIP-44 or sealed secrets
- **Container log streaming** — live logs via Blossom references or direct streaming
- **Auto-remediation** — automatic rollback on drift or health failure

#### E. Platform Experience
- **Real-time event stream** — WebSocket/SSE for live deployment status
- **Webhook notifications** — notify Slack, Discord, or any HTTP endpoint
- **OpenTelemetry observability** — distributed traces, metrics, and structured logs
- **Multi-tenant RBAC** — organizations, teams, roles, and permissions
- **Web dashboard** — visual deployment management and monitoring

---

## Part III — Implementation Roadmap

### Phase 0 — Foundation Hardening & Protocol Alignment

**Goal**: Fix foundational issues so later phases land on stable ground.
**Dependencies**: None
**Estimated effort**: 1–2 weeks

#### 0.1 Fix Configuration Loading

**Problem**: `config.go` uses `__` as env var separator, but `.env.example`, `docker-compose.yml`, and all documentation use single `_`. Nested config likely doesn't load from environment.

**Files to modify**:
- `internal/config/config.go` — Fix `Load()` env mapping function
- `internal/config/config_test.go` — Add precedence tests

**Implementation**:
```
Change the env provider mapping so:
- BAHIA_DB_HOST → db.host (matches documented behavior)
- BAHIA_DB__HOST → db.host (explicit nested form also works)
- BAHIA_AUTH_JWT_SECRET → auth.jwt_secret
Rule: Strip BAHIA_, lowercase, replace __ with ., then for remaining
single underscores within each segment treat them as field separators.
```

#### 0.2 Add Future Config Stubs

**Files to modify**:
- `internal/config/config.go` — Add struct definitions for Blossom, Cashu, Telemetry, Notifications configs

**Purpose**: Define config structs now so later phases don't need repeated config churn. Defaults should leave all new features disabled.

```go
type BlossomConfig struct {
    Enabled        bool     `koanf:"enabled"`
    Servers        []string `koanf:"servers"`
    RequestTimeout time.Duration `koanf:"request_timeout"`
}

type CashuConfig struct {
    Enabled        bool   `koanf:"enabled"`
    DefaultMintURL string `koanf:"default_mint_url"`
}

type TelemetryConfig struct {
    Enabled      bool   `koanf:"enabled"`
    OTLPEndpoint string `koanf:"otlp_endpoint"`
    ServiceName  string `koanf:"service_name"`
}

type NotificationsConfig struct {
    Enabled  bool     `koanf:"enabled"`
    Webhooks []string `koanf:"webhooks"`
}
```

#### 0.3 Loom Protocol Alignment

**Problem**: `docs/event-spec.md` documents Loom kinds as 5003/6003, but the actual Loom protocol uses 10100/5100/30100/5101/5102.

**Files to modify**:
- `internal/adapters/loom/client.go` — Update event kinds in `SubmitJob` and `PollJobStatus`
- `docs/event-spec.md` — Correct Loom event kind documentation
- `docs/architecture.md` — Update integration documentation
- `README.md` — Fix any kind references

**Implementation**:
- Change `SubmitJob` to publish kind 5100 (Job Request) instead of 5003
- Change `PollJobStatus` to subscribe to kind 30100 (Job Status Update) and kind 5101 (Job Result)
- Add proper tag structure matching the Loom spec: `["p", worker_pubkey]`, `["cmd", ...]`, etc.
- Add a protocol compatibility matrix to docs

#### 0.4 Lifecycle Management for Background Workers

**Files to modify**:
- `internal/app/app.go` — Add a registry of background workers with start/stop lifecycle

**Implementation**:
```go
// Add to App struct:
type BackgroundRunner interface {
    Run(ctx context.Context)
    Name() string
}

runners []BackgroundRunner

// In Run(), start all runners with the lifecycle context:
for _, runner := range a.runners {
    go runner.Run(ctx)
}

// In shutdown, cancel context (already done) and wait with timeout
```

This allows Phase 1's Nostr subscriber, Phase 5's notification dispatcher, etc. to register cleanly.

#### 0.5 Protocol Compatibility Documentation

**New file**: `docs/protocol-compatibility.md`

Document which Nostr/Loom/Hive-CI event kinds Bahia:
- Publishes (outbound)
- Subscribes to (inbound, future)
- References in its data model

---

### Phase 1 — Nostr-Native Identity & Bidirectional Control Plane

**Goal**: Make Bahia authentically nostr-first by supporting NIP-98 auth, ingesting nostr events, and deriving actor identity from cryptographic signatures.
**Dependencies**: Phase 0
**Estimated effort**: 3–4 weeks

#### 1.1 Principal-Based Auth Model

**New files**:
- `internal/auth/principal.go`
- `internal/auth/nip98.go`

**Modified files**:
- `internal/auth/auth.go`
- `internal/auth/auth_test.go`

**Database changes**:
```sql
-- Replay protection for NIP-98 auth events
CREATE TABLE auth_events (
    event_id TEXT PRIMARY KEY,
    pubkey TEXT NOT NULL,
    method TEXT NOT NULL,
    url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_auth_events_expires ON auth_events(expires_at);
```

**Implementation details**:

Create a unified `Principal` type:
```go
type Principal struct {
    Subject string   // npub or JWT subject
    Method  Method   // "jwt", "nip98", "system"
    PubKey  string   // hex pubkey (set for nostr auth)
    Roles   []string // empty until RBAC phase
}
```

NIP-98 middleware:
- Parse `Authorization: Nostr <base64-encoded-kind-27235-event>`
- Validate Schnorr signature using go-nostr
- Verify URL tag matches request URL
- Verify method tag matches HTTP method
- Check `created_at` within configurable skew (default: 60s)
- Check event ID not in replay table
- Store event ID in replay table with TTL
- Set `Principal` on request context

Refactor existing JWT path to also produce `Principal` instead of `Claims`.

Add `GetPrincipal(ctx) *Principal` as the universal accessor.

#### 1.2 Server-Derived Actor Identity

**Modified files**:
- `internal/api/handlers/deployments.go`
- `internal/api/handlers/builds.go`
- `internal/api/dto/requests.go`

**Implementation**:
- When auth is enabled, override `requested_by` / actor fields with `Principal.Subject`
- When auth is disabled, keep current behavior (accept client-supplied value)
- If auth is enabled and client supplies a conflicting actor, return 400

#### 1.3 Nostr Event Ingestion

**New files**:
- `internal/adapters/nostr/subscriber.go`
- `internal/adapters/nostr/processor.go`

**Modified files**:
- `internal/app/app.go` — Wire subscriber as a background runner
- `internal/adapters/nostr/publisher.go` — Coordinate with subscriber on relay connections

**Database changes**:
```sql
ALTER TABLE nostr_events
    ADD COLUMN direction TEXT NOT NULL DEFAULT 'outbound',
    ADD COLUMN processing_status TEXT NOT NULL DEFAULT 'processed',
    ADD COLUMN processing_error TEXT,
    ADD COLUMN processed_at TIMESTAMPTZ;
CREATE INDEX idx_nostr_events_processing ON nostr_events(processing_status)
    WHERE processing_status != 'processed';
```

**Implementation**:

The subscriber:
- Connects to configured relays (reuse `NostrConfig.Relays`)
- Subscribes to relevant inbound kinds
- Persists every raw event to `nostr_events` with `direction='inbound'`
- Deduplicates by event ID
- Hands events to the processor

The processor maps inbound events to domain commands:

| Inbound Kind | Action |
|-------------|--------|
| Hive-CI 5402 (Workflow Result) | Call `RegistryService.RegisterBuild` + `RegisterArtifact` |
| Loom 10100 (Worker Advertisement) | Upsert worker in catalog (Phase 2) |
| Loom 30100 (Job Status Update) | Update `DeploymentRun` status |
| Loom 5101 (Job Result) | Call `RegistryService.CompleteDeploymentRun` |

Events with missing dependencies get `processing_status='deferred'` and are retried by a background sweep.

#### 1.4 NIP-05 Identity Resolution

**New file**: `internal/auth/nip05.go`

**Implementation**:
- On first NIP-98 auth from a pubkey, attempt NIP-05 resolution
- Cache result with TTL (e.g., 1 hour)
- Populate `Principal.NIP05` if resolved
- Store in a `nip05_cache` table or in-memory LRU
- Display in API responses where actor identity is shown

#### 1.5 Nostr Command Events (Inbound)

**New file**: `docs/nostr-commands.md`

Define Bahia-specific command kinds for fully nostr-native operation:

| Kind | Command | Maps to |
|------|---------|---------|
| 31100 | `build.register` | `POST /api/v1/builds` |
| 31101 | `artifact.register` | `POST /api/v1/artifacts` |
| 31102 | `deployment.intent.create` | `POST /api/v1/deployments/intents` |
| 31103 | `deployment.intent.approve` | `POST /api/v1/deployments/intents/{id}/approve` |
| 31104 | `deployment.intent.reject` | `POST /api/v1/deployments/intents/{id}/reject` |
| 31105 | `rollback.request` | `POST /api/v1/rollback` |

REST and Nostr ingestion must both call the same `RegistryService` methods — no parallel code paths.

---

### Phase 2 — Loom Worker Discovery & Cashu Payments

**Goal**: Make Bahia aware of available compute workers and enable payment-gated deployments.
**Dependencies**: Phase 1 (Nostr ingestion)
**Estimated effort**: 3–4 weeks

#### 2.1 Worker Catalog

**New files**:
- `internal/domain/worker.go`
- `internal/repository/interfaces_worker.go`
- `internal/repository/pg_worker.go`
- `internal/service/workers.go`
- `internal/api/handlers/workers.go`

**Database changes**:
```sql
CREATE TABLE workers (
    pubkey TEXT PRIMARY KEY,
    name TEXT,
    description TEXT,
    architecture TEXT,
    max_concurrent_jobs INT,
    current_queue_depth INT,
    software JSONB NOT NULL DEFAULT '[]'::jsonb,
    pricing JSONB NOT NULL DEFAULT '[]'::jsonb,
    min_duration_secs INT,
    max_duration_secs INT,
    geohash TEXT,
    preferred_relays JSONB NOT NULL DEFAULT '[]'::jsonb,
    last_advertisement_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'active',  -- active, stale, offline
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_workers_status ON workers(status);
CREATE INDEX idx_workers_architecture ON workers(architecture);
```

**API endpoints**:
```
GET  /api/v1/workers                    — List workers (filterable by software, architecture, status)
GET  /api/v1/workers/{pubkey}           — Get worker details
GET  /api/v1/workers/{pubkey}/pricing   — Get worker pricing
POST /api/v1/workers/match              — Find best workers for a given job requirement
```

**Implementation**:
- Ingest Loom kind 10100 (Worker Advertisement) events via the Phase 1 subscriber
- Parse software tags (`["S", name, version, path]`), pricing tags (`["price", mint, rate, unit]`), architecture, duration limits
- Upsert into `workers` table on each advertisement
- Mark workers as `stale` if no advertisement received within 2× their expected refresh interval
- Mark as `offline` after configurable timeout (e.g., 30 min)
- Worker matching: score workers by capability match, price, queue depth, geographic proximity

#### 2.2 Enhanced Loom Client

**Modified files**:
- `internal/adapters/loom/client.go`

**Implementation**:
- Implement proper Loom protocol event structures (kind 5100 job request with all required tags)
- Add worker selection: accept target worker pubkey, or auto-select from catalog
- Add job cancellation support (kind 5102)
- Add real-time status monitoring via kind 30100 subscription
- Add Cashu payment token inclusion in job request tags
- Replace polling with event-driven completion via kind 5101 subscription

#### 2.3 Cashu Payment Integration

**New files**:
- `internal/adapters/cashu/wallet.go`
- `internal/adapters/cashu/mint.go`
- `internal/domain/payment.go`
- `internal/repository/pg_payment.go`
- `internal/service/payments.go`
- `internal/api/handlers/payments.go`

**Database changes**:
```sql
CREATE TABLE payment_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_run_id UUID REFERENCES deployment_runs(id),
    worker_pubkey TEXT NOT NULL,
    mint_url TEXT NOT NULL,
    amount_sats BIGINT NOT NULL,
    token_hash TEXT NOT NULL,
    direction TEXT NOT NULL,  -- 'payment' or 'change'
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, redeemed, expired, refunded
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    redeemed_at TIMESTAMPTZ
);
CREATE INDEX idx_payment_records_run ON payment_records(deployment_run_id);
CREATE INDEX idx_payment_records_status ON payment_records(status);
```

**API endpoints**:
```
GET  /api/v1/deployments/runs/{id}/cost  — Get actual/estimated cost for a deployment run
POST /api/v1/payments/estimate           — Estimate deployment cost (service, env, worker)
GET  /api/v1/payments/history            — Payment history
```

**Implementation**:
- Cost estimation: query worker pricing + estimated execution time
- Payment creation: mint Cashu tokens locked to worker pubkey
- Payment tracking: record token hash, amount, status
- Change redemption: automatically redeem change tokens from job results
- Integration with `workflow.Coordinator`: inject payment token into Loom job request
- Support for operator-funded and user-funded payment models

#### 2.4 Worker Selection Policies

**New file**: `internal/service/worker_policy.go`

**Implementation**:
- Define worker selection strategies per environment:
  - `cheapest` — lowest price per second
  - `fastest` — lowest queue depth
  - `nearest` — geographic proximity (geohash)
  - `preferred` — match `loom_worker_selector` JSONB from environment config
  - `reputation` — future WoT integration
- Store policy in `environments.runtime_config` JSONB
- Apply during deployment execution in `Coordinator.ExecuteDeployment`

---

### Phase 3 — Supply Chain Security & Multi-Registry Support

**Goal**: Make Bahia a trust anchor for container supply chain integrity.
**Dependencies**: Phase 1 (auth model), partially Phase 0
**Estimated effort**: 4–5 weeks

#### 3.1 Generic OCI Registry Adapter

**New files**:
- `internal/adapters/registry/oci.go` — Generic OCI Distribution API client
- `internal/adapters/registry/ghcr.go` — GitHub Container Registry adapter
- `internal/adapters/registry/dockerhub.go` — Docker Hub adapter
- `internal/adapters/registry/factory.go` — Registry factory based on URL

**Modified files**:
- `internal/service/registry.go` — Broaden `ImageVerifier` interface
- `internal/adapters/harbor/verifier.go` — Implement broadened interface

**Implementation**:

Broaden the `ImageVerifier` interface:
```go
type ImageInspection struct {
    Exists          bool
    Digest          string
    ScanStatus      string
    MediaType       string
    Size            int64
    Signatures      []SignatureInfo
    SBOMRef         string
    ProvenanceRef   string
    Annotations     map[string]string
}

type ImageVerifier interface {
    VerifyImage(ctx context.Context, imageRepo, reference string) (*ImageInspection, error)
    ListTags(ctx context.Context, imageRepo string) ([]string, error)
    GetReferrers(ctx context.Context, imageRepo, digest string) ([]ReferrerInfo, error)
}
```

The generic OCI client uses the OCI Distribution Specification API:
- `GET /v2/{name}/manifests/{reference}` for existence/digest
- `GET /v2/{name}/tags/list` for tag listing
- `GET /v2/{name}/referrers/{digest}` for signatures/SBOMs/provenance (OCI Referrers API)

Registry auto-detection from image URL:
- `ghcr.io/*` → GHCR adapter (token auth)
- `docker.io/*` or `library/*` → Docker Hub adapter
- `harbor.*` → Harbor adapter
- Others → generic OCI adapter

#### 3.2 Image Signature Verification (Cosign/Sigstore)

**New files**:
- `internal/adapters/signing/cosign.go`
- `internal/adapters/signing/nostr_sign.go`
- `internal/domain/signature.go`

**Database changes**:
```sql
CREATE TABLE artifact_signatures (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    signer_identity TEXT NOT NULL,  -- cosign key, nostr pubkey, fulcio identity
    signature_type TEXT NOT NULL,   -- 'cosign', 'nostr', 'sigstore'
    signature_ref TEXT NOT NULL,    -- OCI referrer or blossom URL
    verified BOOLEAN NOT NULL DEFAULT false,
    verified_at TIMESTAMPTZ,
    verification_error TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_artifact_signatures_artifact ON artifact_signatures(artifact_id);
```

**Implementation**:
- Cosign verification: shell out to `cosign verify` or use the Go library
- Nostr-native signing: verify that an artifact's signature event was signed by a trusted nostr pubkey
- Sigstore/Fulcio: keyless verification via certificate transparency
- On artifact registration, automatically check for referrer signatures
- Store verification results in `artifact_signatures`
- Reject deployments of unsigned artifacts when policy requires it

#### 3.3 SBOM Ingestion & Analysis

**New files**:
- `internal/adapters/sbom/parser.go`
- `internal/domain/sbom.go`
- `internal/repository/pg_sbom.go`
- `internal/api/handlers/sbom.go`

**Database changes**:
```sql
CREATE TABLE artifact_sboms (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id UUID NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    format TEXT NOT NULL,       -- 'spdx', 'cyclonedx'
    source_url TEXT NOT NULL,   -- blossom or OCI referrer URL
    package_count INT,
    vulnerability_count INT,
    critical_count INT,
    high_count INT,
    parsed_at TIMESTAMPTZ,
    raw_hash TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_artifact_sboms_artifact ON artifact_sboms(artifact_id);

CREATE TABLE sbom_packages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sbom_id UUID NOT NULL REFERENCES artifact_sboms(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    ecosystem TEXT,  -- npm, pip, go, etc.
    license TEXT,
    purl TEXT,       -- package URL
    cpe TEXT
);
CREATE INDEX idx_sbom_packages_sbom ON sbom_packages(sbom_id);
CREATE INDEX idx_sbom_packages_name ON sbom_packages(name);
```

**API endpoints**:
```
GET  /api/v1/artifacts/{id}/sbom           — Get SBOM summary for an artifact
GET  /api/v1/artifacts/{id}/sbom/packages  — List packages in an artifact's SBOM
GET  /api/v1/sbom/search?package=log4j     — Search for a package across all artifacts
```

**Implementation**:
- Parse SPDX and CycloneDX formats
- Download SBOM from `sbom_url` (Blossom) or OCI referrers
- Extract package inventory and license info
- Store normalized package data for cross-artifact queries
- Enable "find all deployments using package X" queries

#### 3.4 Deployment Policy Engine

**New files**:
- `internal/service/policy.go`
- `internal/domain/policy.go`
- `internal/repository/pg_policy.go`
- `internal/api/handlers/policies.go`

**Database changes**:
```sql
CREATE TABLE deployment_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    environment_id UUID REFERENCES environments(id),  -- NULL = global
    rules JSONB NOT NULL,  -- array of policy rules
    enforcement TEXT NOT NULL DEFAULT 'warn',  -- 'warn', 'block'
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Policy rule examples**:
```json
{
  "rules": [
    {"type": "require_signature", "signers": ["npub1..."]},
    {"type": "require_sbom", "formats": ["spdx", "cyclonedx"]},
    {"type": "max_critical_vulns", "count": 0},
    {"type": "max_high_vulns", "count": 5},
    {"type": "require_scan_status", "status": "clean"},
    {"type": "block_package", "name": "log4j", "versions": ["<2.17.0"]},
    {"type": "require_approval", "min_approvers": 2}
  ]
}
```

**Implementation**:
- Evaluate policies during `CreateDeploymentIntent`
- Block or warn based on enforcement level
- Record policy evaluation results in intent metadata
- Support per-environment and global policies

---

### Phase 4 — Advanced Deployment Capabilities

**Goal**: Enable production-grade progressive delivery, secrets management, and multi-runtime support.
**Dependencies**: Phase 2 (Loom integration), Phase 0 (lifecycle management)
**Estimated effort**: 5–6 weeks

#### 4.1 Progressive Delivery (Canary & Blue-Green)

**New files**:
- `internal/rollout/strategy.go`
- `internal/rollout/canary.go`
- `internal/rollout/bluegreen.go`
- `internal/rollout/health_gate.go`
- `internal/domain/rollout.go`
- `internal/repository/pg_rollout.go`

**Database changes**:
```sql
CREATE TABLE rollout_plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_intent_id UUID NOT NULL REFERENCES deployment_intents(id),
    strategy TEXT NOT NULL,  -- 'canary', 'blue_green', 'replace'
    steps JSONB NOT NULL,    -- ordered array of rollout steps
    current_step INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE rollout_steps (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rollout_plan_id UUID NOT NULL REFERENCES rollout_plans(id) ON DELETE CASCADE,
    step_order INT NOT NULL,
    action TEXT NOT NULL,       -- 'deploy_canary', 'shift_traffic', 'observe', 'promote', 'rollback'
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    health_result JSONB,
    UNIQUE(rollout_plan_id, step_order)
);
```

**Canary step example**:
```json
{
  "steps": [
    {"action": "deploy_canary", "config": {"weight": 10}},
    {"action": "observe", "config": {"duration": "5m", "success_threshold": 0.99}},
    {"action": "shift_traffic", "config": {"weight": 50}},
    {"action": "observe", "config": {"duration": "5m", "success_threshold": 0.99}},
    {"action": "promote", "config": {"weight": 100}},
  ]
}
```

**Implementation**:
- When `deploy_strategy` is canary or blue-green, create a `RolloutPlan` instead of a single run
- Each step creates its own `DeploymentRun` if needed
- Health gates poll the runtime observer between steps
- Automatic rollback if health gate fails
- The reconciler skips rollout-managed states (marked as `deploying`)

#### 4.2 Secrets Management

**New files**:
- `internal/adapters/secrets/nip44.go`
- `internal/adapters/secrets/vault.go` (optional HashiCorp Vault adapter)
- `internal/domain/secret.go`
- `internal/repository/pg_secret.go`
- `internal/api/handlers/secrets.go`

**Database changes**:
```sql
CREATE TABLE service_secrets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_id UUID NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    environment_id UUID REFERENCES environments(id),  -- NULL = all environments
    name TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    encryption_method TEXT NOT NULL,  -- 'nip44', 'aes256gcm'
    version INT NOT NULL DEFAULT 1,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(service_id, environment_id, name)
);
```

**API endpoints**:
```
POST   /api/v1/services/{id}/secrets         — Create/update a secret
GET    /api/v1/services/{id}/secrets         — List secret names (not values)
DELETE /api/v1/services/{id}/secrets/{name}  — Delete a secret
```

**Implementation**:
- Secrets encrypted at rest using NIP-44 (encrypted to Bahia's service key) or AES-256-GCM
- During deployment, secrets are re-encrypted to the target worker's pubkey using NIP-44
- Injected as `["secret", key, nip44_encrypted_value]` tags in Loom job requests
- Secret values never appear in API responses, logs, or nostr events
- Version tracking for secret rotation

#### 4.3 Multi-Runtime Support

**New files**:
- `internal/adapters/runtime/compose.go`
- `internal/adapters/runtime/kubernetes.go`
- `internal/adapters/runtime/factory.go`

**Modified files**:
- `internal/adapters/runtime/docker.go` — Implement extended `Observer` interface
- `internal/reconcile/reconciler.go` — Use runtime factory based on environment config

**Implementation**:

Extend the `Observer` interface:
```go
type Observer interface {
    Observe(ctx context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error)
}

type Deployer interface {
    Deploy(ctx context.Context, artifact *domain.Artifact, env *domain.Environment, secrets []Secret) error
    Undeploy(ctx context.Context, serviceID uuid.UUID, env *domain.Environment) error
    StreamLogs(ctx context.Context, containerID string) (io.ReadCloser, error)
}

type Runtime interface {
    Observer
    Deployer
}
```

Runtime selection based on `environments.runtime_type`:
- `docker` → DockerRuntime (Docker Engine API)
- `compose` → ComposeRuntime (Docker Compose CLI or API)
- `k8s` → KubernetesRuntime (Kubernetes API via kubeconfig)

#### 4.4 Container Log Streaming

**New files**:
- `internal/adapters/runtime/logs.go`
- `internal/api/handlers/logs.go`

**API endpoints**:
```
GET /api/v1/deployments/runs/{id}/logs      — Get logs (from Blossom or runtime)
GET /api/v1/services/{id}/environments/{envId}/logs  — Stream live container logs (SSE)
```

**Implementation**:
- For completed runs: fetch from `stdout_ref`/`stderr_ref` (Blossom URLs)
- For running containers: stream from Docker/Compose/K8s API
- Support `?follow=true` for SSE streaming
- Support `?tail=100` for recent lines

#### 4.5 Blossom Storage Integration

**New files**:
- `internal/adapters/blossom/client.go`
- `internal/adapters/blossom/upload.go`

**Implementation**:
- Download artifacts referenced by Blossom URLs (SBOMs, logs, signatures)
- Upload deployment logs and reports to Blossom
- SHA-256 verification on all downloads
- Multi-server support for redundancy
- Integrate with log streaming and SBOM analysis

#### 4.6 Auto-Remediation

**New file**: `internal/reconcile/remediation.go`

**Implementation**:
- When drift is detected and auto-remediation is enabled for the environment:
  - If the observed state diverged from desired: re-deploy the desired artifact
  - If health checks fail: automatic rollback to last known good
- Configurable per environment via `runtime_config`:
  ```json
  {
    "auto_remediation": {
      "enabled": true,
      "max_retries": 3,
      "cooldown": "5m",
      "on_drift": "redeploy",
      "on_health_failure": "rollback"
    }
  }
  ```
- Rate-limit remediation to prevent thrashing

---

### Phase 5 — Platform Experience & Enterprise Features

**Goal**: Complete the platform with real-time UX, observability, multi-tenancy, and a web dashboard.
**Dependencies**: All previous phases (can be partially parallelized)
**Estimated effort**: 6–8 weeks

#### 5.1 Real-Time Event Stream (WebSocket/SSE)

**New files**:
- `internal/api/handlers/events_stream.go`
- `internal/api/handlers/websocket.go`

**API endpoints**:
```
GET /api/v1/events/stream                    — SSE stream of all events
GET /api/v1/events/stream?service={id}       — SSE stream filtered by service
GET /api/v1/events/stream?environment={id}   — SSE stream filtered by environment
WS  /api/v1/ws                               — WebSocket for bidirectional event stream
```

**Implementation**:
- Subscribe to the internal `events.Publisher` for real-time event delivery
- SSE format for simple clients (curl, browser EventSource)
- WebSocket for richer clients (dashboards, CLI with `--follow`)
- Events include: deployment status changes, drift detection, build registration, health status changes
- Per-connection filters (by service, environment, event type)
- Heartbeat/keepalive for connection health
- Backpressure: drop events for slow consumers after configurable buffer

#### 5.2 Webhook Notifications

**New files**:
- `internal/notifications/dispatcher.go`
- `internal/notifications/webhook.go`
- `internal/notifications/nostr_dm.go`
- `internal/domain/notification.go`
- `internal/repository/pg_notification.go`
- `internal/api/handlers/notifications.go`

**Database changes**:
```sql
CREATE TABLE notification_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    channel_type TEXT NOT NULL,  -- 'webhook', 'nostr_dm'
    config JSONB NOT NULL,      -- URL, headers, pubkey, etc.
    event_filter JSONB,         -- which events trigger this channel
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE notification_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID NOT NULL REFERENCES notification_channels(id),
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL,       -- 'sent', 'failed', 'retrying'
    attempts INT NOT NULL DEFAULT 1,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**API endpoints**:
```
POST   /api/v1/notifications/channels        — Create notification channel
GET    /api/v1/notifications/channels        — List channels
PUT    /api/v1/notifications/channels/{id}   — Update channel
DELETE /api/v1/notifications/channels/{id}   — Delete channel
POST   /api/v1/notifications/channels/{id}/test — Send test notification
GET    /api/v1/notifications/log             — View notification history
```

**Implementation**:
- Webhook: HTTP POST with configurable headers, retry with exponential backoff
- Nostr DM: NIP-17 encrypted direct message to operator pubkey
- Event filtering: subscribe only to specific event types
- Templates: configurable payload templates per channel
- Delivery guarantees: persist to `notification_log`, retry on failure

#### 5.3 OpenTelemetry Observability

**Modified files**:
- `internal/adapters/telemetry/telemetry.go` — Replace stub with real implementation
- `internal/app/app.go` — Initialize OTLP exporter
- `internal/api/middleware/logging.go` — Add trace context propagation
- `internal/api/middleware/metrics.go` (new) — Prometheus/OTLP metrics

**Implementation**:

Traces:
- HTTP request tracing (auto-instrument chi router)
- Database query tracing (pgx hooks)
- Nostr publish/subscribe tracing
- Loom job submission/completion tracing
- Reconciliation loop tracing

Metrics:
```
bahia_http_requests_total{method, path, status}
bahia_http_request_duration_seconds{method, path}
bahia_deployments_total{service, environment, status}
bahia_deployment_duration_seconds{service, environment}
bahia_drift_detected_total{service, environment}
bahia_reconcile_duration_seconds
bahia_reconcile_states_checked
bahia_nostr_events_published_total{kind}
bahia_nostr_events_received_total{kind}
bahia_workers_active
bahia_loom_jobs_inflight
bahia_cashu_payments_total{direction, status}
```

Expose:
```
GET /metrics — Prometheus-compatible metrics endpoint
```

#### 5.4 Multi-Tenant RBAC

**New files**:
- `internal/auth/rbac.go`
- `internal/domain/tenant.go`
- `internal/repository/pg_tenant.go`
- `internal/api/handlers/tenants.go`
- `internal/api/middleware/rbac.go`

**Database changes**:
```sql
CREATE TABLE organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    display_name TEXT,
    owner_pubkey TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
    org_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    pubkey TEXT NOT NULL,
    role TEXT NOT NULL,  -- 'owner', 'admin', 'deployer', 'viewer'
    nip05 TEXT,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, pubkey)
);

-- Add org ownership to services and environments
ALTER TABLE services ADD COLUMN org_id UUID REFERENCES organizations(id);
ALTER TABLE environments ADD COLUMN org_id UUID REFERENCES organizations(id);
CREATE INDEX idx_services_org ON services(org_id);
CREATE INDEX idx_environments_org ON environments(org_id);
```

**Roles and permissions**:

| Role | Services | Environments | Deployments | Secrets | Settings |
|------|----------|-------------|-------------|---------|----------|
| viewer | read | read | read | — | — |
| deployer | read | read | create/approve | read names | — |
| admin | CRUD | CRUD | full | CRUD | read |
| owner | CRUD | CRUD | full | CRUD | CRUD |

**Implementation**:
- RBAC middleware checks `Principal.PubKey` against `org_members`
- All queries filtered by org_id when multi-tenant is enabled
- Organization management API
- Member invitation via nostr DM or NIP-05 lookup
- Cross-org visibility disabled by default

#### 5.5 Web Dashboard

**New directory**: `web/` (SvelteKit or similar)

**Key views**:
- **Dashboard**: Overview of all services, environments, deployment status, drift alerts
- **Service detail**: Build history, artifact list, deployment timeline
- **Environment detail**: Current state, deployed artifact, health, drift status
- **Deployment detail**: Intent → run → logs → result timeline
- **Worker catalog**: Available workers, pricing, status, capabilities
- **Policy management**: Create and edit deployment policies
- **Notifications**: Channel configuration and delivery log
- **Settings**: Organization, members, secrets, integrations

**Implementation approach**:
- Server-side rendered with SSE for real-time updates
- Nostr Connect (NIP-46) or NIP-07 browser extension for authentication
- Consumes the REST API + SSE event stream
- Deployed alongside or separately from the Go API server
- Optional: serve static assets from the Go server itself

#### 5.6 Enhanced CLI

**Modified files**:
- `cmd/cli/main.go`
- `pkg/client/client.go`

**New commands**:
```
bahia login --nip46 <bunker-url>          — Login with Nostr Connect
bahia login --nsec <nsec>                  — Login with nsec
bahia whoami                               — Show current identity

bahia workers list                         — List available workers
bahia workers show <pubkey>                — Worker details

bahia deploy --follow                      — Deploy and stream status updates
bahia logs <service> <env>                 — Stream live container logs

bahia policies list                        — List deployment policies
bahia policies create --file policy.json   — Create a policy

bahia secrets set <service> <key>          — Set a secret (reads from stdin)
bahia secrets list <service>               — List secret names
bahia secrets delete <service> <key>       — Delete a secret

bahia orgs create <name>                   — Create organization
bahia orgs members list                    — List org members
bahia orgs members add <npub> --role deployer — Add member
```

---

## Summary: Priority Matrix

| Phase | Effort | Impact | Key Differentiator |
|-------|--------|--------|-------------------|
| **Phase 0**: Foundation | 1–2 weeks | High (unblocks everything) | Stability & correctness |
| **Phase 1**: Nostr Identity & Control Plane | 3–4 weeks | **Critical** | NIP-98 auth, event ingestion — this is what makes Bahia *nostr-first* |
| **Phase 2**: Workers & Payments | 3–4 weeks | **Critical** | Worker discovery + Cashu payments — decentralized compute marketplace integration |
| **Phase 3**: Supply Chain Security | 4–5 weeks | High | Multi-registry + signing + SBOM — trust anchor for container supply chain |
| **Phase 4**: Advanced Deployment | 5–6 weeks | High | Progressive delivery + secrets + multi-runtime — production-grade deployments |
| **Phase 5**: Platform Experience | 6–8 weeks | Medium-High | Dashboard + RBAC + observability — complete platform experience |

**Total estimated effort**: 22–29 weeks for the complete roadmap.

**What makes this world-class**:
1. **Nostr-native from authentication to audit** — no other container registry has this
2. **Decentralized compute marketplace** — deploy to any Loom worker, pay with Cashu
3. **Cryptographic supply chain** — nostr signatures + cosign + SBOM, all anchored in verifiable events
4. **Full deployment lifecycle** — from build registration to canary rollout to drift detection to auto-remediation
5. **Open and composable** — REST + Nostr events + WebSocket; any tool can integrate
6. **Zero vendor lock-in** — any OCI registry, any Loom worker, any nostr relay
