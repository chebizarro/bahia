# Investigation: REST API Surface Audit in Bahia

## Summary
Comprehensive audit of REST API endpoints in Bahia codebase. Found **~100+ REST endpoints** across incoming API and **~50+ outbound HTTP calls** to external services. Bahia already has strong Nostr-first primitives with command/request kinds (5961-6006, 38390-38431), result kinds (6961-7997, 38395-38399), and read-model kinds (31961-32003). REST should remain only for HTTP-native boundaries (Docker Engine, OCI registries, health probes, browser compatibility).

## Symptoms
- Concern that REST APIs may exist where Nostr-first alternatives should be used
- Need to identify which REST endpoints are essential for non-Nostr interoperability vs. legacy patterns

## Background / Prior Research

### Nostr Kind Coverage Already in Bahia
From `internal/kinds/kinds.go` and `policy.go`:
- **Command/Request kinds**: 5961-6006 (deploy, rollback, service CRUD, observation, secrets, tool approval, adoption, workers)
- **Status kinds**: 6961-6997 (deployment status, action status, policy eval status, worker operation status)
- **Result kinds**: 7961-7997 (deployment results, action results, policy eval results)
- **ML/AI kinds**: 38390-38399 (recipe run, inference deploy/rollback, model import)
- **Read-model kinds**: 31961-32003 (service state, registries, LLM routes, workers, policies)
- **Backup kinds**: 38400-38419
- **Assistant kinds**: 38420-38423
- **Continuity kinds**: 38430-38431, 30350-30353, 31400-31404
- **DNS kinds**: 5941-5945, 7941-7945, 31975-31978
- **Package kinds**: 5991-5996, 7991-7992, 31971-31973
- **Audit kinds**: 31000-31005, 31200 (artifact attestation), 31310-31311
- **NIP-98 HTTP auth**: kind 27235
- **Loom worker ads**: 10100/30100/5101/5102
- **HiveCI workflow**: 5401/5402

## Investigator Findings

### 2026-06-01 Pair Verification

#### Nostr kind coverage
- Confirmed the concrete command/result/read-model examples in this report exist in `internal/kinds/kinds.go`: `DeployRequest = 5961`, `RollbackRequest = 5962`, `ServiceAction = 5963`, `ServiceCreate = 5964`, `EnvironmentCreate = 5965`, `DeploymentApproval = 5966`, `ObservationSubmit = 5967`, `LLMRouteCreate = 5971`, `ToolApprovalRequest = 5977`, `AdoptionScanRequest = 5978`, `AdoptionImportRequest = 5979`, `ServiceUpdate/Delete = 5981/5982`, `EnvironmentUpdate/Delete = 5983/5984`, `ArtifactRegister = 5985`, and `PolicyCreate/Update/Delete/Evaluate = 5986-5989` (`internal/kinds/kinds.go:39-65`). Package and worker request kinds extend that request family through `PackageDriftDetect = 5996` and `WorkerCleanupRequest = 6006` (`internal/kinds/kinds.go:73-96`).
- Confirmed status/result examples exist: `DeploymentStatus = 6961`, `ServiceStatus = 6962`, `ActionStatus = 6963`, `WorkerStatus = 6997`, `DeploymentResult = 7961`, `ActionResult = 7962`, `ServiceCreateResult = 7963`, `EnvironmentCreateResult = 7964`, and `WorkerResult = 7997` (`internal/kinds/kinds.go:103-135`).
- Confirmed ML/AI, backup, assistant, continuity, DNS, package, audit, NIP-98, Loom, and HiveCI families exist at the claimed ranges: ML commands/results `38390-38399` (`internal/kinds/kinds.go:343-353`), backup `38400-38419` (`internal/kinds/kinds.go:360-380`), assistant `38420-38423` (`internal/kinds/kinds.go:387-392`), continuity runtime/definition/request kinds (`internal/kinds/kinds.go:164-181`), DNS `5941-5945` and `7941-7945` (`internal/kinds/kinds.go:19-31`), package `5991-5996` and `7991-7992` (`internal/kinds/kinds.go:73-80`, `internal/kinds/kinds.go:133-134`), audit `31000-31024` with `AuditMin/AuditMax = 31000/31099` (`internal/kinds/kinds.go:208-238`), `NostrSignature = 31200` (`internal/kinds/kinds.go:244-246`), backup attestations `31310-31311` (`internal/kinds/kinds.go:252-255`), `HTTPAuth = 27235` (`internal/kinds/kinds.go:398-399`), Loom (`internal/kinds/kinds.go:146-150`), and HiveCI (`internal/kinds/kinds.go:151-153`).
- Confirmed read-model kinds include `ServiceState = 31961` through DNS read models, ML read models `31980-31989`, backup read models `31991-31999`, and worker states `32000-32003` (`internal/kinds/kinds.go:261-318`). Note that `31979` is intentionally unused, so `31961-32003` is a family shorthand, not a fully contiguous range.
- `internal/kinds/policy.go` classifies these kinds rather than defining constants. It marks DNS/core/package/worker/ML/backup/assistant/continuity commands as request kinds (`internal/kinds/policy.go:6-32`) and marks status/result/read-model/audit/ML-result/backup-result/assistant-result kinds as Bahia projection kinds (`internal/kinds/policy.go:39-108`). It also enumerates request, status, result, and read-model lists in `AllRequestKinds`, `AllStatusKinds`, `AllResultKinds`, and `AllReadModelKinds` (`internal/kinds/policy.go:125-242`).
- Accuracy caveat: the report's range wording is shorthand, not contiguous allocation. There are gaps such as `5969`, `5970`, `5990`, and `31979`. Also, `PolicyEvaluate = 5989` exists (`internal/kinds/kinds.go:65`), but no dedicated policy-evaluation status/result constants appear in the kind lists; `AllStatusKinds` and `AllResultKinds` enumerate statuses/results without a policy-evaluation entry (`internal/kinds/policy.go:174-213`).

