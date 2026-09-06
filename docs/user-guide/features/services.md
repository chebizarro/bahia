# Services

A **Service** represents an application you deploy with Bahia — a web API, background worker, scheduled job, or any containerized workload.

## Overview

Services are the primary organizational unit in Bahia. Each service:
- Has a unique name and optional repository link
- Can be deployed to multiple environments
- Tracks builds, artifacts, and deployment history
- Has associated secrets, policies, and notifications

## Creating a Service

Service creation is signer-first. Clients publish a ContextVM JSON-RPC `service/create` intent as Nostr kind `25910`, usually wrapped with CEP-4/NIP-59 `1059` or `21059` for encrypted transport. Bahia also exposes transitional REST `POST /api/v1/services` when a control-plane command publisher is configured; that route publishes the same signed command, verifies relay `OK` acceptance, and returns a `202` command receipt with `request_event_id`, requester pubkey, request kind, status/result/read-model kinds, and published relay count. The immediate JSON-RPC response or REST receipt is only an acknowledgment; clients follow canonical `30900`, `30315`, and `4903` observables for durable state, progress, and audit truth.

### Web UI

1. Navigate to **Services** in the sidebar
2. Click **New Service**
3. Fill in the form:
   - **Name**: Unique identifier (e.g., `payment-api`)
   - **Display Name**: Human-friendly name (optional)
   - **Repository**: Git repository URL, or a NIP-34 repository selected from configured `nostr.nip34_relays`
   - **Description**: What the service does
4. Click **Create** to publish the signed Nostr command

### Nostr (ContextVM)

Publish a ContextVM `service/create` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"svc-create-payment-api\",\"method\":\"service/create\",\"params\":{\"name\":\"payment-api\",\"repository\":\"https://github.com/company/payment-api\",\"_meta\":{\"progressToken\":\"svc-create-payment-api\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/create"],
    ["service", "payment-api"]
  ]
}
```

Require relay `OK` with `accepted=true`, then subscribe to canonical observables scoped by `service`.

## Repository Selection

The service form can use a direct repository URL or a NIP-34 repository announcement. NIP-34 repository discovery queries the configured `nostr.nip34_relays` set for kind `30617` announcements; selected repositories retain their own `relays` tag values for subsequent branch/state lookups against kind `30618`.

## Service Properties

| Property | Description | Required |
|----------|-------------|----------|
| `name` | Unique identifier | Yes |
| `display_name` | Human-readable name | No |
| `repository` | Git repository URL | No |
| `description` | Service description | No |
| `tags` | Key-value metadata | No |
| `org_id` | Owning organization | No |

## Viewing Services

### Web UI

The **Services** page shows all services with:
- Name and description
- Latest deployment status per environment
- Drift indicators
- Quick actions (deploy, view)

Click a service to see:
- **Overview**: Current state across environments
- **Deployments**: Deployment history
- **Artifacts**: Available container images
- **Secrets**: Encrypted configuration
- **Settings**: Edit service properties

### CLI

```bash
# List all services
bahia services list

# Get service details
bahia services get payment-api

# Output as JSON
bahia services get payment-api -o json
```

### MCP Tool

```json
{
  "tool": "bahia_list_services",
  "arguments": {}
}
```

```json
{
  "tool": "bahia_get_service",
  "arguments": {
    "service_id": "svc-123"
  }
}
```

## Service State

Each service/environment combination has a **state** that tracks:

```yaml
service_id: "svc-123"
environment_id: "env-456"
desired_state:
  artifact_id: "art-789"
  desired_hash: "sha256:..."
  renderer: "compose"
  target: "payment-api-prod"
  deployed_at: "2024-01-15T10:00:00Z"
observed_state:
  artifact_id: "art-789"
  normalized_hash: "sha256:..."
  container_status: "running"
  observed_at: "2024-01-15T10:05:00Z"
