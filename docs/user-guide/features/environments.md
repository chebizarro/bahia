# Environments

An **Environment** is a deployment target in Bahia — such as development, staging, or production.

## Overview

Environments define:
- Where services get deployed
- What approval policies apply
- Which runtime targets execute deployments
- How notifications are routed

## Creating an Environment

Environment creation is signer-first. Bahia no longer accepts REST `POST /api/v1/environments`; clients publish a signed Nostr `5965` `EnvironmentCreate` command to the control-plane relay and subscribe for the correlated `7964` result/read-model update.

### Web UI

1. Navigate to **Environments** in the sidebar
2. Click **New Environment**
3. Fill in:
   - **Name**: Human-readable name (e.g., "Production")
   - **Slug**: URL-safe identifier (e.g., `production`)
   - **Description**: Purpose of this environment
4. Click **Create** to publish the signed Nostr command

### Nostr

Publish a `5965` EnvironmentCreate event.

## Environment Properties

| Property | Description | Required |
|----------|-------------|----------|
| `name` | Display name | Yes |
| `slug` | URL-safe identifier | Yes |
| `description` | Environment purpose | No |
| `requires_approval` | Require deployment approval | No |
| `auto_deploy` | Auto-deploy on new artifacts | No |
| `runtime_target` | Deployment execution target | No |

## Environment Types

### Development

For local or shared development:
- Auto-deploy enabled
- No approval required
- Frequent deployments expected

### Staging

For pre-production testing:
- May require approval
- Mirrors production configuration
- Used for QA and integration testing

### Production

For live traffic:
- Approval typically required
- Strict change control
- Monitored closely

## Runtime Targets

Environments connect to runtime targets for deployment execution:

### Kubernetes

```yaml
runtime_target:
  type: kubernetes
  config:
    context: prod-cluster
    namespace: default
```

### Docker

```yaml
runtime_target:
  type: docker
  config:
    endpoint_ref: prod-docker
```

### Compose

```yaml
runtime_target:
  type: compose
  config:
    project_name: myapp
    compose_file: docker-compose.prod.yml
```

### Loom Workers

Bahia delegates to Loom workers for actual deployment:

```yaml
runtime_target:
  type: loom
  config:
    worker_pubkeys:
      - "npub1worker..."
```

## Approval Policies

Environments can require deployment approval:

### Manual Approval

```yaml
requires_approval: true
approvers:
  - "npub1admin1..."
  - "npub1admin2..."
```

### Policy-Based Approval

Link to deployment policies:

```yaml
policies:
  - "policy-require-tests"
  - "policy-sbom-check"
```

See [Policies](policies.md) for details.

## Viewing Environments

### Web UI

The **Environments** page shows:
- All environments with descriptions
- Service count per environment
- Health summary

Click an environment to see:
- **Services**: Services deployed here
- **State**: Current state of all services
- **History**: Deployment activity
- **Settings**: Edit environment

### CLI

```bash
# List environments
bahia environments list

# Get environment details
bahia environments get production

# List state for an environment
bahia state list --environment production
```

### MCP Tool

```json
{
  "tool": "bahia_list_environments",
  "arguments": {}
}
```

## Environment State

Query the current state of all services in an environment:

```bash
bahia state list --environment production
```

Output:
```
SERVICE        ARTIFACT    STATUS    DRIFT
payment-api    v2.1.0     healthy   no
user-api       v1.5.2     healthy   no
notification   v3.0.1     drifted   yes
```

### Drifted Services

Find services that have drifted:

```bash
bahia state drifted --environment production
```

## Environment Variables

Configure environment-level defaults:

```yaml
environment_variables:
  LOG_LEVEL: "info"
  ENABLE_TRACING: "true"
```

These are available to all services in the environment.

## Updating Environments

Environment updates are signer-first. REST `PUT /api/v1/environments/{id}` is no longer accepted.

### Web UI

1. Go to the environment detail page
2. Click **Settings**
3. Modify properties
4. Click **Save** to publish the signed Nostr command

### Nostr

Publish a `5983` EnvironmentUpdate event.

## Deleting Environments

Environments can be deleted when no longer needed by publishing a signed `5984` EnvironmentDelete event. REST `DELETE /api/v1/environments/{id}` is no longer accepted.

**Warning**: You cannot delete an environment that has:
- Active deployments
- Running services
- Pending intents

Remove or stop all services first.

## Read Models

Environment state is published as Nostr read models:

| Kind | d-tag | Content |
|------|-------|---------|
| 31963 | `environment_id` | Environment registry entry |
| 31961 | `service_id:environment_id` | Service state in environment |

## Best Practices

1. **Use consistent naming** — `dev`, `staging`, `prod` or `development`, `staging`, `production`
2. **Require approval for production** — Prevent accidental deployments
3. **Mirror production in staging** — Catch issues before they hit prod
4. **Document purpose** — Help team members understand each environment
5. **Configure notifications** — Alert on deployment failures

## Related

- [Services](services.md) — Applications to deploy
- [Deployments](deployments.md) — Deployment workflows
- [Policies](policies.md) — Approval rules
- [Notifications](notifications.md) — Alert channels
