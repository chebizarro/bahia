# Bahia Control Planes

Bahia's supported control-plane contract is now sidecar-first and Nostr-native:

1. **Nostr relay sidecar** — primary async/realtime plane for browser state, operator requests, agent progress, and read models.
2. **Native MCP JSON-RPC** — synchronous tool discovery/invocation at `/mcp` and `/api/v1/mcp`.
3. **REST API** — narrowed CRUD/query/log surface protected by direct NIP-98 when auth is enabled; Bearer credentials are not accepted.

Removed legacy surfaces:

- `GET /api/v1/events/stream` dashboard SSE stream
- `POST /api/v1/auth/nostr` NIP-98-to-JWT browser exchange
- `/api/v1/agent/*` custom MCP-inspired HTTP tools

`/api/v1/system/info` keeps `nostr_auth_exchange`, `legacy_sse`, `legacy_jwt_exchange`, and `legacy_agent_http` keys as `false` values so old clients can fail closed.

---

## Native MCP Transport

> **Base paths**: `/mcp` and `/api/v1/mcp`

MCP clients use JSON-RPC 2.0 over HTTP. Tool implementations are backed by `internal/mcp/server.go`; long-running tool results include Nostr correlation metadata (`request_event_id`, `request_kind`, `service_id`, `route_id`, `release_id`, `environment_id`, `intent_id`, `run_id`, status/result/read-model kinds) so agents can follow async truth on the relay. `/api/v1/system/info` advertises the same contract under `control_plane` for clients that need kind discovery before subscribing.

Example:

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc":"2.0",
    "id":2,
    "method":"tools/call",
    "params": {
      "name": "bahia_deploy",
      "arguments": {
        "service_id": "...",
        "environment_id": "...",
        "artifact_id": "..."
      }
    }
  }'
