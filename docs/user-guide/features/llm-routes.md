# LLM Routes

**LLM Routes** in Bahia manage and deploy large language model inference endpoints.

## Overview

LLM Routes provide:
- **Model registry** — Track LLM versions and configurations
- **Release management** — Immutable versioned releases
- **Deployment workflow** — Deploy with approvals
- **State tracking** — Monitor active deployments

## Key Concepts

### Route

A **Route** is an LLM inference endpoint definition:

```yaml
name: "gpt4-proxy"
description: "GPT-4 API proxy route"
model_family: "openai"
```

### Release

A **Release** is an immutable, versioned configuration:

```yaml
route_id: "route-123"
version: "v1.2.0"
config:
  model: "gpt-4-turbo"
  max_tokens: 4096
  temperature: 0.7
```

### Deployment

Deploying a release to an environment makes it live.

## Creating LLM Routes

### Web UI and CLI

LLM route creation is a signer-first ContextVM operation. Clients publish `llm/route-create` as Nostr kind `25910`, usually inside encrypted `1059`/`21059` when the payload is sensitive, and follow canonical observables for durable truth. Transitional REST `POST /api/v1/llm/routes` is available when a control-plane command publisher is configured; it requires an org-scoped caller with `llm_routes:write`, publishes the signed `llm/route-create` command, verifies relay `OK` acceptance, and returns a `202` command receipt rather than a synchronous route domain object.

### MCP Tool

```json
{
  "tool": "bahia_llm_route_create",
  "arguments": {
    "name": "gpt4-proxy",
    "model_family": "openai"
  }
}
```

Returns Nostr correlation metadata:
```json
{
  "request_event_id": "...",
  "method": "llm/route-create",
  "observable_kinds": [30900, 30315, 4903],
  "route_id": "route-123"
}
```

## Creating Releases

### Web UI and CLI

LLM release registration is a signer-first ContextVM operation. Use `llm/release-register` and follow `30900`, `30315`, and `4903` observables scoped by route and release.

### MCP Tool

```json
{
  "tool": "bahia_llm_release_register",
  "arguments": {
    "route_id": "route-123",
    "version": "v1.2.0",
    "config": {
      "model": "gpt-4-turbo",
      "max_tokens": 4096
    }
  }
}
```

## Deploying LLM Routes

### Web UI

1. Go to route detail
2. Select a release
3. Click **Deploy**
4. Choose environment
5. Click **Create Deployment**

### CLI

```bash
bahia llm deploy \
  --route-id route-123 \
  --release-id release-456 \
  --environment production
```

### MCP Tool

```json
{
  "tool": "bahia_llm_deploy",
  "arguments": {
    "route_id": "route-123",
    "release_id": "release-456",
    "environment_id": "env-789"
  }
}
```

### Nostr (ContextVM)

Publish a ContextVM `llm/deploy` request:

```json
{
  "kind": 25910,
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"llm-deploy-route-123-env-789\",\"method\":\"llm/deploy\",\"params\":{\"route_id\":\"route-123\",\"release_id\":\"release-456\",\"environment_id\":\"env-789\",\"_meta\":{\"progressToken\":\"llm-deploy-route-123-env-789\"}}}",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "llm/deploy"],
    ["route", "route-123"],
    ["release", "release-456"],
    ["environment", "env-789"]
  ]
}
```

## Approving LLM Deployments

If the environment requires approval:

### Web UI

1. Go to **LLM** → **Pending Approvals**
2. Review deployment details
3. Click **Approve** or **Reject**

### CLI

```bash
bahia llm approve intent-123
bahia llm reject intent-123 --reason "Config issue"
```

### Nostr

Publish a ContextVM `llm/approve` or `llm/reject` request and follow canonical observables scoped by `intent`.

## Rolling Back

### Web UI

1. Go to route detail
2. Find previous successful deployment
3. Click **Rollback to this release**

### CLI

```bash
bahia llm rollback \
  --route-id route-123 \
  --environment production
```

### MCP Tool

```json
{
  "tool": "bahia_llm_rollback",
  "arguments": {
    "route_id": "route-123",
    "environment_id": "env-789"
  }
}
```

## Viewing LLM State

### Current State

```bash
bahia llm state list
bahia llm state list --environment production
```

### Drifted Routes

```bash
bahia llm state drifted
```

### Route Detail

```bash
bahia llm routes get route-123
bahia llm releases list --route-id route-123
```

## Canonical Observables

LLM state is published as canonical Nostr observables:

| Kind | Tags | Content |
|------|------|---------|
| `30900` | `d`, `domain=llm`, `schema`, `route`, optional `environment`/`release` | Route registry, release, and route/environment state projections |
| `30315` | `status`, `route`, optional `environment`/`release`/`intent`, correlation `e` | Deployment progress and operational status |
| `4903` | requester `p`, resource tags, correlation `e` | Audit, approval, and provenance facts |

Subscribe for real-time updates:

```json
{
  "kinds": [30900, 30315, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#route": ["route-123"]
}
```

## Nostr Methods and Kinds

| Surface | Contract | Description |
|---------|----------|-------------|
| Mutation intent | ContextVM `25910` (`1059`/`21059` when encrypted) | `llm/route-create`, `llm/release-register`, `llm/deploy`, `llm/approve`, `llm/reject`, `llm/rollback` |
| Observable state | `30900` | Route, release, and route/environment projections |
| Observable status/audit | `30315`, `4903` | Progress, approvals, terminal facts, and provenance |

Historical `5971`-`5975`, `6973`, `7971`-`7973`, and `31964`/`31965` events are startup migration inputs only.

## Best Practices

1. **Version releases semantically** — Use semver (v1.2.3)
2. **Document changes** — Note what changed in each release
3. **Test before production** — Deploy to staging first
4. **Monitor after deployment** — Check for errors
5. **Use approvals for production** — Prevent accidental deploys

## Troubleshooting

### Route Not Deploying

- Check release exists
- Verify environment ID
- Check approval status

### Drift Detected

- Worker may have restarted
- Check runtime connectivity
- Manual intervention may be needed

## Related

- [Environments](environments.md) — Deployment targets
- [Workers](workers.md) — LLM execution hosts
- [ML Models](ml-models.md) — Generic AI/ML models
