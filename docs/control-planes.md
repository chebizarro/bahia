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
- Encrypted-request relay URLs and Loom relays remain explicitly configured for their own traffic and are not used for Bahia read-model publication.

This avoids duplicate event loops: Bahia publishes canonical 696x/796x/3196x/read-model traffic to the sidecar pool only, while optional upstream mirroring is isolated behind the sidecar boundary.

---

## Nostr Control Plane

The Nostr reactor subscribes to signed request events and publishes status, terminal results, and replaceable read models. Service registry operations delegate to `RegistryService`; adoption scan/import delegates to `AdoptionService`; direct-runtime `deploy|restart|stop` delegates to `RuntimeLifecycleService`; LLM route/release/deploy/approval/rollback operations delegate to `LLMRegistryService`. LLM deploy, adoption/import, and direct-runtime actions are Nostr-first async actions; REST is only a narrowed registry/query/compatibility surface.

| Series | Range | Purpose |
|--------|-------|---------|
| Service requests | 5961–5968 | Inbound service/environment operation requests |
| LLM requests | 5971–5975 | Inbound LLM route/release/deploy/approval/rollback requests |
| Adoption requests | 5978–5979 | Inbound adoption scan/import operator requests |
| Encrypted requests | 5980 | Browser → Bahia encrypted request-domain request |
| Service/action status | 6961–6963 | Service deployment/action progress/status updates |
| LLM status | 6973 | LLM deployment/rollback progress updates |
| Adoption status | 6978 | Adoption scan/import progress updates |
| Service results | 7961–7966 | Service terminal operation results |
| LLM results | 7971–7973 | LLM route/release/deployment terminal results |
| Adoption results | 7978–7979 | Adoption scan/import terminal results |
| Encrypted results | 7980 | Bahia → Browser encrypted request-domain result |
| Registry/read models | 31961–31965 | Replaceable browser/agent read models |

### Request Events (596x)

| Kind | Name | Description |
|------|------|-------------|
| 5961 | `DeployRequest` | Request to deploy an artifact |
| 5962 | `RollbackRequest` | Request to roll back a service |
| 5963 | `ServiceAction` | Lifecycle action; signer-first direct-runtime `deploy`, `restart`, `stop` |
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
| 5978 | `AdoptionScanRequest` | Request adoption previews for managed endpoint targets |
| 5979 | `AdoptionImportRequest` | Request adoption import for managed endpoint targets |

### Status and Result Events

| Kind | Name | Description |
|------|------|-------------|
| 6961 | `DeploymentStatus` | Service deployment progress |
| 6962 | `ServiceStatus` | Service health/state updates |
| 6963 | `ActionStatus` | Direct-runtime service action progress |
| 6973 | `LLMDeploymentStatus` | LLM deployment/rollback progress |
| 6978 | `AdoptionStatus` | Adoption scan/import progress |
| 7961 | `DeploymentResult` | Service deployment terminal result |
| 7962 | `ActionResult` | Service action terminal result |
| 7963 | `ServiceCreateResult` | Service creation terminal result |
| 7964 | `EnvironmentCreateResult` | Environment creation terminal result |
| 7965 | `ObservationResult` | Observation submission terminal result |
| 7966 | `RemediationResult` | Drift remediation terminal result |
| 7971 | `LLMRouteCreateResult` | LLM route creation terminal result |
| 7972 | `LLMReleaseRegisterResult` | LLM release registration terminal result |
| 7973 | `LLMDeploymentResult` | LLM deploy/approval/rollback terminal result |
| 7978 | `AdoptionScanResult` | Adoption scan terminal result |
| 7979 | `AdoptionImportResult` | Adoption import terminal result |

### Replaceable Read Models

| Kind | Name | d-tag | Description |
|------|------|-------|-------------|
| 31961 | `ServiceState` | `service_id:environment_id` | Current desired/observed service state |
| 31962 | `ServiceRegistry` | `service_id` | Service registry entry |
| 31963 | `EnvironmentRegistry` | `environment_id` | Environment registry entry |
| 31964 | `LLMRouteRegistry` | `route_id` | LLM route registry entry |
| 31965 | `LLMRouteState` | `route_id:environment_id` | Current desired/observed LLM route state |

### Signer-First Operator Actions

Operator workflows are public signed control-plane requests. They are not RPC and must be consumed as event streams: publish the request, subscribe for `e=<request_event_id>` replies, process `696x`/`697x` status events as progress, and treat the corresponding `796x`/`797x` result event as terminal. Clients should not poll or use timeout-based completion; use EOSE for historical catch-up and keep the subscription open for realtime replies.

CLI behavior:

