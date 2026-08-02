# Environments

An **Environment** is a deployment target in Bahia — such as development, staging, or production.

## Overview

Environments define:
- Where services get deployed
- What approval policies apply
- Which runtime targets execute deployments
- How notifications are routed

## Creating an Environment

Environment creation is signer-first. Bahia no longer accepts REST `POST /api/v1/environments`; clients publish a ContextVM JSON-RPC `environment/create` intent as Nostr kind `25910`, usually wrapped with CEP-4/NIP-59 `1059` or `21059`. The response acknowledges receipt only; durable environment state, status, and audit facts come from canonical `30900`, `30315`, and `4903` observables.

### Web UI

1. Navigate to **Environments** in the sidebar
2. Click **New Environment**
3. Enter the required environment name and any supported targeting, deployment-unit, strategy, or protection settings
4. Click **Create** to publish the signed Nostr command

### Nostr

Publish a ContextVM `environment/create` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper.

## Environment Properties

| Property | Description | Required |
|----------|-------------|----------|
| `id` | Environment UUID | Update only |
| `expected_updated_at` | Revision from the latest environment read; required when update supplies `deployment_units` | Complete-set update only |
| `org_id` | Organization UUID; authorization is checked when creating or moving an environment | No |
| `name` | Display name | Create only |
| `loom_worker_selector` | Legacy/non-Compose worker-selection object | No |
| `runtime_config` | Environment-level runtime compatibility settings | No |
| `targeting` | Typed `default_unit_key`, failure-domain labels, secret scope, and default reconcile policy | No |
| `reconcile_mode` | `observe_only`, `auto_apply`, `approval_required`, or `disabled` | No |
| `deployment_units` | Complete desired explicit deployment-unit set | No |
| `deploy_strategy` | `replace`, `blue_green`, or `canary` | No |
| `protected` | Enables additional deployment protections | No |

For `environment/update`, omitted fields remain unchanged. `deployment_units` is special: omission preserves the current set, a supplied array replaces the complete explicit set atomically, and `[]` returns the environment to an implicit default unit. A request that supplies `deployment_units` must include `expected_updated_at` from the latest read; stale revisions fail closed with ContextVM code `-32009` and no registry mutation. If explicit units are supplied, `targeting.default_unit_key` must name one of them.

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

Runtime targets are represented by `targeting` plus `deployment_units`; there is no `runtime_target` property and `loom` is not a deployment-unit runtime type. Each unit accepts `docker`, `compose`, `kubernetes`, or `podman` and follows the schema in `schemas/deployment_unit.json`.

```json
{
  "name": "production",
  "targeting": {
    "default_unit_key": "max",
    "failure_domain_labels": {"host": "max"},
    "secret_scope_mode": "unit",
    "default_reconcile_mode": "approval_required"
  },
  "deployment_units": [
    {
      "key": "max",
      "display_name": "Max Compose",
      "runtime_type": "compose",
      "endpoint_ref": "max",
      "compose_dir": "/srv/bahia/compose/gastown",
      "network_profile": {},
      "ownership_mode": "bahia_managed",
      "reconcile_mode": "approval_required",
      "runtime_config": {"execution_mode": "sdk"}
    }
  ],
  "deploy_strategy": "replace",
  "protected": true
}
```

`endpoint_ref` names a server-managed endpoint alias; callers do not put raw Docker credentials in the signed payload. `compose_dir` is the Bahia-owned full-project directory on that endpoint. Non-Compose workloads can use `loom_worker_selector` for worker selection.

## Approval Policies

Use the environment or unit reconcile mode to control automated drift remediation. Deployment approval requirements are defined by deployment policies rather than stale `requires_approval` or `approvers` environment properties. See [Policies](policies.md) for details.

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

# Get environment details, including explicit units or the marked implicit default
bahia environments get <environment-id>

# Create or update through signed ContextVM mutations
bahia environments create --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json

# Manage one unit through read-merge, complete-set signed updates
bahia environments units list <environment-id>
bahia environments units create <environment-id> --file unit.json --default-unit-key max
bahia environments units update <environment-id> max --file unit.json --default-unit-key max

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

Environment updates are signer-first ContextVM intents. REST `PUT /api/v1/environments/{id}` is no longer accepted.

### Web UI

1. Go to the environment detail page
2. Click **Settings**
3. Modify properties
4. Click **Save** to publish the signed ContextVM intent

### Nostr

Publish a ContextVM `environment/update` request with `id` and only the fields to change. If `deployment_units` is present, it is the complete desired explicit set, not a patch, and `expected_updated_at` is required. The CLI retries a stale complete-set write by rereading and remerging up to three signed attempts; it then reports the conflict.

## Deleting Environments

Environments can be deleted when no longer needed by publishing a ContextVM `environment/delete` intent. REST `DELETE /api/v1/environments/{id}` is no longer accepted for signer-first mutations.

**Warning**: You cannot delete an environment that has:
- Active deployments
- Running services
- Pending intents

Remove or stop all services first.

## Canonical Observables

Environment state is published as canonical Nostr observables:

| Kind | Tags | Content |
|------|------|---------|
| `30900` | `d`, `domain=environment` or `domain=service`, `schema`, `environment`, optional `service` | Environment registry and service/environment state projections |
| `30315` | `status`, `environment`, optional `service`, correlation `e` | Operational status and progress |
| `4903` | requester `p`, resource tags, correlation `e` | Immutable audit facts |

Historical `31961`/`31963` read models are startup migration inputs only.

## Best Practices

1. **Use consistent naming** — `dev`, `staging`, `prod` or `development`, `staging`, `production`
2. **Require approval for production** — Prevent accidental deployments
3. **Mirror production in staging** — Catch issues before they hit prod
4. **Document purpose** — Help team members understand each environment
5. **Configure notifications** — Alert on deployment failures

## Related

- [Services](services.md) — Applications to deploy
- [Environment States](environment-states.md) — Compare desired and observed state
- [Deployments](deployments.md) — Deployment workflows
- [Policies](policies.md) — Approval rules
- [Notifications](notifications.md) — Alert channels
