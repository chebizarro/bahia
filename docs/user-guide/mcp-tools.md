# MCP Tools Reference

Bahia exposes tools via the **Model Context Protocol (MCP)** for AI agent integration.

## Overview

MCP provides:
- **Tool discovery** — List available tools
- **Tool invocation** — Execute operations
- **Resource access** — Read DNS/FIPS state
- **Nostr correlation** — Follow async operations

## Endpoints

| Path | Description |
|------|-------------|
| `/mcp` | Primary MCP endpoint |
| `/api/v1/mcp` | Alternate endpoint |

## Protocol

MCP uses **JSON-RPC 2.0** over HTTP. Bahia's Nostr-native migration uses the same method model over ContextVM: client mutations are JSON-RPC requests on kind `25910` rather than legacy Bahia request kinds.

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Discovery

### List Tools

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/list"
}
```

### List Resources

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "resources/list"
}
```

## Tool Categories

### Service Tools

| Tool | Description |
|------|-------------|
| `bahia_list_services` | List all services |
| `bahia_get_service` | Get service details |
| `bahia_service_create` | Create a service |
| `bahia_service_update` | Update a service |
| `bahia_service_delete` | Delete a service |

### Environment Tools

| Tool | Description |
|------|-------------|
| `bahia_list_environments` | List environments |
| `bahia_get_environment` | Get environment details |
| `bahia_environment_create` | Create environment |

### Deployment Tools

| Tool | Description |
|------|-------------|
| `bahia_deploy` | Create deployment intent |
| `bahia_rollback` | Rollback to previous |
| `bahia_list_intents` | List deployment intents |
| `bahia_get_intent` | Get intent details |
| `bahia_approve_intent` | Approve deployment |
| `bahia_reject_intent` | Reject deployment |
| `bahia_list_runs` | List deployment runs |
| `bahia_get_run` | Get run details |
| `bahia_get_run_logs` | Get run logs (encrypted) |

### Artifact Tools

| Tool | Description |
|------|-------------|
| `bahia_list_artifacts` | List artifacts |
| `bahia_get_artifact` | Get artifact details |
| `bahia_artifact_register` | Register artifact |
| `bahia_get_sbom` | Get artifact SBOM |
| `bahia_verify_signatures` | Verify signatures |

### LLM Tools

| Tool | Description |
|------|-------------|
| `bahia_llm_list_routes` | List LLM routes |
| `bahia_llm_get_route` | Get route details |
| `bahia_llm_route_create` | Create LLM route |
| `bahia_llm_release_register` | Register release |
| `bahia_llm_deploy` | Deploy LLM release |
| `bahia_llm_approve` | Approve deployment |
| `bahia_llm_rollback` | Rollback LLM route |
| `bahia_llm_list_state` | List LLM state |

### ML Tools

| Tool | Description |
|------|-------------|
| `bahia_ml_model_import` | Import ML model |
| `bahia_ml_recipe_run` | Run ML recipe |
| `bahia_ml_inference_deploy` | Deploy inference |
| `bahia_ml_inference_rollback` | Rollback inference |
| `bahia_ml_list_models` | List models |
| `bahia_ml_list_endpoints` | List endpoints |

### Soul Factory Tools

| Tool | Description |
|------|-------------|
| `soul_factory_list_souls` | List souls |
| `soul_factory_get_soul` | Get soul details |
| `soul_factory_provision` | Provision new soul |
| `soul_factory_action` | Lifecycle action |
| `soul_factory_regenerate` | Regenerate soul |

### Worker Tools

| Tool | Description | ContextVM method |
|------|-------------|------------------|
| `bahia_list_workers` | List workers from canonical read models (`30900`/`30078`) | read-only resource |
| `bahia_get_worker` | Get worker details from canonical read models | read-only resource |
| `bahia_worker_drain` | Drain worker | `worker/drain` |
| `bahia_worker_resume` | Resume worker | `worker/undrain` |
| `bahia_worker_cordon` | Cordon worker | `worker/cordon` |
| `bahia_worker_uncordon` | Uncordon worker | `worker/uncordon` |

### Backup Tools

| Tool | Description |
|------|-------------|
| `bahia_backup_definition_apply` | Apply backup def |
| `bahia_backup_policy_apply` | Apply backup policy |
| `bahia_backup_run` | Trigger backup |
| `bahia_backup_verify` | Verify backup |
| `bahia_backup_restore` | Restore backup |

### DNS Tools

| Tool | Description |
|------|-------------|
| `bahia_dns_list_zones` | List DNS zones |
| `bahia_dns_list_endpoints` | List DNS endpoints |
| `bahia_dns_zone_create` | Create zone |
| `bahia_dns_policy_apply` | Apply DNS policy |

### Package Tools

