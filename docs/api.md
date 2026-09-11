# Bahia HTTP API Reference

Base URL: `http://localhost:8080`

Most API endpoints are mounted under `/api/v1`. Root-level exceptions include `/health`, `/ready`, `/metrics`, `/mcp`, and `/v2/*`.

Route tables in this document reflect `internal/api/router/router.go`. Many route groups are only mounted when their backing dependency is configured (for example ML, LLM, payments, SBOM, signatures, secrets, notifications, tool provisioning, Blossom, OCI, and SoulFactory).

## Important scope note

This document covers Bahia's **HTTP surfaces**.

It does **not** by itself describe the full product interaction model.

Current Bahia behavior is:
- shared browser state is primarily bootstrapped from ContextVM discovery (`11316`-`11320`), NIP-51 relay sets (`30002`), and canonical relay-backed observables (`30900`, `4903`, `30315`, `30078`)
- many control-plane writes are published as signed Nostr request events
- sensitive browser domains use encrypted Nostr request/result events
- REST remains a narrowed compatibility/query/log surface
- MCP at `/mcp` and `/api/v1/mcp` is a first-class tooling surface

For the full control-plane contract, see:
- `docs/control-planes.md`
- `docs/nostr-commands.md`
- `docs/event-spec.md`

## Authentication

Local development can run with `auth.enabled=false`.

When Bahia HTTP auth is enabled:
- protected Nostr event contracts accept **direct NIP-98** via `Authorization: Nostr <base64event>`
- `Authorization: Bearer ...` is unsupported and should be rejected with `401`
- signer-first adoption/import and direct runtime action events are privileged operator flows gated by their feature flags and allowlists

## Operating mode tiers and rate limits

The server runs in one of three modes, set by `mode` in the config file or `BAHIA_MODE`: `full` (tier 3, the default), `degraded` (tier 2), or `emergency` (tier 1). Bootstrap dependency checks can lower the active tier below the requested one. Routes above the active tier return `503` with `{"error":"route unavailable in current mode", "mode", "active_tier", "required_tier"}`.

- Tier 1: worker reads
- Tier 2: core registry/deployment/state reads, orgs, policies, payments, signatures, secrets reads, notifications, instance health, route canaries
- Tier 3: OCI `/v2`, MCP, ML, LLM, SBOM, config fabric, tool provisioning, Blossom, SoulFactory, repository CI lookup

`/api/v1` requests are rate-limited per client IP: 100 requests/minute for the read group and 30 requests/minute for the write group.

## Health

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Liveness snapshot (always `200` when the process is serving) |
| GET | `/ready` | Readiness snapshot; returns `503` when active-tier required checks fail |
| GET | `/metrics` | Prometheus text metrics (always mounted by the server; `telemetry.enabled` only controls OTLP export). Requires NIP-98 auth when HTTP auth is enabled |

Container health checks should use `/health`; traffic gates and operators should use `/ready`.

## Discovery and tooling bootstrap

### Nostr discovery

`ContextVM discovery events (11316-11320) + NIP-51 relay sets (30002)`

Returns discovery/capability metadata used by the browser and tooling, including:
- registries
- Nostr relay topology
- service pubkey
- core control-plane kind maps
- runtime / OCI / Blossom metadata
- feature flags such as `relay_read_models`, `direct_nostr_http_auth`, and `encrypted_nostr_requests`

The `control_plane` payload is a compatibility discovery subset. Production command families use ContextVM methods and canonical observables documented in `docs/control-planes.md`; old Bahia read-model kind numbers are migration inventory only.

### Native MCP

| Method | Path | Description |
|--------|------|-------------|
| POST | `/mcp` | Native MCP JSON-RPC endpoint |
| POST | `/api/v1/mcp` | Alternate native MCP JSON-RPC endpoint |

MCP tool responses for long-running writes return the same receipt fields as REST Nostr-backed writes: `request_event_id`, `request_kind`, `status_kind`, `result_kind`, `d_tag`, `idempotency_key`, `status`, `published_relays`, and `timeout_seconds`.

## Command receipts for long-running writes

Long-running HTTP writes that publish canonical Nostr command events return `202 Accepted` with `data` set to a `CommandReceipt` object:

```json
{
  "data": {
    "request_event_id": "<nostr-event-id>",
    "request_kind": 25910,
    "status_kind": 30315,
    "result_kind": 25910,
    "d_tag": "ml-inference-deploy:<key>",
    "idempotency_key": "ml-inference-deploy:<key>",
    "status": "submitted",
    "published_relays": 1,
    "timeout_seconds": 30,
    "message": "request published; subscribe to Nostr result/read-model events for completion"
  }
}
```