drift_status: "in_sync"
```

Desired-state fields are additive. Compose/Docker deploys store a canonical desired runtime snapshot and hash; observations store normalized runtime state and hash. Public projections may include hashes, renderer, target, revision, and observation IDs, but never secret plaintext, generated Compose env-file values, raw Docker hosts, or TLS credentials.

### State Lifecycle

1. **No State** — Service exists but never deployed to this environment
2. **Deploying** — Deployment in progress
3. **Deployed** — Desired state applied, waiting for observation
4. **Healthy** — Desired matches observed
5. **Drifted** — Desired doesn't match observed

## Managed Compose Runtime Configuration

For Compose services, the Deploy wizard persists a versioned `runtime_config.managed` definition through signed `service/update`. It contains only runtime-safe, non-secret configuration:

- Compose service name, ports, command arguments, and literal environment values
- Opaque service-secret IDs mapped to environment variable names
- Semantic HTTP `GET` healthcheck settings
- Restart policy, volume mappings, and CPU/memory limits
- Pull policy

The artifact image is not typed into the runtime definition. Bahia derives an immutable `repository@sha256:digest` reference from the registered artifact selected in the wizard. Secret plaintext is likewise never accepted in managed desired state; Bahia resolves selected secret IDs server-side only during runtime apply.

The same normalized managed definition is projected by the backend into the canonical non-secret desired state used for preview, hashing, persistence, rendering, policy, and apply. Browser code does not independently calculate the desired-state hash.

Every signed `service/update` request must include `expected_updated_at` from the service revision currently displayed to the operator. Bahia locks and compares the persisted revision before publishing or storing the update; a stale revision returns JSON-RPC conflict code `-32009` without changing service state, so the client must refresh before retrying.

## Managed Public Hostnames

The Deploy wizard has a separate **Public route** step. Enable Bahia-managed HTTPS, then enter a fully qualified hostname, the container target port exposed by the signed runtime configuration, and an HTTPS health path. The final review shows the exact non-secret proxied CNAME, remote Tunnel ingress, proxy origin, TLS expectation, provider configuration hash, ordered operations, and compensation plan. Those fields are part of the desired-state hash and the signed deploy request.

Bahia rejects hostnames outside configured zones, organizations not authorized for a zone, unmanaged DNS or Tunnel collisions, ports that are not both configured and exposed, TLS passthrough, and protected-zone use from an unprotected environment. Protected zones continue through deployment approval before any app or route mutation.

The production public provider uses the Cloudflare API for the fleet's remote-managed Tunnel and proxied DNS; it does not edit connector or Cloudflare configuration files on disk. When `internal_routing` is enabled for the hostname's zone, the same signed plan also contains a versioned `internal_https` stanza and Bahia manages one ownership-marked nginx include on its host. The planned public and internal routes are non-secret desired state and participate in one signed desired-state hash. Application health is verified before publication; provider mutations are snapshot-backed and compensated on failure, and public HTTPS must return a 2xx response before the deployment succeeds.

For an already deployed service, `service/route-attach` creates a new signed intent from the current desired service specification, adds the planned route, and executes only the routing/HTTPS verification phase. It does not rebuild or reconverge the artifact or container. Protected environments still require deployment approval, and all zone, organization, origin, and port allowlists are enforced before an intent is created. See [Managed DNS and HTTPS Routes](../guides/managed-dns-and-https-routes.md) for the complete signer-first operator flow and an operational dnsmasq configuration.

Configure the backend with server-side values under `edge_routing`:

```yaml
edge_routing:
  enabled: true
  provider: cloudflare_tunnel
  backend_ref: public-edge
  api_base_url: https://api.cloudflare.com/client/v4
  api_token_ref: "<opaque Bahia secret UUID>"
  account_id: "<Cloudflare account ID>"
  tunnel_id: "<remote-managed Tunnel UUID>"
  verify_timeout: 30s
  verify_resolver: 1.1.1.1:53
  zones:
    - name: example.com
      zone_id: "<Cloudflare zone ID>"
      allowed_org_ids: ["<Bahia organization UUID>"]
      protected: true
      ttl: 1
  origins:
    - deployment_unit_id: "<Bahia deployment unit UUID>"
      host: edge-01.internal
      allowed_ports: [8080]
```

Optionally attach LAN HTTPS to the same reviewed route intent:

```yaml
internal_routing:
  enabled: true
  provider: nginx
  include_dir: /etc/nginx/conf.d
  file_prefix: bahia-
  test_command: [nginx, -t]
  reload_command: [nginx, -s, reload]
  command_env: []
  cert_file: /etc/letsencrypt/live/example.com/fullchain.pem
  key_file: /etc/letsencrypt/live/example.com/privkey.pem
  zones: [example.com]
