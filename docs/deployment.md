# Deployment Guide

This guide covers how to run Bahia in different environments.

## Prerequisites

- Go 1.24+
- PostgreSQL 16+
- Docker, Podman, or Kubernetes (for runtime observation)

## Quick Start with Docker Compose

```bash
# Clone and start
cd bahia
docker compose up --build

# The API is available at http://localhost:8080
curl http://localhost:8080/health
```

## Manual Setup

### 1. Database

```bash
# Create the database
createdb bahia

# Migrations run automatically on server start
```

### 2. Configuration

Copy and edit the config file:

```bash
cp config.yaml config.local.yaml
# Edit config.local.yaml with your settings
```

Or use environment variables:

```bash
export BAHIA_DB_HOST=localhost
export BAHIA_DB_PORT=5432
export BAHIA_DB_USER=bahia
export BAHIA_DB_PASSWORD=bahia
export BAHIA_DB_NAME=bahia
```

### 3. Build and Run

```bash
# Build
make build

# Run server
./bin/bahia-server -config config.local.yaml

# Or with make
make run-dev
```

### 4. CLI

```bash
# Build CLI
make build-cli

# List services
./bin/bahia services list

# Deploy
./bin/bahia deploy \
  --service <service-id> \
  --environment <env-id> \
  --artifact <artifact-id> \
  --requested-by "operator@example.com"
```

## Production Deployment

### Docker

```bash
# Build image
make docker

# Run
docker run -p 8080:8080 \
  -e BAHIA_DB_HOST=db.example.com \
  -e BAHIA_DB_PASSWORD=secret \
  bahia:latest
```

### Environment Variables

See `.env.example` for all available configuration options.

Nested runtime target settings use double underscores in environment variables:

```bash
export BAHIA_RUNTIME__DEFAULT__TYPE=compose
export BAHIA_RUNTIME__DEFAULT__COMPOSE_DIR=/srv/bahia/compose/default
export BAHIA_RUNTIME__ENVIRONMENTS__production__COMPOSE_DIR=/srv/bahia/compose/production
export BAHIA_RUNTIME__ENVIRONMENTS__production__DOCKER_HOST=tcp://docker-prod.example.com:2375
```

## Runtime Targeting

Bahia supports a process-wide runtime default plus per-environment runtime targets:

```yaml
runtime:
  # Legacy flat keys are still accepted as fallback defaults.
  type: docker
  docker_host: unix:///var/run/docker.sock

  default:
    type: compose
    docker_host: unix:///var/run/docker.sock
    compose_dir: /srv/bahia/compose/default

  environments:
    staging:
      compose_dir: /srv/bahia/compose/staging
    production:
      docker_host: tcp://docker-prod.example.com:2375
      compose_dir: /srv/bahia/compose/production
```

Resolution order is: legacy flat `runtime.*`, then `runtime.default.*`, then `runtime.environments.<environment-name>.*`, then the persisted `Environment.runtime_config` keys (`type`, `docker_host`, `podman_host`, `compose_dir`, `kube_context`, `kube_namespace`, `kube_config`). A service's `runtime_type` remains authoritative for whether Bahia uses Docker, Compose, Kubernetes, or Podman; environment-specific `type` overrides are rejected if they conflict with the service.

## Podman Runtime

Bahia supports Podman as an alternative to Docker. Since Podman emulates Docker's API, Bahia communicates with Podman via its Docker-compatible socket.

### Configuration

```yaml
runtime:
  type: podman
  # Rootless Podman (default if omitted):
  podman_host: unix:///run/user/1000/podman/podman.sock

  # Or for rootful Podman:
  # podman_host: unix:///run/podman/podman.sock
```

### Socket Paths

| Mode | Socket Path |
|------|-------------|
| Rootless | `unix:///run/user/<UID>/podman/podman.sock` |
| Rootful | `unix:///run/podman/podman.sock` |

### Enabling the Podman Socket

Podman's API socket is not enabled by default. Enable it with:

```bash
# Rootless (user service)
systemctl --user enable --now podman.socket

# Rootful (system service)
sudo systemctl enable --now podman.socket
```

### Environment Variables

```bash
export BAHIA_RUNTIME__DEFAULT__TYPE=podman
export BAHIA_RUNTIME__DEFAULT__PODMAN_HOST=unix:///run/user/1000/podman/podman.sock
```

For Compose, Bahia intentionally uses **one Compose project directory per Bahia environment**. Multiple environments can point to different `compose_dir` values, but services in the same environment share that Compose project.

Compose files should expose image overrides using the service-name-derived environment variable pattern. For example, service `agent-api` maps to `AGENT_API_IMAGE`:

```yaml
services:
  agent-api:
    image: ${AGENT_API_IMAGE:-registry.example.com/agent-api:latest}
```

When running Bahia inside a container with the Compose runtime, mount both `/var/run/docker.sock` and the configured compose project directory at the same path used by `runtime.default.compose_dir` or the per-environment `compose_dir`.

## Monitoring

- Health check: `GET /health`
- Readiness check: `GET /ready`
- Drift detection: `GET /api/v1/state/drifted`