`timeout_seconds` is the publisher-provided publish-and-wait compatibility timeout when available; if the publisher omits it, REST compatibility responses default it to 30 seconds. A relay-unreachable failure returns an immediate HTTP error with a retry hint in the message because no relay accepted the request. A partial relay failure returns a receipt with `status="error"`, `published_relays > 0`, and an `error` field; clients must not fall back to a second business path after any relay has accepted the command.

Idempotency keys are represented as the Nostr `d` tag. Clients may provide `idempotency_key`; otherwise publishers generate one and signer-first CLI operator requests derive deterministic keys from the command kind, scoped tags, and payload. Consumers should subscribe for the receipt's status/result kinds using `request_event_id` and resource tags rather than polling REST for completion.

For canonical ContextVM-backed writes, `request_kind` is `25910`, `status_kind` is normally `30315`, and `result_kind` is the correlated ContextVM response kind `25910`; state/audit convergence uses `30900` and `4903` (plus domain-specific standard NIPs). Some receipt fields remain for compatibility, but clients must not substitute historical `69xx`/`79xx` kinds.

Compatibility note: the server no longer mounts REST mutations for services, environments, artifacts, deployment intents/approvals/rollback, policy writes/evaluation, or LLM route creation. Use ContextVM, the signer-first CLI, or MCP for those operations. A smaller set of compatibility writes remains mounted for deployment-run bookkeeping, ML commands, organizations, payments, SBOM ingestion, signature verification, secrets, notifications, tool denylisting, and config-fabric operations. Route availability can also depend on configured dependencies and the active mode tier.

## Core registry routes

### Services

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/services` | List services |
| GET | `/api/v1/services/{id}` | Get a service |

Service creation, update, and deletion use signer-first ContextVM methods; no service REST mutation routes are mounted.

### Environments

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/environments` | List environments |
| GET | `/api/v1/environments/{id}` | Get an environment |

Environment creation, detail reads for unit management, update, and deletion are available through signer-first ContextVM methods. The REST surface only mounts the two reads above.

### Builds

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/builds` | Register a build |
| GET | `/api/v1/builds/{id}` | Get a build |
| PATCH | `/api/v1/builds/{id}/status` | Update build status |
| GET | `/api/v1/services/{serviceId}/builds` | List builds by service |

### Artifacts

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/artifacts/{id}` | Get an artifact |
| GET | `/api/v1/services/{serviceId}/artifacts` | List artifacts by service |

Artifact registration and observed-artifact import use signer-first ContextVM methods; `POST /api/v1/artifacts` is not mounted.

## Deployment routes

### Deployment intents

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/deployments/intents/{id}` | Get deployment intent |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/intents` | List intents |

Create, approve, reject, and rollback operations use signer-first ContextVM methods; their former REST mutation routes are not mounted.

### Deployment runs

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/deployments/runs` | Create deployment run |
| GET | `/api/v1/deployments/runs/{id}` | Get deployment run |
| POST | `/api/v1/deployments/runs/{id}/complete` | Complete a deployment run |
| GET | `/api/v1/deployments/intents/{intentId}/runs` | List runs by intent |
| GET | `/api/v1/deployments/runs/{id}/logs` | Stored run logs |


## State and observations

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/state` | List all state |
| GET | `/api/v1/state/drifted` | List drifted state |
| GET | `/api/v1/environments/{envId}/state` | List state by environment |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/state` | Get state for one service/environment |
| GET | `/api/v1/services/{id}/environments/{envId}/logs?follow=true` | Live log stream (SSE); mounted when a runtime resolver is configured |

There is no REST route for recording runtime observations; observations are produced by Bahia's reconcilers and runtime observers.

### Managed instance health and maintenance

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/instance-health` | List managed instance health |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/health` | Get instance health |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/health/events` | List health events |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/health/recovery-attempts` | List recovery attempts |
| POST | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance` | Set maintenance mode for the instance |
| DELETE | `/api/v1/services/{serviceId}/environments/{envId}/managed-instances/{deploymentUnitId}/maintenance` | Clear maintenance |

### Route canaries

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/route-canaries` | List managed route canary results |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/routes/{hostname}/canary` | Get canary state for one route |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/routes/{hostname}/canary/events` | List canary events for one route |

## Repository / worker / policy / payment routes

### Repository CI lookup

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/repositories/ci/lookup` | Lookup CI/provider metadata for repositories |

### Workers

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/workers` | List workers |
| GET | `/api/v1/workers/{pubkey}` | Get worker |
| GET | `/api/v1/workers/{pubkey}/pricing` | Get worker pricing |

### Policies

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/policies` | List policies |
| GET | `/api/v1/policies/{id}` | Get policy |

