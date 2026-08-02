# REST API Deprecation Plan

> **Status (2026-08-01): Completed migration record.** The 25 mutation endpoints
> marked for deletion below are no longer mounted. Endpoint inventories, handler
> names, line numbers, and future-consideration sections are point-in-time
> planning context; verify the current surface in `internal/api/router/router.go`.

> **Bahia is Nostr-first.** REST APIs should only exist when absolutely necessary to interoperate with non-Nostr services. This document tracks which REST endpoints have Nostr analogs and should be removed.

## Status Legend

| Status | Meaning |
|--------|---------|
| 🔴 **DELETE** | Reactor already handles this via Nostr. Delete the REST endpoint. |
| 🟡 **GRAY** | REST publishes Nostr command (doing it right), but could be deleted if clients publish directly. |
| 🟢 **KEEP** | No Nostr alternative exists, or HTTP is required by external protocol. |

---

## 🔴 DELETE — Reactor Already Handles These

These REST endpoints duplicate functionality that the Nostr reactor (`internal/controlplane/reactor.go`) already handles. Clients should sign and publish Nostr events directly to relays instead.

### Services

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /services` | `ServiceCreate` 5964 | `handleServiceCreate` | `router.go:395` |
| `PUT /services/{id}` | `ServiceUpdate` 5981 | `handleServiceUpdate` | `router.go:396` |
| `DELETE /services/{id}` | `ServiceDelete` 5982 | `handleServiceDelete` | `router.go:397` |

### Environments

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /environments` | `EnvironmentCreate` 5965 | `handleEnvironmentCreate` | `router.go:399` |
| `PUT /environments/{id}` | `EnvironmentUpdate` 5983 | `handleEnvironmentUpdate` | `router.go:400` |
| `DELETE /environments/{id}` | `EnvironmentDelete` 5984 | `handleEnvironmentDelete` | `router.go:401` |

### Deployments

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /deployments/intents` (removed) | `DeployRequest` 5961 | `handleDeployRequest` | `internal/controlplane/reactor.go` |
| `POST /deployments/intents/{id}/approve` (removed) | `DeploymentApproval` 5966 | `handleDeploymentApproval` | `internal/controlplane/reactor.go` |
| `POST /deployments/intents/{id}/reject` (removed) | `DeploymentApproval` 5966 | `handleDeploymentApproval` | `internal/controlplane/reactor.go` |
| `POST /rollback` (removed) | `RollbackRequest` 5962 | `handleRollbackRequest` | `internal/controlplane/reactor.go` |
| `POST /observations` (removed) | `ObservationSubmit` 5967 | `handleObservationSubmit` | `internal/controlplane/reactor.go` |

### Artifacts

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /artifacts` (removed) | `ArtifactRegister` 5985 | `handleArtifactRegister` | `internal/controlplane/reactor.go` |

### Policies

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /policies` (removed) | `PolicyCreate` 5986 | `handlePolicyCreate` | `internal/controlplane/reactor.go` |
| `PUT /policies/{id}` (removed) | `PolicyUpdate` 5987 | `handlePolicyUpdate` | `internal/controlplane/reactor.go` |
| `DELETE /policies/{id}` (removed) | `PolicyDelete` 5988 | `handlePolicyDelete` | `internal/controlplane/reactor.go` |
| `POST /policies/evaluate` (removed) | `PolicyEvaluate` 5989 | `handlePolicyEvaluate` | `internal/controlplane/reactor.go` |

### Tools

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /tools/{id}/approve` (removed) | `ToolApprovalResponse` 7977 | `handleToolApprovalResponse` | `internal/controlplane/reactor.go` |
| `POST /tools/{id}/reject` (removed) | `ToolApprovalResponse` 7977 | `handleToolApprovalResponse` | `internal/controlplane/reactor.go` |

### Adoption

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /adoption/scan` (removed) | `AdoptionScanRequest` 5978 | `handleAdoptionScanRequest` | `internal/controlplane/reactor.go` |
| `POST /adoption/import` (removed) | `AdoptionImportRequest` 5979 | `handleAdoptionImportRequest` | `internal/controlplane/reactor.go` |

### LLM

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /llm/routes` (removed) | `LLMRouteCreate` 5971 | `handleLLMRouteCreate` | `internal/controlplane/reactor.go` |
| `POST /llm/routes/{routeId}/releases` (removed) | `LLMReleaseRegister` 5972 | `handleLLMReleaseRegister` | `internal/controlplane/reactor.go` |

