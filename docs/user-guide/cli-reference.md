# CLI Reference

The `bahia` CLI provides command-line access to Bahia’s current HTTP-compatible read surfaces plus signer-first operator commands.

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
| `BAHIA_SERVER` | Bahia server URL | `http://localhost:8080` |
| `BAHIA_NOSTR_KEY_FILE` | File containing a local Nostr private key for signer-first operations | unset |
| `BAHIA_NOSTR_BUNKER_FILE` / `BAHIA_NOSTR_BUNKER_URI` | File containing, or direct value of, the NIP-46 bunker URI | unset |
| `BAHIA_NOSTR_BUNKER_RELAYS` | Comma-separated NIP-46 signer relay URLs | unset |
| `BAHIA_NOSTR_CLIENT_KEY_FILE` | Persistent NIP-46 client private-key file | unset |
| `BAHIA_NOSTR_CLIENT_PRIVATE_KEY` | Raw persistent NIP-46 client private key | unset |
| `BAHIA_NOSTR_NSEC` | Nostr private key in `nsec` form | unset |
| `BAHIA_NOSTR_PRIVATE_KEY` | Raw Nostr private key hex | unset |
| `BAHIA_NOSTR_RELAYS` | Comma-separated final relay URLs for signer-first operator transport | unset |
| `BAHIA_NOSTR_BOOTSTRAP_RELAYS` | Comma-separated bootstrap relay seeds used only when final relay sources are absent | unset |
| `BAHIA_NOSTR_SERVICE_PUBKEY` | Bahia service pubkey for signer-first routing and single-service discovery trust | unset |
| `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS` | Comma-separated trusted Bahia service pubkeys for bootstrap discovery | unset |
| `BAHIA_OPERATOR_HTTP_FALLBACK` | Allow explicit HTTP compatibility fallback before any relay accepts the request | `false` |

## Authentication

The CLI does not implement interactive `login` commands. The only built-in auth helper is:

```bash
bahia auth inspect
```

For local signing, use `--nostr-key-file`, `BAHIA_NOSTR_KEY_FILE`, `BAHIA_NOSTR_NSEC`, or `BAHIA_NOSTR_PRIVATE_KEY`.

For NIP-46 remote signing, use `--nostr-bunker-file` (or `BAHIA_NOSTR_BUNKER_FILE` / `BAHIA_NOSTR_BUNKER_URI`), at least one signer relay from the bunker URI, repeatable `--nostr-bunker-relay`, or `BAHIA_NOSTR_BUNKER_RELAYS`, and a persistent client key via `--nostr-client-key-file`, `BAHIA_NOSTR_CLIENT_KEY_FILE`, or `BAHIA_NOSTR_CLIENT_PRIVATE_KEY`. The CLI refuses simultaneous local-key and bunker configuration and does not generate a throwaway NIP-46 identity.

## Nostr-native transport