Policy create, update, delete, and evaluation REST routes are not mounted. Use the corresponding signer-first ContextVM methods.

### Payments

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/payments/estimate` | Estimate run cost |
| GET | `/api/v1/deployments/runs/{id}/cost` | Get run cost |
| GET | `/api/v1/payments/history` | Get payment history |

### Config fabric

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/config-fabric/drift` | List desired/applied config drift |
| POST | `/api/v1/config-fabric/events` | Publish desired config state |
| POST | `/api/v1/config-fabric/rollback` | Roll back config state |

## SBOM / signatures / secrets / notifications

### SBOM

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/artifacts/{id}/sbom` | Get SBOM |
| GET | `/api/v1/artifacts/{id}/sbom/packages` | Get SBOM packages |
| GET | `/api/v1/sbom/search` | Search SBOM packages |
| POST | `/api/v1/artifacts/{id}/sbom` | Ingest SBOM |

### Signatures

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/artifacts/{id}/signatures` | List signatures |
| GET | `/api/v1/artifacts/{id}/signatures/verified` | List verified signatures |
| GET | `/api/v1/artifacts/{id}/signatures/check` | Check whether verified signatures exist |
| GET | `/api/v1/signatures/{id}` | Get signature record |
| POST | `/api/v1/artifacts/{id}/signatures/verify` | Verify signatures |

### Secrets

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/services/{id}/secrets` | List secrets for service |
| POST | `/api/v1/services/{id}/secrets` | Create secret |
| PUT | `/api/v1/services/{id}/secrets/{secretId}` | Update secret |
| DELETE | `/api/v1/services/{id}/secrets/{secretId}` | Delete secret |

### Notifications

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/notifications/channels` | List channels |
| GET | `/api/v1/notifications/channels/{id}` | Get channel |
| POST | `/api/v1/notifications/channels` | Create channel |
| PUT | `/api/v1/notifications/channels/{id}` | Update channel |
| DELETE | `/api/v1/notifications/channels/{id}` | Delete channel |
| POST | `/api/v1/notifications/channels/{id}/test` | Send test notification |
| GET | `/api/v1/notifications/log` | List notification logs |

### Tool provisioning

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/tools/pending` | List pending tool provisioning intents |
| GET | `/api/v1/tools/{id}` | Get tool provisioning intent |
| GET | `/api/v1/tools/denylist` | List denylisted packages |
| POST | `/api/v1/tools/denylist` | Add a denylist entry |
| DELETE | `/api/v1/tools/denylist/{package}/{manager}` | Remove a denylist entry |
| GET | `/api/v1/services/{id}/tools` | Get a service's tool profile |

## Organization routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/orgs` | List organizations visible to caller |
| GET | `/api/v1/orgs/{id}` | Get organization |
| GET | `/api/v1/orgs/{id}/members` | List members |
| GET | `/api/v1/orgs/{id}/invites` | List invites |
| GET | `/api/v1/me/invites` | List caller invites |
| POST | `/api/v1/orgs` | Create organization |
| PUT | `/api/v1/orgs/{id}` | Update organization |
| DELETE | `/api/v1/orgs/{id}` | Delete organization |
| POST | `/api/v1/orgs/{id}/members` | Add member |
| PUT | `/api/v1/orgs/{id}/members/{pubkey}` | Update member role |
| DELETE | `/api/v1/orgs/{id}/members/{pubkey}` | Remove member |
| POST | `/api/v1/orgs/{id}/invites` | Create invite |
| DELETE | `/api/v1/orgs/{id}/invites/{inviteId}` | Revoke invite |

## ML routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/ml/models` | List models |
| GET | `/api/v1/ml/models/{id}` | Get model |
| GET | `/api/v1/ml/models/{modelId}/versions` | List model versions |
| GET | `/api/v1/ml/model-versions/{id}` | Get model version |
| GET | `/api/v1/ml/endpoints` | List inference endpoints |
| GET | `/api/v1/ml/endpoints/{id}` | Get inference endpoint |
| GET | `/api/v1/ml/state` | List ML state |
| GET | `/api/v1/ml/endpoints/{endpointId}/environments/{envId}/state` | Get endpoint state |
| GET | `/api/v1/ml/artifacts/{artifactId}/provenance` | Get artifact provenance |
| POST | `/api/v1/ml/imports` | Publish model import command; returns `202` receipt |
| POST | `/api/v1/ml/recipes/runs` | Publish recipe run command; returns `202` receipt |
| POST | `/api/v1/ml/deployments` | Publish ML deploy command; returns `202` receipt |
| POST | `/api/v1/ml/rollback` | Publish ML rollback command; returns `202` receipt |

