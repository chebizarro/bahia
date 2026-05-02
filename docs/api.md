# Bahia API Reference

Base URL: `http://localhost:8080`

All API endpoints (except health checks) are prefixed with `/api/v1`.

## Authentication

Local development can run with `auth.enabled=false`. Production deployments should enable Bahia API auth with JWT and/or NIP-98. Adoption/import and direct runtime action routes are privileged operator routes: they are mounted only when their feature flag is enabled and the authenticated principal is allowlisted by subject, pubkey, or email.

## Health

## Health

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/ready` | Readiness check |

## Services

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/services` | Create a service |
| GET | `/api/v1/services` | List all services |
| GET | `/api/v1/services/{id}` | Get a service |
| PUT | `/api/v1/services/{id}` | Update a service |
| DELETE | `/api/v1/services/{id}` | Delete a service |

## Environments

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/environments` | Create an environment |
| GET | `/api/v1/environments` | List all environments |
| GET | `/api/v1/environments/{id}` | Get an environment |
| PUT | `/api/v1/environments/{id}` | Update an environment |
| DELETE | `/api/v1/environments/{id}` | Delete an environment |

## Builds

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/builds` | Register a build |
| GET | `/api/v1/builds/{id}` | Get a build |
| PATCH | `/api/v1/builds/{id}/status` | Update build status |
| GET | `/api/v1/services/{serviceId}/builds` | List builds by service |

## Artifacts

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/artifacts` | Register an artifact |
| GET | `/api/v1/artifacts/{id}` | Get an artifact |
| GET | `/api/v1/services/{serviceId}/artifacts` | List artifacts by service |

## Deployment Intents

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/deployments/intents` | Create a deployment intent |
| GET | `/api/v1/deployments/intents/{id}` | Get a deployment intent |
| POST | `/api/v1/deployments/intents/{id}/approve` | Approve an intent |
| POST | `/api/v1/deployments/intents/{id}/reject` | Reject an intent |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/intents` | List intents |

## Deployment Runs

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/deployments/runs` | Create a deployment run |
| GET | `/api/v1/deployments/runs/{id}` | Get a deployment run |
| POST | `/api/v1/deployments/runs/{id}/complete` | Complete a run |
| GET | `/api/v1/deployments/intents/{intentId}/runs` | List runs by intent |

## Rollback

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/rollback` | Create a rollback intent |

## State & Observations

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/state` | List all states |
| GET | `/api/v1/state/drifted` | List drifted states |
| GET | `/api/v1/environments/{envId}/state` | List states by environment |
| GET | `/api/v1/services/{serviceId}/environments/{envId}/state` | Get specific state |
| POST | `/api/v1/observations` | Record a runtime observation |

## Adoption / Import (operator only)

These routes are disabled unless `adoption.enabled=true`. They require auth and the adoption operator allowlist.

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/adoption/scan` | Dry-run discovery of configured Docker endpoint aliases |
| POST | `/api/v1/adoption/import` | Import selected or all discovered candidates |

Use `endpoint_ref` targets for production. `docker_host` targets are accepted only when raw-host compatibility mode is enabled. Scan/import responses include safe environment and label fields plus `redacted_environment_keys` and `redacted_label_keys`; redacted values are never returned.

## Direct Runtime Actions (operator only)

These routes are disabled unless `direct_runtime_actions.enabled=true`. They require auth and the direct-runtime operator allowlist. Actions are limited to adopted `direct_runtime` workloads.

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/services/{serviceId}/environments/{envId}/deploy` | Deploy the desired or explicit artifact directly through the runtime |
| POST | `/api/v1/services/{serviceId}/environments/{envId}/restart` | Restart an adopted runtime target |
| POST | `/api/v1/services/{serviceId}/environments/{envId}/stop` | Stop an adopted runtime target |

Operational limits are separate from the generic write limiter: scan 5/min/IP, import 10/min/IP, direct runtime actions 20/min/IP. Metrics are exported on `/metrics` when telemetry is enabled; if API auth is enabled, `/metrics` requires the same auth middleware.

## OCI Registry (Distribution API v2)

The OCI registry endpoints are mounted at `/v2` (outside `/api/v1`) and implement the [OCI Distribution Spec](https://github.com/opencontainers/distribution-spec).

### Authentication

| Method | Description |
|--------|-------------|
| NIP-98 | Nostr-signed HTTP auth for push operations |
| Basic Auth | Service account credentials |
| Anonymous | Pull from allowed CIDRs (internal network) |

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

### Usage

```bash
# Push with docker (requires auth)
docker tag myapp:latest registry.sharegap.net/cascadia/myapp:v1.0.0
docker push registry.sharegap.net/cascadia/myapp:v1.0.0

# Pull (anonymous from internal network)
docker pull registry.sharegap.net/cascadia/myapp:v1.0.0
```

## Response Format

All responses follow this format:

```json
{
  "data": { ... },
  "error": "",
  "message": ""
}
```

List responses include pagination:

```json
{
  "data": [ ... ],
  "total": 42,
  "limit": 50,
  "offset": 0
}
```