Signer-first CLI mutations use ContextVM JSON-RPC methods over Nostr kind `25910`, usually wrapped with CEP-4/NIP-59 gift-wrap (`1059` or `21059`) when encrypted transport is available. Reads consume canonical observable/state kinds (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`) and standard NIPs.

Operator relay resolution is deterministic and ordered:

1. Explicit `--relay` values are final and highest priority.
2. `BAHIA_NOSTR_RELAYS` is next.
3. Trusted bootstrap discovery is used only when both final relay sources are absent.

Trusted bootstrap discovery requires at least one bootstrap relay (`--bootstrap-relay` or `BAHIA_NOSTR_BOOTSTRAP_RELAYS`) and at least one trusted service pubkey (`--trusted-service-pubkey`, `BAHIA_NOSTR_TRUSTED_SERVICE_PUBKEYS`, or `--service-pubkey` / `BAHIA_NOSTR_SERVICE_PUBKEY`).

## Registered command groups

The current top-level CLI command groups are:

- `auth`
- `services`
- `environments`
- `state`
- `deployments`
- `adopt`
- `workers`
- `logs`
- `policies`
- `secrets`
- `orgs`
- `package`
- `souls`

Bahia does **not** currently register top-level `llm`, `payments`, `artifacts`, `builds`, or `notifications` CLI commands.

## Commands

### Services

```bash
# List services
bahia services list
bahia services list -o json

# Get service by ID
bahia services get svc-123
bahia services get svc-123 -o yaml

# Transitional create command (deprecated; signer-first mutations are the canonical path)
bahia services create \
  --name "payment-api" \
  --artifact-repo "ghcr.io/company/payment-api"

# Direct runtime lifecycle actions
bahia services actions deploy --service svc-123 --environment env-456 --artifact art-789
bahia services actions restart --service svc-123 --environment env-456
bahia services actions stop --service svc-123 --environment env-456
```

### Environments

Environment mutations publish signed ContextVM `environment/create` or `environment/update` requests. Deployment-unit helpers use the environment read model and publish the complete explicit unit set; they do not call REST mutation endpoints.

```bash
# Read environments (GET responses include deployment_units)
bahia environments list
bahia environments get <environment-id>

# Create or update an environment from a complete unit-set file
bahia environments create --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json

# List explicit units or the marked implicit default
bahia environments units list <environment-id>

# Create or update one unit using a JSON specification
bahia environments units create <environment-id> --file unit.json --default-unit-key max
bahia environments units update <environment-id> max --file unit.json --default-unit-key max
```

Omitting `--units-file` leaves the unit set unchanged on update. Supplying a file replaces the complete explicit set; use a JSON `[]` to return to the implicit default. Complete-set updates carry the environment's `updated_at` revision. On conflict, the CLI rereads and deliberately remerges at most three signed attempts before surfacing the conflict. `--default-unit-key` on unit create/update changes targeting in the same transaction; use it when the first explicit unit has a non-`default` key. Unit JSON follows `schemas/deployment_unit.json`.

### Deployments

```bash
# Submit signer-first deployment intent
bahia deployments deploy --service svc-123 --environment env-456 --artifact art-789

# Submit signer-first rollback intent
bahia deployments rollback --service svc-123 --environment env-456 --deployment-unit unit-789 --target-artifact artifact-prev --supersedes-intent intent-current
```

### State

```bash
# List desired/observed state
bahia state list
bahia state list --environment production
bahia state list --service payment-api

# Show drifted services
bahia state drifted
bahia state drifted --environment production
```

### Workers

```bash
# List workers
bahia workers list

# Show worker detail
bahia workers show npub1worker...
```

### Logs

```bash
# Fetch run logs
bahia logs run run-456 --tail 100

# Stream live logs for a service/environment
bahia logs live svc-123 env-456
```

### Policies

```bash
# Read policies
bahia policies list
bahia policies get require-sbom

# Create a signer-first policy
bahia policies create \
  --name require-sbom \
  --rules '[{"type":"require_sbom"}]' \
  --enforcement block \
  --idempotency-key policy-create-require-sbom
```

### Config fabric

```bash
# Publish a validated desired-state request; Bahia signs through operator Signet
bahia config publish --file config-request.json

# Compare desired events with applied/rejected status
bahia config drift

# Republish a prior desired event at the next version
bahia config rollback <desired-event-id>
```

### Secrets

```bash
# List secrets for a service
bahia secrets list svc-123

# Set a secret
bahia secrets set svc-123 DATABASE_URL postgres://example

# Delete a secret
bahia secrets delete svc-123 secret-456
```

### Organizations

```bash
# List organizations
bahia orgs list

# Get organization by ID or name
bahia orgs get acme-corp

# Create an organization
bahia orgs create acme-corp --display-name "ACME Corporation"

# List members
bahia orgs members list org-123

# Add a member
bahia orgs members add org-123 npub1member... --role deployer

# Remove a member
bahia orgs members remove org-123 npub1member...
```

### Package repositories and artifacts

```bash
# Create or update a package repository
bahia package repo apply \
  --name libs \
  --format npm \
  --backend-ref nexus-main \
  --backend-type nexus

# Delete a package repository
bahia package repo delete --name libs

# Upload an artifact
bahia package upload \
  --repository libs \
  --package widgets \
  --version 1.0.0 \
  --file ./dist/widgets-1.0.0.tgz

# Promote an artifact
bahia package promote \
  --source-repository libs \
  --target-repository production \
  --package widgets \
  --version 1.0.0 \
  --filename widgets-1.0.0.tgz

# Yank an artifact
bahia package yank \
  --repository production \
  --package widgets \
  --version 1.0.0 \
  --filename widgets-1.0.0.tgz \
  --reason "security issue"

# Trigger drift detection
bahia package drift --repository production
```

### Souls

Soul Factory is feature-gated and disabled by default unless Bahia is configured with `BAHIA_SOUL_FACTORY_ENABLED=true`.

```bash
# List souls
bahia souls list
bahia souls list --status active

# Get soul details
bahia souls get scout

# Provision
bahia souls provision scout \
  --template "31950:pubkey:research-agent" \
  --tier standard \
  --follow

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

### Adoption

```bash
# Scan for containers (target syntax is alias=endpointRef)
bahia adopt scan --target prod=prod-docker

# Import discovered containers and bind the signed request to an organization
bahia adopt import --target prod=prod-docker --all --org 11111111-1111-1111-1111-111111111111
```

`--org` is part of the signed import request. Use the destination organization UUID; it is not client-only display metadata.

## Output formats

```bash
# Table (default)
bahia services list

# JSON
bahia services list -o json

# YAML
bahia services get svc-123 -o yaml
```

## Global flags

| Flag | Description |
|------|-------------|
| `--server` | Override Bahia server URL |
| `--nostr-key-file` | Read a local Nostr private key from a file (use `-` for stdin) |
| `--nostr-bunker-file` | Read a NIP-46 bunker URI from a file |
| `--nostr-bunker-relay` | Add a NIP-46 signer relay (repeatable) |
| `--nostr-client-key-file` | Read the persistent NIP-46 client key from a file |
| `--relay` | Specify final operator relay (repeatable; highest priority) |
| `--bootstrap-relay` | Specify bootstrap relay seed for trusted operator discovery (repeatable) |
| `--service-pubkey` | Specify Bahia service pubkey for routing and single-service discovery trust |
| `--trusted-service-pubkey` | Specify trusted Bahia service pubkey for bootstrap discovery (repeatable) |
| `--http-fallback` | Allow explicit HTTP compatibility fallback before any relay accepts the request |
| `-o, --output` | Output format (`table`, `json`, `yaml`) |
| `--help` | Show help |

## Related

- [Getting Started](getting-started.md) — Setup guide
- [MCP Tools](mcp-tools.md) — Programmatic access
- [Nostr Integration](nostr-integration.md) — Event model
