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

LLM route creation is a signer-first Nostr operation (`LLMRouteCreate`, kind `5971`). Legacy REST-backed Web UI and CLI create paths are deprecated until they publish signed Nostr events directly.

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
  "request_kind": 5971,
  "result_kind": 7971,
  "route_id": "route-123"
}
```

## Creating Releases

### Web UI and CLI

LLM release registration is a signer-first Nostr operation (`LLMReleaseRegister`, kind `5972`). Legacy REST-backed Web UI and CLI release-create paths are deprecated until they publish signed Nostr events directly.

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

### Nostr (Signer-First)

Publish a `5973` LLMDeployRequest:

```json
{
  "kind": 5973,
  "content": {
    "route_id": "route-123",
    "release_id": "release-456",
    "environment_id": "env-789"
  },
  "tags": [
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

Publish a `5974` LLMDeploymentApproval:

```json
{
  "kind": 5974,
  "content": {
    "intent_id": "intent-123",
    "approved": true
  }
}
```

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

## Read Models

LLM state is published as Nostr events:

| Kind | d-tag | Content |
|------|-------|---------|
| 31964 | `route_id` | LLM route registry |
| 31965 | `route_id:environment_id` | LLM route state |

Subscribe for real-time updates:

```json
{
  "kinds": [31965],
  "#route": ["route-123"]
}
```

## Nostr Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 5971 | LLMRouteCreate | Create route request |
| 5972 | LLMReleaseRegister | Register release |
| 5973 | LLMDeployRequest | Deploy release |
| 5974 | LLMDeploymentApproval | Approve/reject |
| 5975 | LLMRollbackRequest | Rollback request |
| 6973 | LLMDeploymentStatus | Progress updates |
| 7971 | LLMRouteCreateResult | Route creation result |
| 7972 | LLMReleaseRegisterResult | Release result |
| 7973 | LLMDeploymentResult | Deploy/rollback result |

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
