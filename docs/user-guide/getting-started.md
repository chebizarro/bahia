# Getting Started with Bahia

This guide walks you through setting up Bahia and deploying your first service.

## Prerequisites

- **Docker** and **Docker Compose** (for quick start)
- OR: **Go 1.24+** and **PostgreSQL 16+** (for development)
- A **Nostr keypair** (for signer-first operations)

## Quick Start with Docker Compose

The fastest way to get Bahia running:

```bash
# Clone the repository
git clone https://github.com/openagentsinc/bahia.git
cd bahia

# Start all services
docker compose up --build

# Verify health
curl http://localhost:8080/health
# Expected: {"status":"ok"}

# Open the web UI
open http://localhost:3000
```

This starts:
- **Bahia API server** on port 8080
- **Web UI** on port 3000
- **PostgreSQL** database
- **Relay sidecar** for Nostr events

## Development Setup

For local development without Docker:

```bash
# Install Go dependencies
make deps

# Set up PostgreSQL (example with psql)
createdb bahia
export DATABASE_URL="postgres://localhost/bahia?sslmode=disable"

# Run database migrations
make migrate

# Start the development server
make run-dev
```

## Configuration

Bahia is configured via environment variables or a config file.

### Essential Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string | (required) |
| `BAHIA_HTTP_ADDR` | API server address | `:8080` |
| `BAHIA_AUTH_ENABLED` | Enable NIP-98 authentication | `false` |
| `BAHIA_NOSTR_RELAYS` | Comma-separated relay URLs | (discovery) |

### Config File (bahia.yaml)

```yaml
http:
  addr: ":8080"

database:
  url: "postgres://localhost/bahia?sslmode=disable"

auth:
  enabled: true
  bootstrap_owner_pubkeys:
    - "your-nostr-pubkey-hex"

nostr:
  relays:
    - "wss://relay.example.com"
  sidecar:
    public_url: "wss://sidecar.example.com"
```

## Your First Deployment

### Step 1: Sign in to the Web UI

1. Open `http://localhost:3000`
2. Click **Sign In**
3. Connect with your Nostr signer (NIP-07 extension or NIP-46 bunker)

### Step 2: Create a Service

A **service** represents an application you want to deploy.

**Via Web UI:**
1. Navigate to **Services** in the sidebar
2. Click **New Service**
3. Fill in:
   - **Name**: `my-api`
   - **Repository**: `https://github.com/org/my-api`
4. Click **Create**

**Via CLI:**
```bash
bahia services create \
  --name "my-api" \
  --repository "https://github.com/org/my-api"
```

**Via MCP:**
```json
{
  "tool": "bahia_service_create",
  "arguments": {
    "name": "my-api",
    "repository": "https://github.com/org/my-api"
  }
}
```

### Step 3: Create an Environment

An **environment** is a deployment target like staging or production.

**Via Web UI:**
1. Navigate to **Environments**
2. Click **New Environment**
3. Fill in:
   - **Name**: `staging`
   - **Slug**: `staging`
4. Click **Create**

### Step 4: Register an Artifact

**Artifacts** are container images produced by CI.

When your CI pipeline builds an image, register it with Bahia:

```bash
bahia artifacts register \
  --service-id "<service-uuid>" \
  --image "registry.example.com/my-api:v1.0.0" \
  --digest "sha256:abc123..."
```

Or use the Hive-CI bridge for automatic artifact registration from CI events.

### Step 5: Deploy

Create a **deployment intent** to request a deployment:

**Via Web UI:**
1. Go to **Services** → select your service
2. Click **Deploy**
3. Select the environment and artifact
4. Click **Create Intent**

**Via CLI:**
```bash
bahia deploy \
  --service "<service-id>" \
  --environment "<environment-id>" \
  --artifact "<artifact-id>"
```

### Step 6: Monitor the Deployment

**Via Web UI:**
- View deployment status on the **Deployments** page
- Check logs in the deployment run detail view

**Via Nostr:**
- Subscribe to `6961` (DeploymentStatus) for progress
- Subscribe to `7961` (DeploymentResult) for completion
- Subscribe to `31961` (ServiceState) for current state

## Understanding the Flow

```
1. CI builds image → publishes build event
2. Bahia registers artifact
3. User creates deployment intent
4. Policy evaluation (optional approval)
5. Deployment run executes on worker
6. Runtime observation confirms state
7. Read models updated on Nostr
```

## Next Steps

- Read [Core Concepts](core-concepts.md) to understand the data model
- Explore [Services](features/services.md) for advanced configuration
- Set up [Notifications](features/notifications.md) for alerts
- Learn about [Nostr Integration](nostr-integration.md) for real-time updates

## Common Issues

### "Connection refused" on localhost:8080

Ensure the server is running:
```bash
docker compose ps
# or
make run-dev
```

### "Unauthorized" errors

Enable authentication and provide your Nostr pubkey:
```yaml
auth:
  enabled: true
  bootstrap_owner_pubkeys:
    - "your-pubkey-hex"
```

### No relay connection

Check relay configuration:
```bash
curl http://localhost:8080/.well-known/nostr.json
```

See [Troubleshooting](troubleshooting.md) for more solutions.