#### ML handler response pattern
- Confirmed `internal/api/handlers/ml.go` is the current model for signer-first REST compatibility. Its `MLCommandPublisher` interface explicitly publishes ML command events and returns `*controlplane.MLCommandReceipt` (`internal/api/handlers/ml.go:15-20`). The write handlers (`ImportModel`, `RunRecipe`, `Deploy`, `Rollback`) all call `publishAsync` (`internal/api/handlers/ml.go:218-236`).
- `publishAsync` returns `202 Accepted` with `dto.CommandReceipt` fields populated from the Nostr receipt: `request_event_id`, `request_pubkey`, `request_kind`, `result_kind`, `read_model_kinds`, `idempotency_key`, `status`, `error`, `retry_hint`, `published_relays`, a 30-second hint, and the message instructing callers to subscribe to Nostr result/read-model events for completion (`internal/api/handlers/ml.go:247-270`; DTO shape at `internal/api/dto/command_receipt.go:5-18`).
- The underlying ML publisher signs and publishes a Nostr event, then returns the signed event ID/pubkey, request/result kinds, read-model kinds, `d`/idempotency tag, and relay acceptance count (`internal/controlplane/ml_command_publisher.go:123-156`). It fail-closes when no relay accepts the request (`internal/controlplane/ml_command_publisher.go:151-153`).
- Accuracy caveat: `dto.CommandReceipt` supports `status_kind`, but the ML handler does not populate it (`internal/api/dto/command_receipt.go:8-10`; `internal/api/handlers/ml.go:258-270`). That is consistent with the current ML kinds, which use request/result/read-model correlation but no separate ML status kind.

#### Missed HTTP surfaces outside the API router
- Found one externally served HTTP/WebSocket surface not clearly inventoried in the report: the relay sidecar. `internal/relaysidecar/server.go` builds a `http.NewServeMux`, registers `/`, `publicPath`, and `publicPath + "/"`, and serves the Khatru relay for NIP-11 HTTP metadata plus Nostr WebSocket traffic (`internal/relaysidecar/server.go:87-107`). It creates an `http.Server` and calls `ListenAndServe` (`internal/relaysidecar/server.go:127-143`), and `cmd/relay/main.go` starts it via `relaysidecar.New(...).Run(ctx)` (`cmd/relay/main.go:35-42`). This is not a REST CRUD surface, but it should be listed under permanent Nostr relay HTTP/WebSocket infrastructure.
- Confirmed the main Bahia server is the expected API server: `cmd/server/main.go` loads config, constructs `app.New`, and calls `application.Run()` (`cmd/server/main.go:13-26`); `internal/app/app.go` constructs `http.Server` with the API router handler (`internal/app/app.go:938-942`) and starts it with `ListenAndServe` (`internal/app/app.go:1472-1476`).
- Confirmed the Prometheus metrics handler is implemented outside the router but mounted by the router as `/metrics`: `telemetry.Provider.MetricsHandler()` returns an `http.HandlerFunc` (`internal/adapters/telemetry/telemetry.go:398-404`), while the route is mounted in `internal/api/router/router.go:138-144`. This is already covered by the report's health/metrics category.
- Searches found no gin or echo route registrations in `internal/` or `cmd/`, and no additional chi route registrations outside `internal/api/router/`. Other `http.HandlerFunc` matches are middleware/auth wrappers or tests, not standalone REST endpoints.