| Tool | Description |
|------|-------------|
| `bahia_package_repository_apply` | Apply repository |
| `bahia_package_repository_delete` | Delete repository |
| `bahia_package_publish` | Publish package |
| `bahia_package_promote` | Promote package |
| `bahia_package_yank` | Yank package |
| `bahia_package_drift_detect` | Detect drift |

### Policy Tools

Policy mutation tools use the ContextVM method surface for writes and canonical observable projections for reads. Deletion follows NIP-09 kind `5` where relay-level deletion semantics apply, with canonical tombstone projections where domain state requires durable delete markers.

| Tool | Description |
|------|-------------|
| `bahia_list_policies` | List policies |
| `bahia_get_policy` | Get policy details |

### Payment Tools

| Tool | Description |
|------|-------------|
| `bahia_estimate_cost` | Estimate run cost |
| `bahia_get_run_cost` | Get actual cost |
| `bahia_payment_history` | Get history (encrypted) |

### Notification Tools

| Tool | Description |
|------|-------------|
| `bahia_list_notification_channels` | List channels |
| `bahia_create_notification_channel` | Create channel |
| `bahia_update_notification_channel` | Update channel |
| `bahia_delete_notification_channel` | Delete channel |
| `bahia_test_notification_channel` | Test channel |

### Organization Tools

| Tool | Description |
|------|-------------|
| `bahia_org_list` | List orgs (encrypted) |
| `bahia_org_create` | Create org (encrypted) |
| `bahia_org_detail` | Get org detail |
| `bahia_org_invite` | Create invite |

## Tool Invocation

### Synchronous Tools

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "name": "bahia_list_services",
    "arguments": {}
  }
}
```

Response:
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "[{\"id\":\"svc-123\",\"name\":\"payment-api\"}]"
      }
    ]
  }
}
```

### Async Tools (Nostr-Backed)

Long-running tools return correlation metadata:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "tools/call",
  "params": {
    "name": "bahia_deploy",
    "arguments": {
      "service_id": "svc-123",
      "environment_id": "env-456",
      "artifact_id": "art-789"
    }
  }
}
```

Response includes Nostr correlation:
```json
{
  "result": {
    "content": [{
      "type": "text",
      "text": "{\"request_event_id\":\"abc...\",\"method\":\"service/deploy\",\"request_kind\":25910,\"observable_kinds\":[30900,4903,30315]}"
    }]
  }
}
```

Use these to subscribe for ContextVM responses and canonical observable completion.

## Resources

### DNS Endpoints

```json
{
  "uri": "bahia://dns/endpoint/payment-api.prod.example.com",
  "name": "payment-api (prod)",
  "description": "Payment API endpoint",
  "mimeType": "application/json",
  "metadata": {
    "protocol": "https",
    "address": "10.0.1.100",
    "port": 8080
  }
}
```

### FIPS Mesh Nodes

```json
{
  "uri": "bahia://fips/node/node-123",
  "name": "fips-node-123",
  "metadata": {
    "status": "online"
  }
}
```

### Reading Resources

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "resources/read",
  "params": {
    "uri": "bahia://dns/endpoint/payment-api.prod.example.com"
  }
}
```

## Authentication

### With NIP-98

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Nostr <base64-signed-event>" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{...}}'
```

### Without Auth (Development)

When `auth.enabled=false`:
```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{...}}'
```

## ContextVM Nostr Transport

ContextVM clients discover Bahia with kind `11316` server announcements and capability announcements `11317`-`11320`, then invoke the same JSON-RPC methods over Nostr kind `25910` with CEP-4/NIP-59 gift-wrap (`1059` or `21059`) where supported. Long-running tool responses are acknowledgments; agents should follow canonical observable kinds `30900`, `4903`, `30315`, NIP-51 `30002`, NIP-78 `30078`, standard NIPs, and NIP-09 kind `5` deletions where applicable for final state.

Production runtime no longer exposes legacy Bahia request/status/result kinds as the live MCP transport. Legacy kind references are migration fixtures only; new MCP clients should require ContextVM method names and canonical observables.

## Error Handling

### Tool Error Response

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "content": [{
      "type": "text",
      "text": "{\"error\":\"service not found\"}"
    }],
    "isError": true
  }
}
```

### JSON-RPC Error

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "error": {
    "code": -32602,
    "message": "Invalid params"
  }
}
```

## Best Practices

1. **List tools first** — Discover available capabilities
2. **Follow async operations** — Use Nostr correlation
3. **Handle errors** — Check `isError` field
4. **Use typed arguments** — Match expected schema
5. **Authenticate for production** — Use NIP-98

## Related

- [Nostr Integration](nostr-integration.md) — Event model
- [Control Planes](../control-planes.md) — Full specification
- [API Reference](../api.md) — HTTP API
