# Bahia API Reference

Base URL: `http://localhost:8080`

All API endpoints (except health checks) are prefixed with `/api/v1`.

## Authentication

Currently no authentication required for local development. Production deployments should use a reverse proxy with authentication.

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
