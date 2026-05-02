# Docker Compose Setup for Bahia

This document describes the Docker Compose setup for running the complete Bahia stack locally.

## Services

The docker-compose.yml file defines three services:

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
- **Health Check**: `wget` on `/health` endpoint
- **Environment**: See docker-compose.yml for full config

### 3. Web Frontend (`web`)
- **Build**: From web/Dockerfile (SvelteKit → nginx)
- **Port**: 3000 (maps to nginx port 80)
- **Dependencies**: Bahia API (waits for healthy status)
- **Proxy**: `/api` requests are proxied to `bahia:8080`

## Usage

### Start the stack
```bash
docker compose up --build
```

### Access the services
- **Web UI**: http://localhost:3000
- **API**: http://localhost:8080
- **API Health**: http://localhost:8080/health
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
- Debug logging
- Web UI on port 3001 (avoiding conflicts with dev server)
- Faster PostgreSQL startup (no fsync)
- Direct postgres port exposure for test connections

## Service Communication

Services communicate via Docker's internal network:
- Web → API: `http://bahia:8080/api/v1/...`
- API → DB: `postgres:5432`

External access:
- Web UI: http://localhost:3000
- API: http://localhost:8080

## Health Checks

All services have health checks:
- **postgres**: Checks `pg_isready`
- **bahia**: Checks `/health` endpoint with wget
- **web**: Checks nginx is responding (via wget)

Services start in dependency order and wait for health checks to pass:
1. PostgreSQL starts and becomes healthy
2. Bahia API starts (waits for postgres) and becomes healthy
3. Web frontend starts (waits for bahia)

## Troubleshooting

### Check service logs
```bash
docker compose logs -f bahia
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
