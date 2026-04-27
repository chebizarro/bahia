# Deployment Guide

This guide covers how to run Bahia in different environments.

## Prerequisites

- Go 1.24+
- PostgreSQL 16+
- Docker (for runtime observation)

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

## Monitoring

- Health check: `GET /health`
- Readiness check: `GET /ready`
- Drift detection: `GET /api/v1/state/drifted`
