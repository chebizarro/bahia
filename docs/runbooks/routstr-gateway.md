# Routstr Gateway Runbook

This runbook connects Bahia to the fleet-managed `fleet-routstr-gateway` service. The gateway is consumed as an OpenAI-compatible `external_api` LLM backend and remains internal-only behind Bahia.

## Gateway contract

Configure Bahia against the gateway's internal base URL:

- `GET /healthz` reports process and wallet-store liveness only.
- `GET /v1/models` lists Routstr-backed OpenAI-compatible models.
- `POST /v1/chat/completions` accepts OpenAI-compatible chat completions with `Authorization: Bearer <gateway-client-token>`.
- `GET /metrics` exposes gateway operational metrics.

Per-request budget and balance failures on `POST /v1/chat/completions` return 402 or 429 responses. Do not include those conditions in `/healthz`; Bahia's `ExternalAPIProvisioner.Observe` probes `external_backend.health_url` only, so route health stays tied to gateway liveness rather than request budget state.

## Deploy the internal gateway service

Bahia does not keep a checked-in Kubernetes or Compose manifest convention for services it manages. Use the active environment's desired-state deployment path and include these service requirements:

- Internal-only service name: `fleet-routstr-gateway`.
- No public ingress.
- Persistent volume mounted at the gateway SQLite/proof store path.
- Secrets provided by the environment secret manager:
  - gateway client bearer token for Bahia callers;
  - Routstr wallet seed or encrypted wallet bootstrap material.
- Health check: `GET /healthz`.
- Metrics scrape path: `GET /metrics` on the internal service network.

The gateway's wallet float should use:

```json
{
  "provider": "routstr",
  "routstr": {
    "mode": "xcashu",
    "mint_url": "https://mint.minibits.cash/Bitcoin"
  }
}
```

## Register the LLM route and release

Use signer-first ContextVM control-plane writes as the canonical path. REST write endpoints for LLM route/release creation are not mounted in current Bahia; the REST examples below are payload shapes for deployments that still expose an operator command bridge, not the preferred path.

Set these variables before running examples:

```bash
export BAHIA_SERVICE_PUBKEY='<bahia-service-pubkey>'
export ROUTSTR_GATEWAY_URL='http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080'
export ROUTE_ID='<route-id-after-create>'
export RELEASE_ID='<release-id-after-register>'
export ENVIRONMENT_ID='<environment-id>'
```

### Create or select the Bahia route

ContextVM request content:

```json
{
  "jsonrpc": "2.0",
  "id": "routstr-route-create-1",
  "method": "llm/route-create",
  "params": {
    "name": "routstr-public-chat",
    "description": "Non-private assistant traffic through the internal Routstr gateway",
    "gateway_config": {
      "public_model": "routstr-public-chat"
    },
    "default_placement_policy": {
      "preferred_kinds": ["external_api"],
      "allow_external": true
    },
    "metadata": {
      "provider": "routstr"
    },
    "idempotency_key": "routstr-public-chat-route"
  }
}
```

Wrap it in a kind `25910` event addressed to Bahia:

```json
{
  "kind": 25910,
  "content": "<jsonrpc-content-above>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "llm/route-create"]
  ]
}
```

REST payload shape, where an operator command bridge supports it:

```json
{
  "name": "routstr-public-chat",
  "description": "Non-private assistant traffic through the internal Routstr gateway",
  "gateway_config": {
    "public_model": "routstr-public-chat"
  },
  "default_placement_policy": {
    "preferred_kinds": ["external_api"],
    "allow_external": true
  },
  "metadata": {
    "provider": "routstr"
  },
  "idempotency_key": "routstr-public-chat-route"
}
```

### Register the external_api release

ContextVM request content:

```json
{
  "jsonrpc": "2.0",
  "id": "routstr-release-register-1",
  "method": "llm/release-register",
  "params": {
    "route_id": "<route-id>",
    "version": "routstr-gateway-2026-07-09",
    "model_ref": "routstr/openai-compatible",
    "model_source": "external",
    "backend_preferences": ["external_api"],
    "external_backend": {
      "base_url": "http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080",
      "health_url": "http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080/healthz"
    },
    "placement_policy": {
      "preferred_kinds": ["external_api"],
      "allow_external": true
    },
    "metadata": {
      "provider": "routstr",
      "routstr": {
        "mode": "xcashu",
        "mint_url": "https://mint.minibits.cash/Bitcoin"
      }
    }
  }
}
```

Wrap it in a kind `25910` event addressed to Bahia:

```json
{
  "kind": 25910,
  "content": "<jsonrpc-content-above>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "llm/release-register"],
    ["route", "<route-id>"]
  ]
}
```

REST payload shape, where an operator command bridge supports it:

```json
{
  "version": "routstr-gateway-2026-07-09",
  "model_ref": "routstr/openai-compatible",
  "model_source": "external",
  "backend_preferences": ["external_api"],
  "external_backend": {
    "base_url": "http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080",
    "health_url": "http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080/healthz"
  },
  "placement_policy": {
    "preferred_kinds": ["external_api"],
    "allow_external": true
  },
  "metadata": {
    "provider": "routstr",
    "routstr": {
      "mode": "xcashu",
      "mint_url": "https://mint.minibits.cash/Bitcoin"
    }
  }
}
```

### Deploy the release

ContextVM request content:

```json
{
  "jsonrpc": "2.0",
  "id": "routstr-deploy-1",
  "method": "llm/deploy",
  "params": {
    "route_id": "<route-id>",
    "release_id": "<release-id>",
    "environment_id": "<environment-id>",
    "_meta": {
      "progressToken": "routstr-deploy-1"
    }
  }
}
```

Then follow route state and status observables scoped by route, release, and environment. The observed backend should be `external_api`, endpoint should equal the gateway base URL, and backend health should be healthy while `/healthz` returns 2xx.

## Assistant cutover for non-private workloads

Only route non-private assistant workloads through Routstr-backed external providers. Configure the assistant's agentic provider to the gateway:

```yaml
assistant:
  enabled: true
  agentic:
    enabled: true
    provider: openai_compatible
    base_url: "http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080"
    model: "<model-from-gateway-/v1/models>"
    api_key: "<gateway-client-token>"
```

Equivalent environment variables:

```bash
export BAHIA_ASSISTANT_AGENTIC_PROVIDER=openai_compatible
export BAHIA_ASSISTANT_AGENTIC_BASE_URL='http://fleet-routstr-gateway.<namespace>.svc.cluster.local:8080'
export BAHIA_ASSISTANT_AGENTIC_MODEL='<model-from-gateway-/v1/models>'
export BAHIA_ASSISTANT_AGENTIC_API_KEY='<gateway-client-token>'
```

Keep private, confidential, or operator-sensitive prompts on a private model route. The gateway sends prompts to external Routstr providers.

## Rollback

1. Disable the assistant cutover by removing the Routstr gateway `assistant.agentic.base_url`, `assistant.agentic.model`, and `assistant.agentic.api_key` overrides or by restoring the previous assistant config.
2. Roll back the LLM route to the prior release, or stop deploying the Routstr release in the target environment.
3. Stop the `fleet-routstr-gateway` service after Bahia no longer routes traffic to it.
4. Confirm route state no longer points at the gateway and `git`/Beads session closeout captures the rollback action.
