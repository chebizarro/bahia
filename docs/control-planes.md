# Bahia Control Planes

Bahia exposes deployment operations through two complementary control planes:

1. **Agent Tools API** — HTTP-based tool interface for AI agent integration
2. **Nostr Control Plane** — Event-driven deployment operations via Nostr relays

Both surfaces delegate to `RegistryService` for business logic, ensuring consistent behavior.

---

## Agent Tools API

> **Base path**: `/api/v1/agent/`

The Agent Tools API provides HTTP endpoints for AI agents to discover and invoke deployment operations. While inspired by MCP (Model Context Protocol), this is a simplified HTTP-based interface — not a true MCP JSON-RPC 2.0 implementation.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/agent/info` | Server metadata and protocol version |
| POST | `/agent/tools/list` | List available tools with schemas |
| POST | `/agent/tools/call` | Invoke a tool by name |

### Available Tools

| Tool Name | Description |
|-----------|-------------|
| `bahia_list_services` | List all registered services |
| `bahia_get_service` | Get service details by ID or name |
| `bahia_create_service` | Register a new service |
| `bahia_delete_service` | Delete a service |
| `bahia_list_environments` | List deployment environments |
| `bahia_get_environment` | Get environment details |
| `bahia_create_environment` | Create a deployment environment |
| `bahia_delete_environment` | Delete an environment |
| `bahia_list_artifacts` | List artifacts for a service |
| `bahia_get_artifact` | Get artifact details by ID |
| `bahia_deploy` | Create deployment intent |
| `bahia_rollback` | Rollback to previous artifact |
| `bahia_approve` | Approve pending deployment |
| `bahia_reject` | Reject pending deployment |
| `bahia_list_builds` | List builds for a service |
| `bahia_get_build` | Get build details |
| `bahia_list_states` | List service states in an environment |
| `bahia_list_drifted` | List services with drift detected |
| `bahia_get_observation` | Get latest runtime observation |
| `bahia_list_intents` | List deployment intents |
| `bahia_list_runs` | List deployment runs for an intent |

### Tool Call Example

```bash
curl -X POST http://localhost:8080/api/v1/agent/tools/call \
  -H "Content-Type: application/json" \
  -d '{
    "name": "bahia_deploy",
    "arguments": {
      "service_id": "550e8400-e29b-41d4-a716-446655440000",
      "environment_id": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
      "artifact_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
      "requested_by": "agent@example.com"
    }
  }'
