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
| `BAHIA_NOSTR_RELAYS` | Backward-compatible service relay alias | (none) |
| `BAHIA_NOSTR_SERVICE_RELAYS` | Backend service publish/backfill relays | `BAHIA_NOSTR_RELAYS` |
| `BAHIA_NOSTR_BROWSER_RELAYS` | Browser-safe bootstrap/read relays | (discovery) |
| `BAHIA_NOSTR_CONTEXTVM_RELAYS` | ContextVM request/reply relays; falls back to browser relays when absent | browser relays |
| `BAHIA_NOSTR_RELAY_AUTH_UNAVAILABLE` | Relay AUTH-unavailable behavior; only `exclude_and_fail` is valid | `exclude_and_fail` |
| `BAHIA_SBOM_CDXGEN_ENABLED` | Enable optional cdxgen executable adapter for repository CycloneDX SBOM generation | `false` |
| `BAHIA_SBOM_CDXGEN_BINARY_PATH` | Path or executable name for cdxgen when enabled | `cdxgen` |
| `BAHIA_ASSISTANT_LLM_STREAMING` | Enable streaming chat completions for the legacy assistant planner provider | `false` |

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
  # service_relays is the backend publish/backfill source; relays is only a compatibility alias.
  service_relays:
    - "wss://service-relay.example.com"
  browser_relays:
    - "wss://sidecar.example.com"
  contextvm_relays:
    - "wss://contextvm-relay.example.com"
  relay_auth_unavailable: "exclude_and_fail"
  sidecar:
    public_url: "wss://sidecar.example.com"

sbom:
  cdxgen:
    # Disabled by default; Syft remains the fallback/default generator.
    enabled: false
    binary_path: "cdxgen"

assistant:
  llm_base_url: "https://api.openai.com"
  llm_model: "<planner-model>"
  # Disabled by default. Enable only for providers that emit delta.content when
  # streaming response_format (json_schema) chat completions.
  llm_streaming: false
```

## Your First Deployment

### Step 1: Sign in to the Web UI

1. Open `http://localhost:3000`
2. Click **Sign In**
3. Connect with your Nostr signer (NIP-07 extension or NIP-46 bunker)

Protected routes, including Settings, fail closed until a signer-first session is present. Routes that still depend on REST compatibility require the backend to advertise `direct_nostr_http_auth`; otherwise the UI shows a compatibility-required state instead of making REST calls.

After sign-in, open the user menu and choose **Edit Profile**, or go directly to `/settings/profile`, to edit your Nostr kind-0 metadata. The profile editor validates fields locally, signs the kind-0 event with the active NIP-07 or NIP-46 signer, publishes to writable Nostr relays from the signer/NIP-65 relay list, and shows the relay OK acceptance/rejection outcomes.

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

When your CI pipeline builds an image, register it with Bahia by publishing a signed Nostr `ArtifactRegister` event, or use the Hive-CI bridge for automatic artifact registration from CI events. The legacy REST-backed artifact registration command path is deprecated until the CLI publishes signed Nostr events directly.

### Step 5: Deploy

Create a **deployment intent** to request a deployment:

**Via Web UI:**
1. Go to **Services** → select your service
2. Click **Deploy**
3. Select the environment and artifact
4. Click **Create Intent**

**Via Nostr:** publish a ContextVM `service/deploy` request as kind `25910` (or encrypted `1059`/`21059`) and follow canonical `30315`, `4903`, and `30900` observables. Legacy `DeployRequest` custom kinds are startup migration inputs only.

### Step 6: Monitor the Deployment

**Via Web UI:**
- View deployment status on the **Deployments** page
- Check logs in the deployment run detail view

**Via Nostr:**
- Subscribe to `30315` (NIP-38 operational status) for progress
- Subscribe to `4903` for audit/provenance facts
- Subscribe to `30900` for current service/deployment state

## Understanding the Flow

```
1. CI builds image → publishes build event
2. Bahia registers artifact
3. User creates deployment intent
4. Policy evaluation (optional approval)
5. Deployment run executes on worker
6. Runtime observation confirms state
7. Canonical observables updated on Nostr (`30900`, `30315`, `4903`)
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

Check relay discovery from the server:
```bash
curl http://localhost:8080/.well-known/nostr.json
```

In the web UI, open **Settings → Relays** (`/settings/relays`) to inspect persistent operator relay policy, validate local browser relay URLs, and reconnect the local browser session. Reconnect results explicitly report whether all, some, or no configured local browser relays connected.

See [Troubleshooting](troubleshooting.md) for more solutions.
