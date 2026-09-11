# Getting Started with Bahia

This guide walks you through setting up Bahia and deploying your first service.

## Prerequisites

- **Docker** and **Docker Compose** (for quick start)
- OR: **Go 1.26.3+** and **PostgreSQL 16+** (for development)
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
# Expected: {"status":"ok","version":"..."}

# Open the web UI
open http://localhost:3000
```

This starts:
- **Bahia API server** (`bahia`) on port 8080
- **Web UI** (`web`) on port 3000
- **PostgreSQL** (`postgres`) on port 5432
- **Nostr relay** (`relay`) on port 3334

## Development Setup

For local development without Docker:

```bash
# Install Go dependencies
make deps

# Set up PostgreSQL (example with psql)
createdb bahia
export BAHIA_DB_HOST=localhost
export BAHIA_DB_PORT=5432
export BAHIA_DB_USER="$USER"
export BAHIA_DB_NAME=bahia
export BAHIA_DB_SSLMODE=disable

# Start the development server with the repo's config.yaml; startup applies database migrations
make run-dev
```

`make run-dev` runs `go run ./cmd/server -config config.yaml`. Environment variables override values from the config file.

## Configuration

Bahia is configured via environment variables or a config file.

### Essential Environment Variables

Environment variables follow `BAHIA_<SECTION>_<FIELD>` (for example `BAHIA_DB_HOST` → `db.host`). Defaults below are the built-in code defaults; the repo's example `config.yaml` overrides several of them for local development (`server.host: 0.0.0.0`, `db.password: bahia`, `db.sslmode: disable`).

| Variable | Description | Code default |
|----------|-------------|---------|
| `BAHIA_DB_HOST` | PostgreSQL host | `localhost` |
| `BAHIA_DB_PORT` | PostgreSQL port | `5432` |
| `BAHIA_DB_USER` | PostgreSQL user | `bahia` |
| `BAHIA_DB_PASSWORD` | PostgreSQL password | (empty) |
| `BAHIA_DB_NAME` | PostgreSQL database | `bahia` |
| `BAHIA_DB_SSLMODE` | PostgreSQL TLS mode | `require` |
| `BAHIA_SERVER_HOST` | API listen host | `127.0.0.1` |
| `BAHIA_SERVER_PORT` | API listen port | `8080` |
| `BAHIA_AUTH_ENABLED` | Enable NIP-98 authentication | `false` |
| `BAHIA_NOSTR_RELAYS` | Backward-compatible service relay alias | (none) |
| `BAHIA_NOSTR_SERVICE_RELAYS` | Backend service publish/backfill relays | `BAHIA_NOSTR_RELAYS` |
| `BAHIA_NOSTR_BROWSER_RELAYS` | Browser-safe bootstrap/read relays | (discovery) |
| `BAHIA_NOSTR_CONTEXTVM_RELAYS` | Direct ContextVM request/reply relays; when the sidecar is enabled Bahia uses their deduplicated union with the sidecar, falling back to browser relays when absent | browser relays |
| `BAHIA_NOSTR_RELAY_AUTH_UNAVAILABLE` | Relay AUTH-unavailable behavior; only `exclude_and_fail` is valid | `exclude_and_fail` |
| `BAHIA_SBOM_CDXGEN_ENABLED` | Enable optional cdxgen executable adapter for repository CycloneDX SBOM generation | `false` |
| `BAHIA_SBOM_CDXGEN_BINARY_PATH` | Path or executable name for cdxgen when enabled | `cdxgen` |
| `BAHIA_ASSISTANT_AGENTIC_ENABLED` | Run the multi-step agentic assistant loop; set `false` to use the legacy plan/approve planner | `true` |
| `BAHIA_ASSISTANT_AGENTIC_TOOL_MODE` | Agentic OpenAI-compatible tool harness: `native` sends provider tool calls; `prompted` injects text tool instructions for models without native function-calling | `native` |
| `BAHIA_ASSISTANT_LLM_STREAMING` | Enable streaming chat completions for the legacy assistant planner provider | `false` |

### Config File (config.yaml)

Pass a YAML file with `bahia-server -config <path>` (`make run-dev` uses `config.yaml`). A minimal excerpt:

```yaml
server:
  host: "0.0.0.0"
  port: 8080

db:
  host: localhost
  port: 5432
  user: bahia
  password: bahia
  name: bahia
  sslmode: disable

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
  # Direct ContextVM request/reply destinations. An enabled sidecar augments this list.
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

soul_factory:
  # Durable local identity mapping used by the agent-memory adapter across restarts.
  agent_memory_task_id_file: "/var/lib/bahia/agent-memory/task-ids.json"

