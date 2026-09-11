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
2. Click **Create Environment**
3. Select the owning organization, enter the required environment name, and review the optional worker placement, deployment-unit, strategy (**Rolling** maps to `replace`, **Blue-Green** to `blue_green`, **Canary** to `canary`), and protection settings
4. Click **Create** to publish the signed Nostr command

### Nostr

Publish a ContextVM `environment/create` request as kind `25910` or inside an encrypted `1059`/`21059` wrapper.

## Environment Properties

| Property | Description | Required |
|----------|-------------|----------|
| `id` | Environment UUID | Update only |
| `expected_updated_at` | Revision from the latest environment read; required when update supplies `deployment_units` | Complete-set update only |
| `org_id` | Organization UUID; authorization is checked before any tenant mutation | Create only |
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

Runtime targets are represented by `targeting` plus `deployment_units`; there is no `runtime_target` property and `loom` is not a deployment-unit runtime type. Server-side validation accepts `docker`, `compose`, `kubernetes`, `podman`, `vm-firecracker`, or `vm-qemu`; units otherwise follow `schemas/deployment_unit.json`.

> **NOTE (2026-09-11):** `schemas/deployment_unit.json` still enumerates only `docker`, `compose`, `kubernetes`, and `podman`; the VM runtime types are accepted by `internal/domain/validate.go` and advertised by the CLI.

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

Use the environment or unit reconcile mode to control automated drift remediation. Deployment approval is required for every intent in a `protected` environment; there are no `requires_approval` or `approvers` environment properties. Deployment [policies](policies.md) add block/warn gates on top.

## Viewing Environments

### Web UI

The **Environments** page lists environments with **Name**, **Strategy**, **Protected**, and **ID** columns, plus **Create Environment**.

Click an environment to see:
- **Deployment Units**: Explicit and implicit runtime boundaries, safe endpoint aliases, Compose directories, ownership, and reconcile modes
- **Worker Placement Policy** and **Runtime Configuration**
- **Deployed Services**: Service state in this environment
- **Deployment History**: Deployment activity
- Header **Edit** and **Delete** actions

Authorized signers can create or edit an explicit Compose unit from the **Deployment Units** section. The browser publishes one revision-guarded `environment/update` containing the complete explicit set; it does not call a deployment-unit CRUD endpoint. Protected environments require a confirmation step and do not permit `auto_apply` from this editor. Endpoint references are aliases only—Docker hosts, TLS files, and credentials are never resolved or displayed in the browser.

### CLI

```bash
# List environments
bahia environments list

# Get environment details, including explicit units or the marked implicit default
bahia environments get <environment-id>

# Create or update through signed ContextVM mutations
bahia environments create --name production --units-file units.json
bahia environments update <environment-id> --units-file units.json

# Manage one unit through signed read-merge, complete-set signed updates
bahia environments units list <environment-id>
bahia environments units create <environment-id> --file unit.json --default-unit-key max
bahia environments units update <environment-id> max --file unit.json --default-unit-key max
```

### MCP Tool

```json
{
  "tool": "bahia_list_environments",
  "arguments": {}
}
```

## Environment State

Query the current state of all service/environment pairs (the command takes no environment filter; use `-o json` and filter client-side, or open the environment detail page):

```bash
bahia state list
```

Output:
```
SERVICE                               ENVIRONMENT                           DRIFT
6f1c…                                 2b9e…                                 in_sync
```

### Drifted Services

Find services that have drifted:

```bash
bahia state drifted
```

See [Environment States](environment-states.md) for the web view.

## Updating Environments

Environment updates are signer-first ContextVM intents. REST `PUT /api/v1/environments/{id}` is no longer accepted.

### Web UI

1. Go to the environment detail page
2. Click **Edit** for environment properties, or use **Deployment Units** to create/edit an explicit Compose target
3. For a target, review the unit key, server-managed endpoint alias, dedicated Compose directory, Bahia-managed ownership, execution mode, and reconcile mode
4. Click **Save Unit** (or **Review Protected Change**, then **Sign Target Update**, for protected environments) to publish the signed ContextVM intent

If the canonical environment revision changes while a target draft is open, Bahia disables submission and requires the operator to reload and review instead of silently rebasing the signed full set.

### Nostr

Publish a ContextVM `environment/update` request with `id` and only the fields to change. If `deployment_units` is present, it is the complete desired explicit set, not a patch, and `expected_updated_at` is required. The `environments units` CLI commands obtain the environment, targeting, `updated_at`, and resolved units through the authorized signed `environment/get-details` method; no REST authorization is required and there is no automatic HTTP fallback. The CLI retries a stale complete-set write by rereading through that signed method and remerging up to three attempts; it then reports the conflict.

## Deleting Environments

Environments can be deleted when no longer needed by publishing a ContextVM `environment/delete` intent. REST `DELETE /api/v1/environments/{id}` is no longer accepted for signer-first mutations.

**Warning**: Without `force`, deletion is refused while the environment has any dependent deployment intents or service state records. With `force: true`, those dependents are cascade-deleted. Stop or remove services first unless you intend to cascade.

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
2. **Protect production** — Mark it `protected` so every deployment requires approval
3. **Mirror production in staging** — Catch issues before they hit prod
4. **Configure notifications** — Alert on deployment failures

## Related

- [Services](services.md) — Applications to deploy
- [Environment States](environment-states.md) — Compare desired and observed state
- [Deployments](deployments.md) — Deployment workflows
- [Policies](policies.md) — Approval rules
- [Notifications](notifications.md) — Alert channels
