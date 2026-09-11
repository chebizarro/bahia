# Docker Compose Setup for Bahia

This document describes the Docker Compose setup for running the complete Bahia stack locally.

## Services

The `docker-compose.yml` file defines four services. Compose interpolation requires `BAHIA_NOSTR_PRIVATE_KEY` to be set before startup.

### 1. PostgreSQL (`postgres`)
- **Image**: postgres:16-alpine
- **Port**: 5432
- **Database**: bahia
- **Credentials**: bahia/bahia
- **Health Check**: `pg_isready -U bahia`

### 2. Bahia API Server (`bahia`)
- **Build**: From root Dockerfile
- **Port**: 8080
- **Dependencies**: PostgreSQL (waits for healthy status)
- **Health Check**: `wget` on `/health` endpoint (liveness only, so a Signet/dependency outage does not restart the container; use `/ready` for traffic gating)
- **Config**: `config.compose.yaml` mounted at `/etc/bahia/config.yaml` (`dev_mode: true`, relay sidecar settings, ContextVM relay `ws://relay:3334/relay`, reconcile every 60s)
- **Docker socket**: `/var/run/docker.sock` is mounted so Bahia can observe and drive local Docker workloads
- **Environment**: `BAHIA_DB_*`, `BAHIA_SERVER_*`, `BAHIA_LOG_*`, `BAHIA_NOSTR_PRIVATE_KEY` (required), and optional `BAHIA_NOSTR_AUTHORIZED_PUBKEYS`. See `docker-compose.yml` for the full list.

### 3. Relay sidecar (`relay`)
- **Build**: From the root Dockerfile, with the entrypoint changed to `bahia-relay`
- **Port**: 3334
- **Storage**: Named `relaydata` volume at `/var/lib/bahia/relay-sidecar`
- **Health Check**: Nostr relay metadata request on `/relay`

### 4. Web Frontend (`web`)
- **Build**: From web/Dockerfile (SvelteKit → nginx)
- **Port**: 3000 (maps to nginx port 80)
- **Dependencies**: Bahia API and relay sidecar (waits for healthy status)
- **Proxy**: `/api` requests are proxied to `bahia:8080`; `/relay` WebSocket traffic is proxied to `relay:3334`
- **Build args**: `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` (default `ws://localhost:3334/relay`) and `PUBLIC_BAHIA_SERVICE_PUBKEYS` (default empty), plus version metadata

> **NOTE (2026-09-11):** `web/docker-entrypoint.d/40-bahia-bootstrap-env.sh` exits non-zero at container start unless `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS` are set as **runtime** environment variables. `docker-compose.yml` passes them only as build args, and the service pubkey defaults to empty. If the `web` container exits on startup, add both values under `web.environment` in a local override. `PUBLIC_BAHIA_SERVICE_PUBKEYS` should be the hex pubkey that matches `BAHIA_NOSTR_PRIVATE_KEY`. Tracked as `bahia-zdbam`; verify against the current compose file before relying on this note.

## Usage

### Start the stack
```bash
# Required by docker-compose.yml; use deployment secret management outside local development.
export BAHIA_NOSTR_PRIVATE_KEY=<64-hex-secret-key>
docker compose up --build
```

### Access the services
- **Web UI**: http://localhost:3000
- **API**: http://localhost:8080
- **API liveness**: http://localhost:8080/health
- **API readiness**: http://localhost:8080/ready
- **Relay sidecar**: ws://localhost:3334/relay
- **Postgres**: localhost:5432 (user: bahia, password: bahia, db: bahia)

### Stop the stack
```bash
docker compose down
```

### Clean up (remove volumes)
```bash
docker compose down -v
```

## Testing Override

For E2E testing, use the test override:

```bash
docker compose -f docker-compose.yml -f docker-compose.test.yml up --build
```

This provides:
- Debug logging for `bahia`
- Bahia API also published on port 8081
- Web UI also published on port 3001

Compose concatenates `ports` lists across files, so the default `8080` and `3000` mappings remain published alongside the override ports.
- Faster PostgreSQL startup (no fsync)
- Direct PostgreSQL port exposure for test connections

The relay sidecar remains on port 3334.

## Service Communication

Services communicate via Docker's internal network:
- Web → API: `http://bahia:8080/api/v1/...`
- Web → relay: `http://relay:3334/relay` (WebSocket proxy)
- Bahia → relay: `ws://relay:3334/relay`
- API → DB: `postgres:5432`

External access:
- Web UI: http://localhost:3000
- API: http://localhost:8080
- Relay sidecar: ws://localhost:3334/relay (also reachable through the web proxy at ws://localhost:3000/relay, which is the sidecar's configured `public_url`)

## Health Checks

Three services have health checks:
- **postgres**: Checks `pg_isready`
- **bahia**: Checks the liveness endpoint `/health` with `wget`
- **relay**: Requests `/relay` with `Accept: application/nostr+json`

The `web` service has no Compose-level health check. Its image defines a Docker `HEALTHCHECK` that runs `wget --spider` against nginx. Startup dependencies are:
1. PostgreSQL becomes healthy before Bahia starts.
2. Bahia and the relay become healthy before the web container starts.

## Troubleshooting

### Check service logs
```bash
docker compose logs -f bahia
docker compose logs -f relay
docker compose logs -f web
docker compose logs -f postgres
```

### Check service health
```bash
docker compose ps
```

### Rebuild a specific service
```bash
docker compose build web
docker compose up -d web
```

### Reset everything
```bash
docker compose down -v
docker compose up --build
```

## Architecture Notes

### Web Frontend Build
The web service uses a multi-stage Dockerfile:
1. **Build stage**: Uses node:20-alpine to build the SvelteKit app
2. **Runtime stage**: Uses nginx:alpine to serve static files and proxy API calls

### Nginx Proxy Configuration
- Static files served from `/usr/share/nginx/html`
- `/api/*` proxied to `http://bahia:8080`
- Nostr relay websocket (`/relay`) proxied to the relay sidecar with buffering disabled
- Gzip compression enabled for text assets

### API Client Configuration
The web frontend uses relative URLs (`/api/v1/...`) which nginx proxies to the bahia service.
See `web/src/lib/api/client.js` for the API client implementation.
