# REST API Deprecation Plan

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
| `POST /deployments/intents` | `DeployRequest` 5961 | `handleDeployRequest` | `router.go:410` |
| `POST /deployments/intents/{id}/approve` | `DeploymentApproval` 5966 | `handleDeploymentApproval` | `router.go:411` |
| `POST /deployments/intents/{id}/reject` | `DeploymentApproval` 5966 | `handleDeploymentApproval` | `router.go:412` |
| `POST /rollback` | `RollbackRequest` 5962 | `handleRollbackRequest` | `router.go:438` |
| `POST /observations` | `ObservationSubmit` 5967 | `handleObservationSubmit` | `router.go:441` |

### Artifacts

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /artifacts` | `ArtifactRegister` 5985 | `handleArtifactRegister` | `router.go:407` |

### Policies

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /policies` | `PolicyCreate` 5986 | `handlePolicyCreate` | `router.go:464` |
| `PUT /policies/{id}` | `PolicyUpdate` 5987 | `handlePolicyUpdate` | `router.go:465` |
| `DELETE /policies/{id}` | `PolicyDelete` 5988 | `handlePolicyDelete` | `router.go:466` |
| `POST /policies/evaluate` | `PolicyEvaluate` 5989 | `handlePolicyEvaluate` | `router.go:467` |

### Tools

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /tools/{id}/approve` | `ToolApprovalResponse` 7977 | `handleToolApprovalResponse` | `router.go:493` |
| `POST /tools/{id}/reject` | `ToolApprovalResponse` 7977 | `handleToolApprovalResponse` | `router.go:494` |

### Adoption

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /adoption/scan` | `AdoptionScanRequest` 5978 | `handleAdoptionScanRequest` | `router.go:529` |
| `POST /adoption/import` | `AdoptionImportRequest` 5979 | `handleAdoptionImportRequest` | `router.go:533` |

### LLM

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /llm/routes` | `LLMRouteCreate` 5971 | `handleLLMRouteCreate` | `router.go:428` |
| `POST /llm/routes/{routeId}/releases` | `LLMReleaseRegister` 5972 | `handleLLMReleaseRegister` | `router.go:430` |

### Direct Runtime (Feature-Flagged)

| REST Endpoint | Nostr Kind | Reactor Handler | File |
|---------------|------------|-----------------|------|
| `POST /services/{serviceId}/environments/{envId}/deploy` | `DeployRequest` 5961 | `handleDeployRequest` | `router.go:513` |
| `POST /services/{serviceId}/environments/{envId}/restart` | `ServiceAction` 5963 | `handleServiceAction` | `router.go:514` |
| `POST /services/{serviceId}/environments/{envId}/stop` | `ServiceAction` 5963 | `handleServiceAction` | `router.go:515` |

**Total: 24 REST endpoints to delete**

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