- `bahia adopt scan|import` and `bahia services actions deploy|restart|stop` use signer-first Nostr requests by default.
- Relay resolution is deterministic: repeatable `--relay` flags, then comma-separated `BAHIA_NOSTR_RELAYS`, then `/api/v1/system/info` discovery from `nostr.browser_relays` plus `nostr.sidecar_url`.
- Live status chatter is written to stderr only in table mode; JSON/YAML stdout remains reserved for the final result payload.
- `--http-fallback` (or `BAHIA_OPERATOR_HTTP_FALLBACK=true`) is explicit compatibility mode and is only safe before any relay accepts the signed request, such as signer/relay discovery failure or publish with zero accepted relays.
- `--raw-target` is compatibility-only. It skips the public signer-first adoption path and requires explicit `--http-fallback`; use `--target` endpoint refs for the signer-first path.

Authorization uses event pubkeys only:

- `nostr.authorized_pubkeys` is the global fallback for all public operator requests.
- `adoption.allowed_pubkeys` additionally authorizes `5978`/`5979` adoption requests.
- `direct_runtime_actions.allowed_pubkeys` additionally authorizes direct-runtime `5963` requests.
- Subject/email operator allowlists remain HTTP/NIP-98 compatibility settings and are ignored by signer-first public events.

#### Adoption scan/import (`5978`/`5979`)

Adoption requests are public relay-visible content, so targets must reference server-managed runtime endpoints. Raw Docker transport material is forbidden.

Scan request content:

```json
{
  "targets": [
    {
      "name": "prod",
      "endpoint_ref": "prod-docker",
      "environment_name": "prod"
    }
  ]
}
```

Import request content:

```json
{
  "targets": [{ "name": "prod", "endpoint_ref": "prod-docker" }],
  "import_all": true,
  "selections": [
    {
      "target_name": "prod",
      "container_id": "abc123",
      "service_name_override": "api"
    }
  ]
}
```

Rules:

- `targets` is required and non-empty.
- Each target requires normalized `name` and non-empty `endpoint_ref`.
- `docker_host` is rejected on the public signer-first path.
- Import requires `import_all=true` or at least one `selection`.

Progress is published as `6978 AdoptionStatus` with `status=processing`, `operation=scan|import`, repeated `target`, `endpoint_ref`, and optional `environment_name` tags. Terminal results are:

- `7978 AdoptionScanResult` with content `[]AdoptionPreviewResponse`.
- `7979 AdoptionImportResult` with content `[]AdoptionImportResultResponse`.

Both result payloads reuse the HTTP-safe DTO projection: only safe env/labels are included, redacted key names are preserved, and managed endpoint `docker_host` values are omitted.

#### Direct-runtime actions (`5963`)

Signer-first direct-runtime actions reuse `5963 ServiceAction` with JSON content:

```json
{
  "action": "deploy",
  "service_id": "...",
  "environment_id": "...",
  "artifact_id": "..."
}
```

Rules:

- `action` must be one of `deploy`, `restart`, or `stop`.
- `service_id` and `environment_id` are required UUIDs.
- `artifact_id` is optional for `deploy` and invalid for `restart`/`stop`.
- Existing non-direct-runtime `5963` tag-based actions remain compatibility acknowledgements.

Progress is published as `6963 ActionStatus` with `status=processing`, `step=executing`, `action`, `service`, `environment`, and optional `artifact` tags. Success publishes `7962 ActionResult` with content `RuntimeActionResponse`, including the runtime observation when available. Failures publish `7962 ActionResult` with `status=failed`, `action`, resource tags, and error content.

### Encrypted Request/Result Events (5980/7980)

Sensitive browser route families and encrypted request-domain actions (notifications, orgs, payments, service secrets, stored deployment run logs, and artifact signature verification) use encrypted request/result events instead of public read models. These events are intentionally **not** accepted by the public relay sidecar policy and must be sent only to operator-configured relay URLs for encrypted request/result traffic.

Discovery/config contract:

- Backend-only relay URLs for encrypted request/result handling are configured as `nostr.encrypted_request_relays` and are not exposed by `/api/v1/system/info`.
- Browser-discoverable relay URLs for encrypted request/result handling are configured as `nostr.browser_encrypted_request_relays` and are exposed as `nostr.browser_encrypted_request_relays`.
- `/api/v1/system/info.features.encrypted_nostr_requests=true` means the backend has a service key, at least one backend `nostr.encrypted_request_relays` subscription target, and at least one browser encrypted-request relay URL advertised.
- Browser clients must keep public `nostr.browser_relays` / `nostr.sidecar_url` separate from `nostr.browser_encrypted_request_relays`; sensitive payloads must never be published to the public sidecar relay.

Event contract:

- Request kind: `5980`; result kind: `7980`.
- Request cleartext tags are limited to routing/correlation metadata such as `p=<service_pubkey>` and `encrypted=bahia-encrypted-v1`.
- Request `content` is NIP-44 encrypted to the Bahia service pubkey and contains `{version, operation, requester_pubkey, payload}`.
- Result tags include `e=<request_event_id>` with reply marker, `p=<requester_pubkey>`, `encrypted=bahia-encrypted-v1`, and terminal `status`.
- Result `content` is NIP-44 encrypted to the requester pubkey and contains `{version, request_event_id, status, payload?, error?}`.
- Backend handlers reject unauthorized requesters before decrypting/dispatching domain operations, publish encrypted terminal errors for decrypt/validation failures, and deduplicate by event id.

