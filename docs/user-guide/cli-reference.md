# CLI Reference

The `bahia` CLI provides command-line access to all Bahia operations.

## Installation

```bash
# From source
go install github.com/openagentsinc/bahia/cmd/cli@latest

# Or build locally
cd bahia
make build
./bin/bahia --help
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `BAHIA_API_URL` | API server URL | `http://localhost:8080` |
| `BAHIA_NOSTR_RELAYS` | Comma-separated final relay URLs for operator/ContextVM transport; takes precedence over bootstrap discovery | unset |
| `BAHIA_NOSTR_BOOTSTRAP_RELAYS` | Comma-separated bootstrap relay seeds used only for trusted operator relay discovery when `BAHIA_NOSTR_RELAYS` and `--relay` are absent | unset |
| `BAHIA_NOSTR_SERVICE_PUBKEY` | Bahia service pubkey for signer-first routing; also accepted as single-service discovery trust | unset |
| `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS` | Comma-separated trusted Bahia service pubkeys for operator bootstrap discovery | unset |
| `BAHIA_AUTH_ENABLED` | Enable authentication | `false` |
| `BAHIA_OPERATOR_HTTP_FALLBACK` | Allow HTTP fallback | `false` |

### Config File

```yaml
# ~/.bahia/config.yaml
api_url: "https://bahia.example.com"
relays:
  - "wss://relay.example.com"
auth:
  enabled: true
```

## Authentication

### NIP-07 (Browser Extension)

```bash
# Authenticate with browser extension
bahia auth login --nip07
```

### NIP-46 (Remote Signer)

```bash
# Connect to bunker
bahia auth login --nip46 "bunker://pubkey@relay?secret=..."
```

### Verify Auth

```bash
bahia auth status
```

## Nostr-native transport

