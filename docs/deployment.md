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
export BAHIA_RUNTIME__ENVIRONMENTS__production__ENDPOINT_REF=prod-docker
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__DOCKER_HOST=tcp://docker-prod.example.com:2376
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CA_CERT_FILE=/etc/bahia/docker/ca.pem
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CLIENT_CERT_FILE=/etc/bahia/docker/cert.pem
export BAHIA_RUNTIME__ENDPOINTS__prod-docker__CLIENT_KEY_FILE=/etc/bahia/docker/key.pem
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

  endpoints:
    prod-docker:
      docker_host: tcp://docker-prod.example.com:2376
      ca_cert_file: /etc/bahia/docker/ca.pem
      client_cert_file: /etc/bahia/docker/cert.pem
      client_key_file: /etc/bahia/docker/key.pem
      # Compatibility only; prefer verified TLS for live endpoints.
      insecure_skip_verify: false

  environments:
    staging:
      compose_dir: /srv/bahia/compose/staging
    production:
      endpoint_ref: prod-docker
      compose_dir: /srv/bahia/compose/production
```

Resolution order is: legacy flat `runtime.*`, then `runtime.default.*`, then `runtime.environments.<environment-name>.*`, then the persisted `Environment.runtime_config` keys (`type`, `endpoint_ref`, `docker_host`, `podman_host`, `compose_dir`, `kube_context`, `kube_namespace`, `kube_config`). When `endpoint_ref` is present, Bahia resolves the concrete Docker host and TLS material from server-managed `runtime.endpoints` and does not need callers or imported environments to carry raw Docker credentials. A service's `runtime_type` remains authoritative for whether Bahia uses Docker, Compose, Kubernetes, or Podman; environment-specific `type` overrides are rejected if they conflict with the service.

Docker API access accepts individual CA/client certificate file paths. Docker Compose uses the Docker CLI `DOCKER_CERT_PATH` convention, so configured Compose endpoint certificates must live in one directory with Docker's standard names (`ca.pem`, `cert.pem`, `key.pem`).

For adoption, prefer endpoint aliases:

```bash
bahia adopt scan --target prod-docker
bahia adopt import --target prod-docker --all
```

Raw Docker hosts are a compatibility path only. They require the server to set `adoption.allow_raw_docker_hosts: true` and the CLI to use `--raw-target alias=dockerHost`.

For production rollout, follow the operator runbook in [`adoption-production-rollout.md`](adoption-production-rollout.md). In short: enable auth, configure operator allowlists, keep raw-host mode off, run a scan-only dry run, import a single low-risk workload first, then monitor adoption/runtime logs and `/metrics` before expanding.

Dedicated operational limits protect runtime endpoints from expensive control-plane bursts:

- adoption scan: 5 requests/minute/IP;
- adoption import: 10 requests/minute/IP;
- direct runtime deploy/restart/stop: 20 requests/minute/IP.

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

## OCI Registry Configuration

Bahia includes an internal OCI-compliant container registry. Configure it in your config file:

```yaml
oci:
  enabled: true
  public_host: registry.sharegap.net
  spool_dir: /srv/data/bahia/oci-uploads
  upload_expiry: 24h
  
  # Anonymous pull from internal network
  allow_anonymous_pull_cidrs:
    - 192.168.40.0/24
    - 10.0.0.0/8
  
  # Trusted proxies for X-Forwarded-For
  trusted_proxy_cidrs:
    - 127.0.0.1/32
    - 172.17.0.0/16
  
  # Service accounts for push access
  service_accounts:
    - username: hive-ci
      password_hash: "$2a$10$..."  # bcrypt hash
      permissions: [pull, push]
      repo_prefixes: [cascadia/]
```

### Blossom Backend

The registry uses Blossom for blob storage. Ensure Blossom is configured:

```yaml
blossom:
  base_url: https://blossom.sharegap.net
  auth_pubkey: <your-blossom-auth-pubkey>
```

### Spool Directory

Create the spool directory for upload chunks:

```bash
mkdir -p /srv/data/bahia/oci-uploads
chown bahia:bahia /srv/data/bahia/oci-uploads
```

## Hive-CI Integration

Enable automatic CI event ingestion:

```yaml
hiveci:
  enabled: true
  
  # Trusted CI dispatcher pubkeys (5401 events)
  trusted_ci_pubkeys:
    - <hive-ci-dispatcher-pubkey>
  
  # Auto-create builds from CI results
  auto_register_builds: true
  
  # Auto-deploy to staging environment
  auto_deploy_staging_environment: edge-01-staging
  
  # Retry configuration
  retry_interval: 30s
  max_retries: 10
```

## Monitoring

- Health check: `GET /health`
- Readiness check: `GET /ready`
- Drift detection: `GET /api/v1/state/drifted`
- Registry API: `GET /v2/`
