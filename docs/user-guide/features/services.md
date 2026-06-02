# Services

A **Service** represents an application you deploy with Bahia — a web API, background worker, scheduled job, or any containerized workload.

## Overview

Services are the primary organizational unit in Bahia. Each service:
- Has a unique name and optional repository link
- Can be deployed to multiple environments
- Tracks builds, artifacts, and deployment history
- Has associated secrets, policies, and notifications

## Creating a Service

Service creation is signer-first. Bahia no longer accepts REST `POST /api/v1/services`; clients publish a signed Nostr `5964` `ServiceCreate` command to the control-plane relay and subscribe for the correlated `7963` result/read-model update.

### Web UI

1. Navigate to **Services** in the sidebar
2. Click **New Service**
3. Fill in the form:
   - **Name**: Unique identifier (e.g., `payment-api`)
   - **Display Name**: Human-friendly name (optional)
   - **Repository**: Git repository URL
   - **Description**: What the service does
4. Click **Create** to publish the signed Nostr command

### Nostr (Signer-First)

Publish a `5964` ServiceCreate event:

```json
{
  "kind": 5964,
  "content": {
    "name": "payment-api",
    "repository": "https://github.com/company/payment-api"
  },
  "tags": [
    ["t", "bahia"],
    ["t", "service-create"]
  ]
}
```

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
  deployed_at: "2024-01-15T10:00:00Z"
observed_state:
  artifact_id: "art-789"
  container_status: "running"
  observed_at: "2024-01-15T10:05:00Z"
drift: false
```

### State Lifecycle

1. **No State** — Service exists but never deployed to this environment
2. **Deploying** — Deployment in progress
3. **Deployed** — Desired state applied, waiting for observation
4. **Healthy** — Desired matches observed
5. **Drifted** — Desired doesn't match observed

## Service Actions

Deploy, restart, and stop are signer-first Nostr control-plane operations.

- Deploy by publishing a signed `DeployRequest` event (`kind:5961`) and subscribing for the deployment result/read-model events.
- Restart or stop an adopted direct-runtime workload by publishing a signed `ServiceAction` event (`kind:5963`) with the action payload.

Legacy REST-backed service action endpoints and direct-runtime CLI action paths are deprecated until the CLI publishes signed Nostr events directly.

## Service Secrets

Services can have encrypted secrets for configuration:

```bash
# Create a secret
bahia services secrets create payment-api \
  --name "DATABASE_URL" \
  --value "postgres://..."

# List secrets (values hidden)
bahia services secrets list payment-api

# Reveal a secret value
bahia services secrets reveal payment-api --name "DATABASE_URL"
```

Secrets are:
- Encrypted at rest
- Available to deployment workers
- Scoped to specific environments (optional)

See [Secrets Management](#secrets-management) for details.

## Tags and Metadata

Use tags to organize services by publishing a signed `5981` ServiceUpdate command with the updated metadata.

Query services by tag:

```bash
bahia services list --tag team=payments
```

## Deleting a Service

Services can be deleted when no longer needed by publishing a signed `5982` ServiceDelete event. REST `DELETE /api/v1/services/{id}` is no longer accepted.

### Web UI

1. Go to service **Settings**
2. Scroll to **Danger Zone**
3. Click **Delete Service**
4. Confirm deletion to publish the signed Nostr command

### Nostr

Publish a `5982` ServiceDelete event.

**Warning**: Deleting a service removes:
- All deployment history
- Associated secrets
- State records

Artifacts and builds are **not** deleted (they may be shared).

## Read Models

Service state is published as Nostr read models:

| Kind | d-tag | Content |
|------|-------|---------|
| 31962 | `service_id` | Service registry entry |
| 31961 | `service_id:environment_id` | Current desired/observed state |

Subscribe to these for real-time updates:

```json
{
  "kinds": [31962],
  "authors": ["<bahia-service-pubkey>"],
  "#d": ["svc-123"]
}
```

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