The CLI uses ContextVM JSON-RPC methods over Nostr kind `25910`, normally wrapped with CEP-4/NIP-59 gift-wrap (`1059` or `21059`) when encrypted transport is available. Reads consume canonical observable/state kinds (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`) and standard NIPs. Legacy Bahia request kinds are not production CLI transport; they are retained only as startup migration/test fixtures.

Operator relay resolution is deterministic and ordered: explicit `--relay` values are final and highest priority, `BAHIA_NOSTR_RELAYS` is second, and trusted bootstrap discovery is used only when both final relay sources are absent. Discovery requires at least one bootstrap relay (`--bootstrap-relay` or `BAHIA_NOSTR_BOOTSTRAP_RELAYS`) and at least one trusted service pubkey (`--trusted-service-pubkey`, `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS`, or single-service `--service-pubkey` / `BAHIA_NOSTR_SERVICE_PUBKEY`). The CLI queries trusted service-authored NIP-51 `30002` relay sets until EOSE, prefers `d=bahia-contextvm-v1`, and falls back to `d=bahia-browser-v1` only when no usable ContextVM relay set is present. If relay `OK`, `CLOSED`, or `AUTH` outcomes leave no usable relay after a signed ContextVM event is accepted, the CLI reports the relay failure rather than falling back to REST unless the explicit HTTP fallback is still in a pre-acceptance failure path.

## Commands

### Services

```bash
# List services
bahia services list
bahia services list -o json

# Get service
bahia services get payment-api
bahia services get svc-123 -o yaml

# Create service
bahia services create \
  --name "payment-api" \
  --repository "https://github.com/company/payment-api"

# Update service
bahia services update payment-api \
  --description "Updated description"

# Delete service
bahia services delete payment-api
```

### Environments

```bash
# List environments
bahia environments list

# Get environment
bahia environments get production

# Create environment
bahia environments create \
  --name "Production" \
  --slug "production"

# Update environment
bahia environments update production \
  --requires-approval true

# Delete environment
bahia environments delete staging
```

### Deployments

Deployment intent create, approval/rejection, and rollback mutations use ContextVM JSON-RPC methods such as `service/deploy`, `approval/approve`, `approval/reject`, and `service/rollback`. Legacy REST-backed command paths and legacy Nostr request-kind publication are not production runtime behavior.

```bash
# List intents
bahia deployments list
bahia deployments list --service payment-api

# Get intent
bahia deployments get intent-123

# List runs
bahia deployments runs list
bahia deployments runs get run-456

# View logs
bahia deployments logs run-456 --tail 100
```

### State

```bash
# List state
bahia state list
bahia state list --environment production
bahia state list --service payment-api

# Drifted services
bahia state drifted
bahia state drifted --environment production
```

### Direct Runtime Actions

Direct runtime deploy/restart/stop REST endpoints have been removed. The CLI surface is ContextVM methods `service/deploy`, `service/restart`, and `service/stop`; production CLI paths do not publish legacy runtime request kinds.

### Artifacts

Artifact registration is a ContextVM Nostr operation. Legacy REST-backed artifact registration command paths are not production CLI mutation transport.

```bash
# List artifacts
bahia artifacts list --service-id svc-123

# Get artifact
bahia artifacts get art-456

# SBOM
bahia artifacts sbom art-456
bahia artifacts sbom art-456 --packages

# Signatures
bahia artifacts signatures art-456
bahia artifacts verify art-456
```

### Builds

```bash
# List builds
bahia builds list --service-id svc-123

# Get build
bahia builds get build-123

# Register build
bahia builds register \
  --service-id svc-123 \
  --workflow-id "ci-run-456" \
  --commit-sha "abc123"
```

### Workers

```bash
# List workers
bahia workers list

# Get worker
bahia workers get npub1worker...

# Pricing
bahia workers pricing npub1worker...
```

### Policies

Policy create, update, delete, and manual evaluation mutations are ContextVM Nostr operations. Deletion uses NIP-09 kind `5` where relay-level deletion semantics apply; domain state may also publish canonical tombstone projections. Legacy REST-backed policy mutation command paths are not production CLI mutation transport.

```bash
# List policies
bahia policies list

# Get policy
bahia policies get require-sbom
```

### LLM Routes

```bash
# List routes
bahia llm routes list

# Get route
bahia llm routes get gpt4-proxy

# Releases
bahia llm releases list --route-id route-123

# Deploy
bahia llm deploy \
  --route-id route-123 \
  --release-id release-456 \
  --environment production

# Approve
bahia llm approve intent-123

# Rollback
bahia llm rollback \
  --route-id route-123 \
  --environment production

# State
bahia llm state list
bahia llm state drifted
```

LLM route creation and release registration are ContextVM Nostr operations. Legacy request kinds such as `5971`/`5972` are startup migration inventory only and are not production CLI mutation transport.

### Souls

```bash
# List souls
bahia souls list
bahia souls list --status active

# Get soul
bahia souls get scout

# Provision
bahia souls provision scout \
  --template "31950:pubkey:research-agent" \
  --tier standard \
  --follow

bahia souls provision codebot \
  --brief "A code review specialist" \
  --tier heavy

# Lifecycle
bahia souls suspend scout --reason "Maintenance"
bahia souls resume scout
bahia souls revoke scout --reason "No longer needed"
bahia souls redeploy scout
bahia souls regenerate scout --brief "New purpose..."

# Templates
bahia souls templates list
bahia souls templates get research-agent
```

### Notifications

```bash
# List channels
bahia notifications channels list

# Get channel
bahia notifications channels get channel-123

# Create channel
bahia notifications channels create \
  --name "Deploy Alerts" \
  --type webhook \
  --config url="https://hooks.example.com/bahia"

# Update channel
bahia notifications channels update channel-123 \
  --events deployment.completed,deployment.failed

# Delete channel
bahia notifications channels delete channel-123

# Test channel
bahia notifications channels test channel-123

# Logs
bahia notifications log --limit 50
```

### Adoption

```bash
# Scan for containers
bahia adopt scan \
  --target name=prod,endpoint_ref=prod-docker

# Import discovered containers
bahia adopt import \
  --target name=prod,endpoint_ref=prod-docker \
  --all
```

### Payments

```bash
# Estimate cost
bahia payments estimate \
  --service-id svc-123 \
  --environment-id env-456

# Get run cost
bahia payments cost run-789

# Payment history
bahia payments history
bahia payments history --worker npub1worker...
```

## Output Formats

```bash
# Table (default)
bahia services list

# JSON
bahia services list -o json

# YAML
bahia services get payment-api -o yaml
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--api-url` | Override API URL |
| `--relay` | Specify final operator relay (repeatable; highest priority) |
| `--bootstrap-relay` | Specify bootstrap relay seed for trusted operator discovery (repeatable) |
| `--service-pubkey` | Specify Bahia service pubkey for routing and single-service discovery trust |
| `--trusted-service-pubkey` | Specify trusted Bahia service pubkey for bootstrap discovery (repeatable) |
| `--http-fallback` | Allow HTTP fallback |
| `-o, --output` | Output format (table, json, yaml) |
| `-v, --verbose` | Verbose output |
| `--help` | Show help |

## Related

- [Getting Started](getting-started.md) — Setup guide
- [MCP Tools](mcp-tools.md) — Programmatic access
- [Nostr Integration](nostr-integration.md) — Event model