```

`include_dir`, `cert_file`, and `key_file` must be absolute. Certificate and key presence/readability are checked by startup health rather than config parsing, so configuration can be reviewed off-host. Commands are argv arrays executed directly without a shell. `command_env` accepts `KEY=VALUE` entries that are applied to both commands; startup and apply logs expose only their keys. Unknown `internal_routing` keys fail configuration loading. The file prefix defaults to `bahia-`; test and reload default to the values shown. Bahia writes `include_dir/<prefix><hostname>.conf` atomically, refuses a pre-existing file without its ownership header, tests before reload, and restores/re-activates the exact prior file on failure. Applying a reviewed plan without `internal_https` removes an existing Bahia-owned file and reloads nginx, which makes rollback to a pre-internal-routing plan convergent without touching foreign vhosts.

### Containerized nginx

When nginx is a container on the same Docker daemon available to Bahia, configure the exact container name in direct argv form:

```yaml
test_command: [docker, exec, nginx, nginx, -t]
reload_command: [docker, exec, nginx, nginx, -s, reload]
```

For nginx on a remote daemon, put the daemon selection directly in each reviewed argv, for example `["docker", "--host", "tcp://edge-01:2375", "exec", "nginx", "nginx", "-t"]`, or keep the shorter `docker exec` argv and set `command_env: ["DOCKER_HOST=tcp://edge-01:2375"]`. The Bahia image ships `docker-cli`, and the provided Compose deployment mounts `/var/run/docker.sock`; consequently, an unqualified `docker exec` addresses the Docker daemon on Bahia's own host, not an nginx container on another host.

The complete `internal_routing` configuration, including `command_env`, is hashed into the reviewed internal-HTTPS plan. If any of it changes after review, apply rejects the stale plan with `internal routing configuration changed after review`; re-attach the route to produce and review a new plan before applying it.

The referenced credential may be a raw API token or a JSON secret containing `api_token`, `token`, or `APIToken`. It is resolved only on the Bahia server and never appears in route previews or provider errors. `verify_resolver` defaults to the public resolver `1.1.1.1:53`, ensuring managed HTTPS verification resolves and exercises the public Cloudflare edge rather than a split-horizon LAN origin; set it explicitly to `system` only to opt back into host resolver behavior. Direct runtime actions must also be enabled because public routes target an explicit Bahia-managed deployment unit.

## Service Actions

Deploy, restart, and stop are signer-first Nostr control-plane operations.

- Deploy by publishing a ContextVM `service/deploy` intent and subscribing for canonical deployment status, audit, and state events. For Compose/Docker desired-state deploys, status events include the shared step progression and state/result observables may include sanitized desired-state metadata.
- Restart or stop an adopted direct-runtime workload with ContextVM `service/restart` or `service/stop`.

Legacy Bahia request/status/result kinds and legacy REST-backed service action endpoints are migration-only and are not live service control-plane guidance.

## Service Secrets

Services can have encrypted secrets for configuration:

```bash
# Set a secret
bahia secrets set payment-api DATABASE_URL postgres://example

# List secret metadata (values hidden)
bahia secrets list payment-api

# Delete by secret ID
bahia secrets delete payment-api secret-456
```

The CLI does not register a reveal command. MCP secret writes additionally require organization `secrets:write` authorization and fail closed for cross-tenant services.

Secrets are:
- Encrypted at rest
- Available to deployment workers
- Scoped to specific environments (optional)

See [Secrets Management](#secrets-management) for details.

## Tags and Metadata

Use tags to organize services by publishing a ContextVM `service/update` intent with the updated metadata.

The CLI service list has no tag flag. Use `bahia_list_services` through MCP and filter the returned metadata in the client.

## Deleting a Service

Services can be deleted when no longer needed by publishing a ContextVM `service/delete` intent. REST `DELETE /api/v1/services/{id}` is no longer accepted for signer-first mutations.

### Web UI

1. Go to service **Settings**
2. Scroll to **Danger Zone**
3. Click **Delete Service**
4. Confirm deletion to publish the signed ContextVM intent

### Nostr

Publish a ContextVM `service/delete` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper.

**Warning**: Deleting a service removes:
- All deployment history
- Associated secrets
- State records

Artifacts and builds are **not** deleted (they may be shared).

## Canonical Observables

Service state is published as canonical Nostr observables:

| Kind | Tags | Content |
|------|------|---------|
| `30900` | `d`, `domain=service`, `schema`, `service`, optional `environment` | Current service registry and desired/observed state projections |
| `30315` | `status`, `service`, optional `environment`, correlation `e` | Operational progress and status |
| `4903` | requester `p`, resource tags, correlation `e` | Immutable audit and provenance facts |

Subscribe to these for real-time updates:

```json
{
  "kinds": [30900, 30315, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#service": ["svc-123"]
}
```

Historical `31961`/`31962` read models are startup migration inputs only.

## Best Practices

1. **Name consistently** — Use lowercase, hyphenated names (`payment-api`)
2. **Link repositories** — Enables CI integration and traceability
3. **Use tags** — Organize by team, tier, criticality
4. **Set descriptions** — Help others understand the service
5. **Scope secrets** — Use environment-specific secrets when needed

## Related

- [Environments](environments.md) — Deployment targets
- [Deployments](deployments.md) — Deploying services
- [Artifacts](artifacts.md) — Container images
- [Policies](policies.md) — Approval rules

## Managed-instance health and recovery

When supervision is enabled, Bahia observes configured targets and Bahia-managed deployment units without rebuilding images or changing configuration. Automatic recovery restarts only the exact unhealthy target, obeys desired-stopped intent, maintenance overrides, restart budgets, exponential backoff, and the shared runtime apply lock. Operators can set or clear a maintenance override through the supervisor service; these changes and recovery attempts are durable audit facts.