### Direct Runtime (Feature-Flagged)

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /services/{serviceId}/environments/{envId}/deploy` (removed) | `DeployRequest` 5961 | `handleDeployRequest` | `internal/controlplane/reactor.go` |
| `POST /services/{serviceId}/environments/{envId}/restart` (removed) | `ServiceAction` 5963 | `handleServiceAction` | `internal/controlplane/reactor.go` |
| `POST /services/{serviceId}/environments/{envId}/stop` (removed) | `ServiceAction` 5963 | `handleServiceAction` | `internal/controlplane/reactor.go` |

**Total: 25 REST endpoints to delete**

---

## 🟡 GRAY — Nostr-Publishing REST (Could Delete)

These REST endpoints already publish Nostr commands internally and return correlation metadata. They're "doing it right" but are still REST ingress. Could be deleted if MCP/tooling clients publish Nostr events directly.

| REST Endpoint | Publishes Kind | Handler | File |
|---------------|----------------|---------|------|
| `POST /ml/imports` | `MLModelImportRequest` 38394 | `mlH.ImportModel` | `router.go:420` |
| `POST /ml/recipes/runs` | `MLRecipeRunRequest` 38390 | `mlH.RunRecipe` | `router.go:421` |
| `POST /ml/deployments` | `MLInferenceDeployRequest` 38391 | `mlH.Deploy` | `router.go:422` |
| `POST /ml/rollback` | `MLInferenceRollbackRequest` 38393 | `mlH.Rollback` | `router.go:423` |

These handlers return `202 Accepted` with Nostr correlation metadata:
- `request_event_id`
- `request_pubkey`
- `request_kind`, `result_kind`, `read_model_kinds`
- `published_relays`

**This is the pattern other REST handlers should follow IF they must exist.**

---

## 🟢 KEEP — No Nostr Alternative

### Essential HTTP Boundaries

| Endpoint | Reason |
|----------|--------|
| `GET /health` | Infrastructure liveness probes require HTTP |
| `GET /ready` | Infrastructure readiness probes require HTTP |
| `GET /metrics` | Prometheus scraping requires HTTP |
| `/v2/*` | OCI Distribution protocol - clients require HTTP |
| `POST /mcp` | MCP JSON-RPC protocol compatibility |

### External Service Adapters (Outbound HTTP)

These are outbound HTTP calls to non-Nostr services, not REST endpoints:

| Adapter | Target | Reason |
|---------|--------|--------|
| Docker Engine | `/v1.44/*` | Container runtime is HTTP/Unix socket |
| OCI Registries | Docker Hub, GHCR, Harbor | Registry protocol is HTTP |
| PowerDNS | `/api/v1/servers/*` | DNS backend is HTTP |
| Qdrant | Collections/search API | Vector DB is HTTP |
| Cashu | `/v1/info`, `/v1/mint/*` | Payment protocol is HTTP |
| OSV | `POST /v1/query` | Vulnerability API is HTTP |
| LLM Providers | `/v1/chat/completions` | Provider APIs are HTTP |
| Blossom | Blob upload/download | HTTP protocol (uses NIP-98 Nostr auth) |

### No Reactor Handler Yet

| REST Endpoint | Status | Action Needed |
|---------------|--------|---------------|
| `POST /builds` | No Nostr kind | Define `BuildRegister` command kind |
| `PATCH /builds/{id}/status` | No Nostr kind | Define `BuildStatusUpdate` command kind |
| `POST /deployments/runs` | Internal | May not need external command |
| `POST /deployments/runs/{id}/complete` | Internal | May not need external command |
| `PUT /llm/routes/{id}` | No Nostr kind | Define `LLMRouteUpdate` command kind |

### No Nostr Kind Defined

| REST Endpoint | Status | Action Needed |
|---------------|--------|---------------|
| Tenant/Org CRUD | No kinds | Define org command/read-model kinds |
| Member/Invite lifecycle | No kinds | Define encrypted invite events |
| Notification channel CRUD | No kinds | Define config read model |
| Tool denylist CRUD | No kinds | Use policy command kinds |
| Secrets CRUD | Partial (5980) | Expand NIP-44 encrypted requests |
| `POST /artifacts/{id}/sbom` | Large docs | May need special handling |
| `POST /artifacts/{id}/signatures/verify` | No kind | Define signature verify command |
| `POST /payments/estimate` | Query only | May not need command kind |

### Browser/UI Read Endpoints

All `GET` endpoints that read projected state are acceptable for browser compatibility:

- Service/environment/artifact/build listings
- Deployment intent/run/state reads
- LLM/ML registry and state reads
- Worker/tool/policy listings
- SBOM/signature reads
- Payment/notification logs
- Continuity status/topology

**Future consideration**: These could migrate to Nostr relay queries (`REQ` filters on read-model kinds) if the web UI used `nostr-tools` directly.

---

## Implementation Progress

### Work Item 1: Services + Environments (6 endpoints)
- [x] Remove POST /services from router.go
- [x] Remove PUT /services/{id} from router.go
- [x] Remove DELETE /services/{id} from router.go
- [x] Remove POST /environments from router.go
- [x] Remove PUT /environments/{id} from router.go
- [x] Remove DELETE /environments/{id} from router.go
- [x] Delete/comment handler methods in services.go, environments.go
- [x] Update tests

### Work Item 2: Deployments + Observations + Artifacts (6 endpoints)
- [x] Remove POST /deployments/intents from router.go
- [x] Remove POST /deployments/intents/{id}/approve from router.go
- [x] Remove POST /deployments/intents/{id}/reject from router.go
- [x] Remove POST /rollback from router.go
- [x] Remove POST /observations from router.go
- [x] Remove POST /artifacts from router.go
- [x] Delete/comment handler methods in deployments.go, state.go, artifacts.go
- [x] Update tests

### Work Item 3: Policies + Tools (6 endpoints)
- [x] Remove POST /policies from router.go
- [x] Remove PUT /policies/{id} from router.go
- [x] Remove DELETE /policies/{id} from router.go
- [x] Remove POST /policies/evaluate from router.go
- [x] Remove POST /tools/{id}/approve from router.go
- [x] Remove POST /tools/{id}/reject from router.go
- [x] Delete/comment handler methods in policies.go, tools.go
- [x] Update tests

### Work Item 4: LLM + Adoption + Direct Runtime (7 endpoints)
- [x] Remove POST /llm/routes from router.go
- [x] Remove POST /llm/routes/{routeId}/releases from router.go
- [x] Remove POST /adoption/scan from router.go
- [x] Remove POST /adoption/import from router.go
- [x] Remove POST /services/{serviceId}/environments/{envId}/deploy from router.go
- [x] Remove POST /services/{serviceId}/environments/{envId}/restart from router.go
- [x] Remove POST /services/{serviceId}/environments/{envId}/stop from router.go
- [x] Delete/comment handler methods in llm.go, adoption.go, service_actions.go
- [x] Update tests

---

## Migration Path

### For Clients

1. **Stop calling REST mutation endpoints** listed in 🔴 DELETE
2. **Sign Nostr events** with the appropriate command kind
3. **Publish to relays** that Bahia subscribes to
4. **Subscribe for results** on result/read-model kinds

### Example: Creating a Service

**Before (REST):**
```bash
curl -X POST /api/v1/services \
  -H "Authorization: Nostr $NIP98_TOKEN" \
  -d '{"name": "my-service", ...}'
```

**After (Nostr):**
```javascript
const event = {
  kind: 5964, // ServiceCreate
  content: JSON.stringify({ name: "my-service", ... }),
  tags: [["d", "idempotency-key"]],
  created_at: Math.floor(Date.now() / 1000),
  pubkey: myPubkey,
};
event.id = getEventHash(event);
event.sig = signEvent(event, myPrivkey);
await relay.publish(event);

// Subscribe for result
relay.subscribe([{ kinds: [7963], "#e": [event.id] }]); // ServiceCreateResult
```

### For Bahia Development

1. **Do not add new REST mutation endpoints** for operations that have Nostr command kinds
2. **If REST must exist temporarily**, follow the ML handler pattern (publish Nostr command, return correlation metadata)
3. **Track progress** in this document

---

## References

- Investigation report: `docs/investigations/rest-api-audit-2026-06-01.md`
- Nostr kinds: `internal/kinds/kinds.go`, `internal/kinds/policy.go`
- Reactor handlers: `internal/controlplane/reactor.go`
- Router: `internal/api/router/router.go`