assistant:
  # The assistant is disabled by default (assistant.enabled: false).
  enabled: true
  # When enabled, it uses the multi-step agentic loop by default in audited permission mode.
  # If agentic.model/base_url/api_key are omitted, they inherit these legacy llm_* fields.
  llm_base_url: "https://api.openai.com"
  llm_model: "<assistant-model>"
  llm_api_key: "<provider-api-key>"
  agentic:
    enabled: true
    # native is the default. Use prompted for OpenAI-compatible endpoints whose
    # models do not implement native tools/function-calling (for example local
    # llama.cpp-compatible instruction models). In prompted mode Bahia asks for
    # a fenced tool_call block containing {"name":"<tool name>","arguments":{}}.
    tool_mode: "native"
    # Optional overrides; omit these to inherit llm_base_url/llm_model/llm_api_key above.
    # base_url: "https://api.openai.com"
    # model: "<agentic-model>"
    # api_key: "<agentic-api-key>"
  permissions:
    mode: "audited"
  # Legacy planner escape hatch:
  # agentic:
  #   enabled: false
  # Disabled by default. Enable only for legacy planner providers that emit delta.content
  # when streaming response_format (json_schema) chat completions.
  llm_streaming: false
```

## Your First Deployment

### Step 1: Sign in to the Web UI

1. Open `http://localhost:3000`
2. Click **Sign In**
3. Connect with your Nostr signer (NIP-07 extension or NIP-46 bunker)

Protected routes, including Settings, fail closed until a signer-first session is present. NIP-98 backend readiness is established only after a signed `GET /orgs` succeeds with a 2xx response; a capability advertisement alone is provisional.

After sign-in, open the user menu and choose **Edit Profile**, or go directly to `/settings/profile`, to edit your Nostr kind-0 metadata. The profile editor validates fields locally, signs the kind-0 event with the active NIP-07 or NIP-46 signer, publishes to writable Nostr relays from the signer/NIP-65 relay list, and shows the relay OK acceptance/rejection outcomes.

### Step 2: Create a Service

A **service** represents an application you want to deploy.

**Via Web UI:**
1. Navigate to **Services** in the sidebar
2. Click **Create Service**
3. Fill in the required **Name**, **Artifact Repository**, and **Runtime Type** fields. Optionally select a Git URL or NIP-34 repository and its default branch.
4. Click **Create**

**Via CLI:**
```bash
bahia services create \
  --name "my-api" \
  --artifact-repo "ghcr.io/org/my-api"
```

The CLI publishes the signer-first ContextVM `service/create` request. The registered `bahia_create_service` MCP compatibility tool is not a portable onboarding path: the standard server leaves the external MCP allowlist empty, so external `tools/call` requests fail closed unless an embedding explicitly authorizes callers (see [MCP Tools](mcp-tools.md)).

### Step 3: Create an Environment

An **environment** is a deployment target like staging or production.

**Via Web UI:**
1. Navigate to **Environments**
2. Click **Create Environment**
3. Select the owning **Organization**, enter **Name** (`staging`), and review the optional placement, deployment-unit, strategy, and protection settings.
4. Click **Create**

### Step 4: Register an Artifact

**Artifacts** are container images produced by CI.

The normal path is automatic registration from a trusted signed HiveCI result. For an explicitly enabled recovery workflow (`hiveci.allow_manual_artifact_registration: true`), the CLI can publish a signer-first ContextVM registration request:

```bash
bahia artifacts register \
  --build <build-id> \
  --service <service-id> \
  --image-repo ghcr.io/org/my-api \
  --image-tag <tag> \
  --image-digest sha256:<manifest-digest>
```

Manual registration remains subject to server-side build, service, repository, digest, and registry verification.

### Step 5: Deploy

Create a **deployment intent** to request a deployment:

**Via Web UI:**
1. Go to **Services** → select your service
2. Click **Deploy**
3. Select the environment and artifact
4. Review each step, then click **Sign & submit idempotently**

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

Bahia does not serve an HTTP relay-discovery document; relays come from configuration (`nostr.service_relays`, `nostr.browser_relays`, `nostr.contextvm_relays`) and Nostr-native discovery (ContextVM `11316`-`11320`, NIP-51 `30002`). Confirm those values are set and reachable.

In the web UI, open **Settings → Relays** (`/settings/relays`) to inspect persistent operator relay policy, validate local browser relay URLs, and reconnect the local browser session. Reconnect results explicitly report whether all, some, or no configured local browser relays connected.

See [Troubleshooting](troubleshooting.md) for more solutions.

### Managed-instance supervision

`supervision.enabled` starts local runtime health checks. Recovery is safe by default: `supervision.observe_only` defaults to `true`. Configure `interval`, `memory_threshold`, and explicit `instances`; Bahia-managed desired deployment units are also discovered from durable environment-service state. Each explicit instance identifies its service, environment, deployment unit, exact runtime target, supervisor type, desired-running intent, probe, restart budget, backoff, and warning interval.