#### Transitional endpoint behavior spot-check
- `POST /services` has a valid Nostr analog (`ServiceCreate = 5964`, `ServiceCreateResult = 7963`), but the REST handler does not publish a Nostr command or return correlation metadata. It validates/constructs a `domain.Service`, calls `h.registry.CreateService`, and returns the service with `201 Created` (`internal/api/handlers/services.go:24-62`). `RegistryService.CreateService` writes through `s.services.Create` and emits an in-process `events.EventServiceCreated`, not a Nostr request event (`internal/service/registry.go:90-105`).
- `POST /deployments/intents` has a valid Nostr analog (`DeployRequest = 5961`, `DeploymentStatus = 6961`, `DeploymentResult = 7961`), but the REST handler is REST-only ingress. It validates the DTO, constructs `domain.DeploymentIntent`, calls `h.registry.CreateDeploymentIntent`, and returns the intent with `201 Created` (`internal/api/handlers/deployments.go:37-98`). `RegistryService.CreateDeploymentIntent` writes repository state, updates desired state, and emits in-process events such as `EventDeploymentIntentCreated`/`EventDeploymentIntentApproved` (`internal/service/registry.go:507-593`), but does not publish a Nostr command receipt.
- `POST /llm/routes` has a valid Nostr analog (`LLMRouteCreate = 5971`, `LLMRouteCreateResult = 7971`), but the mounted REST route calls `LLMHandler.CreateRoute`, which directly calls `h.registry.CreateRoute` and returns the route with `201 Created` (`internal/api/router/router.go:428-432`; `internal/api/handlers/llm.go:37-48`). `LLMRegistryService.CreateRoute` writes either the ML-backed model registry path or the LLM route repository and emits an in-process `EventLLMRouteCreated` only in the non-ML-backed path (`internal/service/llm_registry.go:70-102`). It does not use `controlplane.LLMCommandPublisher` from the REST handler path.
- By contrast, the ML write compatibility routes are accurately described as Nostr-publishing: router comments label them as publishing Nostr commands (`internal/api/router/router.go:420-426`), the ML handler invokes `MLCommandPublisher`, and the control-plane publisher signs/publishes Nostr events before returning correlation metadata (`internal/api/handlers/ml.go:218-270`; `internal/controlplane/ml_command_publisher.go:123-156`).

#### Report accuracy adjustments
- The recommendation to return Nostr correlation metadata on all REST mutations is still valid, but current coverage is narrower than the report may imply: ML write endpoints do this; representative service, deployment-intent, policy, and LLM route REST writes do not. Policy REST writes call `PolicyService.CreatePolicy/UpdatePolicy/DeletePolicy/Evaluate` through the handler (`internal/api/handlers/policies.go:61-204`), and `PolicyService.CreatePolicy` is direct repository CRUD (`internal/service/policy.go:544-547`).
- The “has Nostr analog” column should be read as “a command kind/reactor path exists,” not “the REST endpoint already publishes that command internally.” The reactor does handle Nostr commands for service create/update/delete, deploy/rollback, LLM route/deploy, policy create/update/delete/evaluate, and ML commands (`internal/controlplane/reactor.go:607-668`), but most legacy REST mutations bypass that Nostr command path today.

## Investigation Log

### Phase 1 - Initial File Discovery
**Hypothesis:** REST APIs exist in `internal/api/` and adapters make outbound HTTP calls
**Findings:**
- 39 handler files in `internal/api/handlers/`
- 444 HTTP references in `internal/adapters/`
- Main router at `internal/api/router/router.go`
**Evidence:** File tree and search results
**Conclusion:** Confirmed - substantial REST surface exists

### Phase 2 - Context Builder Analysis
**Hypothesis:** REST APIs can be categorized by necessity and Nostr analog availability
**Findings:** Comprehensive inventory created (see below)
**Evidence:** Selected 64 files (113k tokens) including router, handlers, adapters, and Nostr kinds
**Conclusion:** Clear categorization possible - see Root Cause section

## Root Cause

### REST Classification Summary

#### 1. ESSENTIAL REST (Must Keep) - HTTP-Native Boundaries

| Category | Endpoints/Calls | Reason |
|----------|-----------------|--------|
| **Health/Metrics** | `/health`, `/ready`, `/metrics` | Infrastructure probes require HTTP |
| **OCI Distribution** | `/v2/*` | OCI clients require HTTP registry API |
| **MCP JSON-RPC** | `/mcp` | MCP protocol compatibility |
| **Docker Engine** | `/v1.44/*` calls | Local daemon control requires HTTP/Unix socket |
| **OCI Registries** | Docker Hub, GHCR, Harbor API calls | External registries are HTTP-only |
| **PowerDNS** | `/api/v1/servers/*` | DNS backend is HTTP-only |
| **Qdrant** | Collections/points/search API | Vector DB is HTTP-only |
| **Cashu** | `/v1/info`, `/v1/mint/quote/*` | Payment protocol is HTTP-only |
| **OSV** | `POST /v1/query` | Vulnerability API is HTTP-only |
| **LLM Providers** | `/v1/chat/completions`, `/v1/messages` | Provider APIs are HTTP-only |
| **Blossom** | Blob upload/download/list | HTTP protocol with NIP-98 Nostr auth |