```

### Tool Result Format

```json
{
  "content": [
    {
      "type": "text",
      "text": "{\"intent_id\": \"...\", \"status\": \"pending\"}"
    }
  ],
  "isError": false
}
```

---

## Nostr Control Plane

The Nostr control plane reactor subscribes to Nostr relay events and dispatches deployment operations. Results and status updates are published back to relays.

### Event Kind Series

The control plane uses three event kind series:

| Series | Range | Purpose |
|--------|-------|---------|
| Request | 5961–5968 | Inbound operation requests |
| Status | 6961–6969 | Progress/status updates |
| Result | 7961–7966 | Operation results |
| Registry | 31961–31963 | Replaceable state/registry events |

### Request Events (596x)

| Kind | Name | Description |
|------|------|-------------|
| 5961 | `DeployRequest` | Request to deploy an artifact |
| 5962 | `RollbackRequest` | Request to rollback a service |
| 5963 | `ServiceAction` | Lifecycle action (scale, restart, stop) |
| 5964 | `ServiceCreate` | Create a new service |
| 5965 | `EnvironmentCreate` | Create a deployment environment |
| 5966 | `DeploymentApproval` | Approve or reject a deployment |
| 5967 | `ObservationSubmit` | Submit runtime observation |
| 5968 | `DriftRemediate` | Request drift remediation |

### Status Events (696x)

| Kind | Name | Description |
|------|------|-------------|
| 6961 | `DeploymentStatus` | Deployment progress updates |
| 6962 | `ServiceStatus` | Service health/state updates |

### Result Events (796x)

| Kind | Name | Description |
|------|------|-------------|
| 7961 | `DeploymentResult` | Final deployment result |
| 7962 | `ActionResult` | Result of lifecycle action |
| 7963 | `ServiceCreateResult` | Service creation result |
| 7964 | `EnvCreateResult` | Environment creation result |
| 7965 | `ObservationResult` | Observation submission result |
| 7966 | `RemediationResult` | Drift remediation result |

### Replaceable Registry Events (3196x)

These are replaceable events (NIP-33) indexed by d-tag:

| Kind | Name | d-tag | Description |
|------|------|-------|-------------|
| 31961 | `ServiceState` | `service:env` | Current service state per environment |
| 31962 | `ServiceRegistry` | `service_id` | Service registry entry |
| 31963 | `EnvironmentRegistry` | `env_id` | Environment registry entry |

### Event Structure

#### Deploy Request (kind:5961)

```json
{
  "kind": 5961,
  "content": "{\"service_id\":\"...\",\"environment_id\":\"...\",\"artifact_id\":\"...\"}",
  "tags": [
    ["service", "<service_id>"],
    ["environment", "<environment_id>"],
    ["artifact", "<artifact_id>"]
  ]
}
```

#### Deployment Result (kind:7961)

```json
{
  "kind": 7961,
  "content": "{\"intent_id\":\"...\",\"status\":\"deployed\"}",
  "tags": [
    ["e", "<request_event_id>", "", "reply"],
    ["p", "<requester_pubkey>"],
    ["status", "success"],
    ["intent", "<intent_id>"]
  ]
}
```

#### Service State (kind:31961)

```json
{
  "kind": 31961,
  "content": "{\"service_id\":\"...\",\"environment_id\":\"...\",\"drift_status\":\"in_sync\"}",
  "tags": [
    ["d", "<service_id>:<environment_id>"],
    ["service", "<service_id>"],
    ["environment", "<environment_id>"],
    ["drift_status", "in_sync"]
  ]
}
```

---

## Authorization

### Agent Tools API

The Agent Tools API inherits authentication from the main Bahia API:
- Bearer token authentication
- Configurable via `api.auth` in config

### Nostr Control Plane

The reactor uses pubkey-based authorization:

```yaml
nostr:
  authorized_pubkeys:
    - "cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400"
    - "14907326f89ebdfc9cfdabe17bd492aa48abbd59ad5d8cc25295760bdf0e5015"
```

Events from unauthorized pubkeys are rejected with an error result.

**Security considerations**:
- Event signatures are verified before processing (`event.CheckSignature()`)
- Events are deduplicated to prevent replay attacks
- Consider using private relays for sensitive operations

---

## Example Workflows

### Deploy via Agent Tools API

```bash
# 1. List available artifacts
curl -X POST .../agent/tools/call -d '{"name":"bahia_list_artifacts","arguments":{"service_id":"..."}}'

# 2. Create deployment intent
curl -X POST .../agent/tools/call -d '{"name":"bahia_deploy","arguments":{...}}'

# 3. Approve (if protected environment)
curl -X POST .../agent/tools/call -d '{"name":"bahia_approve","arguments":{"intent_id":"..."}}'
```

### Deploy via Nostr

```javascript
// 1. Publish deploy request
const event = {
  kind: 5961,
  content: JSON.stringify({
    service_id: "...",
    environment_id: "...",
    artifact_id: "..."
  }),
  tags: [
    ["service", serviceId],
    ["environment", envId],
    ["artifact", artifactId]
  ]
};
await relay.publish(event);

// 2. Subscribe for result
const sub = relay.sub([{
  kinds: [7961],
  "#e": [event.id]
}]);
sub.on('event', (result) => {
  console.log('Deployment result:', result.content);
});
```

### Monitor Drift via Nostr

```javascript
// Subscribe to replaceable state events for a service
const sub = relay.sub([{
  kinds: [31961],
  "#service": [serviceId]
}]);
sub.on('event', (state) => {
  const data = JSON.parse(state.content);
  if (data.drift_status === 'drifted') {
    console.log('Drift detected!');
  }
});
```

---

## Deprecated Event Kinds

The `311xx` series in `internal/adapters/nostr/publisher.go` is deprecated:

| Deprecated | Replacement |
|------------|-------------|
| 31102 `KindCmdIntentCreate` | 5961 `KindDeployRequest` |
| 31103 `KindCmdIntentApprove` | 5966 `KindDeploymentApproval` |
| 31104 `KindCmdIntentReject` | 5966 `KindDeploymentApproval` |
| 31105 `KindCmdRollbackRequest` | 5962 `KindRollbackRequest` |

New implementations should use the 596x/696x/796x series.