Browser signer support:

- NIP-07 is supported only when `window.nostr.nip44.encrypt/decrypt` are available.
- NIP-46 can participate only if the provider explicitly exposes `provider.nip44.encrypt/decrypt`; NIP-46's internal encrypted RPC channel does not by itself give the web app NIP-44 conversation-key operations. If absent, encrypted request/result route migration is blocked for that signer mode and the UI/tests should surface that exact blocker.

Encrypted operation catalog:

The following operation names are normative for the `5980`/`7980` encrypted request/result family. New encrypted browser-facing operations must be added here when introduced so the documented contract stays aligned with the implementation.

Notification encrypted operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `notifications.channels.list` | `{}` | `{channels}` | Channel configs are encrypted in transit; webhook `config.secret` is omitted from results. |
| `notifications.channels.get` | `{id}` | `{channel}` | Returns one sanitized channel or an encrypted terminal error. |
| `notifications.channels.create` | channel fields | `{channel}` | Webhook secrets are accepted only as encrypted write payloads. |
| `notifications.channels.update` | `{id, ...fields}` | `{channel}` | Omitted webhook secrets preserve the stored secret; returned channel is sanitized. |
| `notifications.channels.delete` | `{id}` | `{status,id}` | Deletes the channel over encrypted request/result events. |
| `notifications.channels.test` | `{id}` | `{status,id}` | Dispatches directly to the selected channel and returns terminal success/error. |
| `notifications.logs.list` | `{limit?,channel_id?}` | `{logs}` | Delivery logs and payloads are returned only in encrypted result content. |

Encrypted domain operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `payments.history` | `{worker,limit?}` | `PaymentRecord[]` | `worker` is required; `limit` defaults to 50 and is capped at 250. |
| `orgs.list` | `{}` | `({org fields..., role})[]` | Returns orgs visible to the requester pubkey with the caller's role attached to each row. |
| `orgs.detail` | `{id}` | `{org,members,invites,my_role}` | `id` may be an org UUID or org name; `invites` is populated only when the requester has admin access. |
| `orgs.create` | `{name,display_name?}` | `Organization` | Creates an organization for an authorized requester and returns the created org object directly. |
| `orgs.delete` | `{id}` | `{message}` | Deletes the organization when the requester is authorized. |
| `orgs.my_invites` | `{}` | `InviteWithOrg[]` | Returns invites for the requester pubkey enriched with org name/display name. |
| `orgs.accept_invite` | `{invite_id}` | `OrgMember` | Accepts an org invite for the requester pubkey and returns the created membership directly. |
| `orgs.create_invite` | `{org_id,pubkey,role?,expires_in?}` | `OrgInvite` | `role` defaults to `viewer`; `expires_in` is in hours and defaults to 72. |
| `orgs.revoke_invite` | `{org_id,invite_id}` | `{message}` | Revokes an existing invite. |
| `orgs.update_member_role` | `{org_id,pubkey,role}` | `{message}` | Updates member role state through encrypted transport. |
| `orgs.remove_member` | `{org_id,pubkey}` | `{message}` | Removes a member from the org. |

Encrypted route operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `services.secrets.list` | `{service_id}` | `{secrets,total}` | Returns secret refs only; plaintext/encrypted values are omitted. |
| `services.secrets.create` | `{service_id,name,value,environment_id?,encryption_method?}` | `{secret,status}` | Secret value is encrypted in the request and at rest; result contains metadata only. |
| `services.secrets.update` | `{service_id,secret_id,value,encryption_method?}` | `{secret,status}` | Re-encrypts the new value; result contains metadata only. |
| `services.secrets.delete` | `{service_id,secret_id}` | `{status,secret_id}` | Validates the secret belongs to the service before deletion. |
| `services.secrets.reveal` | `{service_id,secret_id}` | `{secret,value}` | Plaintext is returned only in the encrypted result for explicit reveal actions. |
| `deployments.run_logs.get` | `{run_id,tail?,stream?}` | `{logs,stream}` | Stored stdout/stderr snapshots are encrypted result content; public run projections carry metadata only. |
| `artifacts.signatures.verify` | `{artifact_id}` | `{found,stored,verified,discovered,rejected,errors,signatures}` | Verification is triggered by encrypted signed requests and stores discovered signature records. |

### Correlation Tags

Use tags for relay-side filtering and MCP follow-up subscriptions. Service flows use `service`, `environment`, `artifact`, `intent`, and `run`. LLM flows use `route`, `release`, `environment`, `intent`, and `run`. Status/result replies also include `e` with marker `reply`, `p` for the requester pubkey, plus `status` and `step` where applicable. Encrypted result replies use the same `e`/`p` pattern but keep payloads encrypted. MCP async LLM tools return the request event id and the relevant request/status/result/read-model kind ids so clients can subscribe directly rather than polling.

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