## LLM routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/llm/routes` | List LLM routes |
| GET | `/api/v1/llm/routes/{id}` | Get route |
| PUT | `/api/v1/llm/routes/{id}` | Update route (compatibility write) |
| GET | `/api/v1/llm/routes/{routeId}/releases` | List releases |
| GET | `/api/v1/llm/releases/{id}` | Get release |
| GET | `/api/v1/llm/intents/{id}` | Get intent |
| GET | `/api/v1/llm/routes/{routeId}/environments/{envId}/intents` | List intents |
| GET | `/api/v1/llm/runs/{id}` | Get run |
| GET | `/api/v1/llm/intents/{intentId}/runs` | List runs |
| GET | `/api/v1/llm/state` | List LLM state |
| GET | `/api/v1/llm/state/drifted` | List drifted LLM state |
| GET | `/api/v1/llm/environments/{envId}/state` | List LLM state by environment |
| GET | `/api/v1/llm/routes/{routeId}/environments/{envId}/state` | Get LLM route state |

LLM route creation, release creation, deployment intent/approval/rollback, host, and observation REST mutations are not mounted. `PUT /api/v1/llm/routes/{id}` remains as a compatibility write. Prefer signer-first ContextVM LLM methods and subscribe to canonical observables for completion.

## SoulFactory and Blossom

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/soulfactory/runtimes` | Non-secret agent runtime policy (mounted only when `soul_factory.enabled`) |
| POST | `/api/v1/blossom/list` | List blobs |
| GET | `/api/v1/blossom/servers` | List configured Blossom servers |
| GET | `/api/v1/blossom/health` | Blossom health check |
| GET | `/api/v1/blossom/stats` | Blossom stats |
| GET | `/api/v1/blossom/blob/{hash}` | Download a blob (content-addressed; no auth required) |

## Adoption / import (operator only)

Adoption scan/import REST endpoints are removed. Operators should call ContextVM methods `adoption/scan` and `adoption/import` over kind `25910` and subscribe for correlated ContextVM responses plus canonical observables (`30900`, `4903`, `30315`).

Use `endpoint_ref` targets for production. `docker_host` targets remain a signer-first compatibility policy decision, not a public REST surface.

## Direct runtime actions (operator only)

Direct runtime deploy/restart/stop REST endpoints are removed. Operators should call the ContextVM method `service/action` (with `action` set to `deploy`, `restart`, or `stop`) over kind `25910` and subscribe for correlated ContextVM responses plus canonical observables (`30900`, `4903`, `30315`).

Direct runtime actions remain limited to adopted `direct_runtime` workloads and authorized operator pubkeys. Deploy responses may include optional `desired_hash` when persisted desired-state metadata is available; restart/stop responses do not create desired-state snapshots.

## Sensitive browser flows that may not use REST even when REST routes exist

The following domains are important examples where the first-party web app may prefer encrypted Nostr request/result flows over REST, despite compatible HTTP routes existing:
- notifications
- payments history
- organizations / invites / membership operations
- service secrets
- deployment run logs and artifact signature verification

Use `docs/control-planes.md` as the source of truth for those transport choices.

## OCI registry (Distribution API v2)

The OCI registry endpoints are mounted at `/v2` and implement the OCI Distribution Spec.

### Authentication

| Method | Description |
|--------|-------------|
| NIP-98 | Nostr-signed HTTP auth for push operations |
| Basic Auth | Service account credentials |
| Anonymous | Pull from allowed CIDRs |

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/v2/` | API version check |
| GET | `/v2/{name}/manifests/{reference}` | Pull manifest by tag or digest |
| HEAD | `/v2/{name}/manifests/{reference}` | Check manifest existence |
| PUT | `/v2/{name}/manifests/{reference}` | Push manifest |
| GET | `/v2/{name}/blobs/{digest}` | Pull blob |
| HEAD | `/v2/{name}/blobs/{digest}` | Check blob existence |
| POST | `/v2/{name}/blobs/uploads/` | Start blob upload |
| PATCH | `/v2/{name}/blobs/uploads/{uuid}` | Upload blob chunk |
| PUT | `/v2/{name}/blobs/uploads/{uuid}?digest=...` | Complete blob upload |
| GET | `/v2/{name}/tags/list` | List tags |
| GET | `/v2/{name}/referrers/{digest}` | List referrers |

## Response format

Most REST responses use a Bahia envelope:

```json
{
  "data": { ... },
  "error": "",
  "message": ""
}
```

List responses may include pagination metadata:

```json
{
  "data": [ ... ],
  "total": 42,
  "limit": 50,
  "offset": 0
}
```