#### 2. TRANSITIONAL REST (Has Nostr Analog - Should Migrate)

| REST Endpoint | Nostr Analog | Status |
|---------------|--------------|--------|
| `POST /services` | `ServiceCreate` 5964 → result 7963 | Has analog |
| `PUT /services/{id}` | `ServiceUpdate` 5981 | Has analog |
| `DELETE /services/{id}` | `ServiceDelete` 5982 | Has analog |
| `POST /environments` | `EnvironmentCreate` 5965 → result 7964 | Has analog |
| `PUT/DELETE /environments/{id}` | `EnvironmentUpdate/Delete` 5983/5984 | Has analog |
| `POST /deployments/intents` | `DeployRequest` 5961 → status 6961 → result 7961 | Has analog |
| `POST /intents/{id}/approve` | `DeploymentApproval` 5966 | Has analog |
| `POST /rollback` | `RollbackRequest` 5962 | Has analog |
| `POST /observations` | `ObservationSubmit` 5967 | Has analog |
| `POST /artifacts` | `ArtifactRegister` 5985 | Has analog |
| `POST/PUT/DELETE /policies` | `PolicyCreate/Update/Delete` 5986-5988 | Has analog |
| `POST /tools/{id}/approve` | `ToolApprovalRequest` 5977 | Has analog |
| `POST /adoption/scan` | `AdoptionScanRequest` 5978 | Has analog |
| `POST /adoption/import` | `AdoptionImportRequest` 5979 | Has analog |
| `POST /llm/routes` | `LLMRouteCreate` 5971 | Has analog |
| Runtime actions (deploy/restart/stop) | `ServiceAction` 5963 | Has analog |

#### 3. UI/BROWSER COMPATIBILITY REST (Acceptable - Reads Projections)

All `GET` endpoints that read from Nostr-projected state are acceptable for browser compatibility:
- Service/environment/artifact/build listings
- Deployment intent/run state
- LLM/ML registries and state
- Worker listings
- Policy listings
- SBOM/signature data
- Payment/cost summaries
- Notification logs
- Continuity status/topology

These should remain as **read-only facades** over Nostr projections.

#### 4. GAPS - Missing Nostr Analogs

| REST Surface | Current State | Recommendation |
|--------------|---------------|----------------|
| Tenant/Org CRUD | No explicit kind | Define org command/read-model kinds |
| Member/Invite lifecycle | No explicit kind | Define encrypted invite events |
| Notification channel config | No explicit kind | Define config read model |
| Tool denylist | No explicit kind | Use policy command kinds |
| Secret lifecycle | Partial (5980 exists) | Expand NIP-44 encrypted requests |
| Build status updates | HiveCI path only | Formalize non-HiveCI path |

### Internal vs External Classification

| Category | Boundary | Examples |
|----------|----------|----------|
| **Internal (Browser→Bahia)** | Browser/UI calling Bahia API | All `/api/v1/*` GET/POST endpoints |
| **Internal (Bahia→Daemon)** | Bahia calling local services | Docker Engine API, Qdrant |
| **External (Bahia→Service)** | Bahia calling external APIs | OCI registries, Harbor, OSV, LLM providers, Cashu mints, PowerDNS |
| **External (Protocol)** | HTTP protocol with Nostr auth | Blossom (NIP-98) |

## Recommendations

### Immediate Actions
1. **Document REST as compatibility layer** - Make it explicit that mutating REST endpoints are compatibility ingress, not canonical
2. **Return Nostr correlation metadata** - All REST mutations should return request event ID, requester pubkey, request/result kinds (ML handler is the model)
3. **Gate transitional REST** - Add feature flags to disable REST mutations when Nostr-first clients are ready

### Medium-Term Actions
1. **Define missing kinds** - Tenant/org, notification config, tool denylist
2. **Expand NIP-44 secrets** - Replace REST secret endpoints with encrypted Nostr commands
3. **Audit REST write paths** - Ensure all write REST endpoints publish Nostr commands internally

### Keep Permanently
1. Health/ready/metrics endpoints
2. OCI Distribution compatibility (`/v2/*`)
3. MCP JSON-RPC transport
4. All outbound HTTP to non-Nostr services (Docker, registries, Qdrant, Cashu, OSV, LLM providers)
5. Blossom HTTP with NIP-98 auth
6. Browser-facing GET endpoints as projection facades

## Preventive Measures

1. **New feature checklist**: Any new REST endpoint must document whether a Nostr analog exists or should be created
2. **Architecture review**: REST mutations without Nostr command publication should require explicit justification
3. **Client migration path**: Document how REST-first clients can transition to Nostr-first
4. **Metrics**: Track REST vs Nostr command volume to measure adoption