```

---

## Nostr Sidecar Topology

Browser and Bahia control-plane traffic should target the relay sidecar first.

- Browser discovery: `/api/v1/system/info` → `nostr.browser_relays` / `nostr.sidecar_url`
- Bahia backend connection: `nostr.sidecar.backend_url` when set, otherwise `nostr.sidecar.public_url`
- Bahia-owned control-plane reactor/projector traffic uses only the sidecar backend URL in sidecar mode.
- Upstream relays: configure `nostr.relays` for public interop/audit traffic. If `nostr.sidecar.mirror_external=true`, Bahia treats the sidecar as the upstream mirror boundary and does not also connect directly to those URLs.
- Private and Loom relays remain explicitly configured for their own traffic and are not used for Bahia read-model publication.

This avoids duplicate event loops: Bahia publishes canonical 696x/796x/3196x/read-model traffic to the sidecar pool only, while optional upstream mirroring is isolated behind the sidecar boundary.

---

## Nostr Control Plane

The Nostr reactor subscribes to signed request events and publishes status, terminal results, and replaceable read models. Service operations delegate to `RegistryService`; LLM route/release/deploy/approval/rollback operations delegate to `LLMRegistryService`. LLM deploy, approval, and rollback are Nostr-first async actions; REST is only a narrowed registry/query/compatibility surface.

| Series | Range | Purpose |
|--------|-------|---------|
| Service requests | 5961–5968 | Inbound service/environment operation requests |
| LLM requests | 5971–5975 | Inbound LLM route/release/deploy/approval/rollback requests |
| Private requests | 5980 | Browser → Bahia encrypted private-domain request |
| Service status | 6961–6962 | Service progress/status updates |
| LLM status | 6973 | LLM deployment/rollback progress updates |
| Service results | 7961–7966 | Service terminal operation results |
| LLM results | 7971–7973 | LLM route/release/deployment terminal results |
| Private results | 7980 | Bahia → Browser encrypted private-domain result |
| Registry/read models | 31961–31965 | Replaceable browser/agent read models |

### Request Events (596x)

| Kind | Name | Description |
|------|------|-------------|
| 5961 | `DeployRequest` | Request to deploy an artifact |
| 5962 | `RollbackRequest` | Request to roll back a service |
| 5963 | `ServiceAction` | Lifecycle action |
| 5964 | `ServiceCreate` | Create a service |
| 5965 | `EnvironmentCreate` | Create an environment |
| 5966 | `DeploymentApproval` | Approve/reject a deployment |
| 5967 | `ObservationSubmit` | Submit runtime observation |
| 5968 | `DriftRemediate` | Request drift remediation |
| 5971 | `LLMRouteCreate` | Create an LLM route registry entry |
| 5972 | `LLMReleaseRegister` | Register an immutable LLM route release |
| 5973 | `LLMDeployRequest` | Request LLM route deployment |
| 5974 | `LLMDeploymentApproval` | Approve/reject an LLM deployment intent |
| 5975 | `LLMRollbackRequest` | Request LLM route rollback |

### Status and Result Events

| Kind | Name | Description |
|------|------|-------------|
| 6961 | `DeploymentStatus` | Service deployment progress |
| 6962 | `ServiceStatus` | Service health/state updates |
| 6973 | `LLMDeploymentStatus` | LLM deployment/rollback progress |
| 7961 | `DeploymentResult` | Service deployment terminal result |
| 7962 | `ActionResult` | Service action terminal result |
| 7963 | `ServiceCreateResult` | Service creation terminal result |
| 7964 | `EnvironmentCreateResult` | Environment creation terminal result |
| 7965 | `ObservationResult` | Observation submission terminal result |
| 7966 | `RemediationResult` | Drift remediation terminal result |
| 7971 | `LLMRouteCreateResult` | LLM route creation terminal result |
| 7972 | `LLMReleaseRegisterResult` | LLM release registration terminal result |
| 7973 | `LLMDeploymentResult` | LLM deploy/approval/rollback terminal result |

### Replaceable Read Models

| Kind | Name | d-tag | Description |
|------|------|-------|-------------|
| 31961 | `ServiceState` | `service_id:environment_id` | Current desired/observed service state |
| 31962 | `ServiceRegistry` | `service_id` | Service registry entry |
| 31963 | `EnvironmentRegistry` | `environment_id` | Environment registry entry |
| 31964 | `LLMRouteRegistry` | `route_id` | LLM route registry entry |
| 31965 | `LLMRouteState` | `route_id:environment_id` | Current desired/observed LLM route state |

### Private Encrypted Transport (5980/7980)

Sensitive browser route families (notifications, orgs, payments, and future private-domain migrations) use encrypted request/result events instead of public read models. These events are intentionally **not** accepted by the public relay sidecar policy and must be sent only to operator-configured private relays.

Discovery/config contract:

- Backend-only private relay URLs remain in `nostr.private_relays` and are not exposed by `/api/v1/system/info`.
- Browser-discoverable private relay URLs must be configured separately as `nostr.private_browser_relays` and are exposed as `nostr.private_browser_relays` only when explicitly set.
- `/api/v1/system/info.features.private_nostr_transport=true` means the backend has a service key, at least one backend `nostr.private_relays` subscription target, and at least one browser-private relay advertised.
- Browser clients must keep public `nostr.browser_relays` / `nostr.sidecar_url` separate from `nostr.private_browser_relays`; sensitive payloads must never be published to the public sidecar relay.

Event contract:

- Request kind: `5980`; result kind: `7980`.
- Request cleartext tags are limited to routing/correlation metadata such as `p=<service_pubkey>` and `private=bahia-private-v1`.
- Request `content` is NIP-44 encrypted to the Bahia service pubkey and contains `{version, operation, requester_pubkey, payload}`.
- Result tags include `e=<request_event_id>` with reply marker, `p=<requester_pubkey>`, `private=bahia-private-v1`, and terminal `status`.
- Result `content` is NIP-44 encrypted to the requester pubkey and contains `{version, request_event_id, status, payload?, error?}`.
- Backend handlers reject unauthorized requesters before decrypting/dispatching domain operations, publish encrypted terminal errors for decrypt/validation failures, and deduplicate by event id.

Browser signer support:

- NIP-07 is supported only when `window.nostr.nip44.encrypt/decrypt` are available.
- NIP-46 can participate only if the provider explicitly exposes `provider.nip44.encrypt/decrypt`; NIP-46's internal encrypted RPC channel does not by itself give the web app NIP-44 conversation-key operations. If absent, private route migration is blocked for that signer mode and the UI/tests should surface that exact blocker.

Notification private operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `notifications.channels.list` | `{}` | `{channels}` | Channel configs are encrypted in transit; webhook `config.secret` is omitted from results. |
| `notifications.channels.get` | `{id}` | `{channel}` | Returns one sanitized channel or an encrypted terminal error. |
| `notifications.channels.create` | channel fields | `{channel}` | Webhook secrets are accepted only as encrypted write payloads. |
| `notifications.channels.update` | `{id, ...fields}` | `{channel}` | Omitted webhook secrets preserve the stored secret; returned channel is sanitized. |
| `notifications.channels.delete` | `{id}` | `{status,id}` | Deletes the channel over private transport. |
| `notifications.channels.test` | `{id}` | `{status,id}` | Dispatches directly to the selected channel and returns terminal success/error. |
| `notifications.logs.list` | `{limit?,channel_id?}` | `{logs}` | Delivery logs and payloads are returned only in encrypted result content. |

### Correlation Tags

Use tags for relay-side filtering and MCP follow-up subscriptions. Service flows use `service`, `environment`, `artifact`, `intent`, and `run`. LLM flows use `route`, `release`, `environment`, `intent`, and `run`. Status/result replies also include `e` with marker `reply`, `p` for the requester pubkey, plus `status` and `step` where applicable. Private result replies use the same `e`/`p` pattern but keep payloads encrypted. MCP async LLM tools return the request event id and the relevant request/status/result/read-model kind ids so clients can subscribe directly rather than polling.

Clients should wait for EOSE on bootstrap queries, then keep subscriptions open for live updates. Deduplicate by event id; for replaceable events, latest `created_at` wins for `(kind, pubkey, d-tag)`. Deletions use tombstone content/tags (`deleted=true`), not Nostr delete events.

---

## Authorization

- **Signer-first browser identity**: web sessions are signer-first (NIP-07 or NIP-46), with signer pubkey as the primary user identity.
- **Nostr requests**: event signatures are verified and request kinds require authorized operator pubkeys.
- **Control-plane operator allowlist**: `nostr.authorized_pubkeys` is for control-plane/operator request authorization only.
- **Tenant bootstrap owner allowlist**: `auth.bootstrap_owner_pubkeys` governs who may create organizations over REST compatibility endpoints when configured.
- **REST and MCP HTTP**: use direct NIP-98 (`Authorization: Nostr <base64event>`) when auth is enabled. `Authorization: Bearer ...` is rejected with `401` rather than treated as a fallback.
- **REST role in architecture**: REST is a compatibility transport for narrowed CRUD/query surfaces that have not yet moved to Nostr-native flows.

---

## Deprecated / Quarantined Event Kinds

The legacy 311xx command bridge is deprecated and logs warnings when received. New integrations must use the canonical 596x request kinds.

| Deprecated | Replacement |
|------------|-------------|
| 31102 `KindCmdIntentCreate` | 5961 `KindDeployRequest` |
| 31103/31104 approval/rejection | 5966 `KindDeploymentApproval` |
| 31105 rollback | 5962 `KindRollbackRequest` |
