# Bahia Nostr Control-Plane Events

Bahia's supported Nostr control-plane contract is the canonical 596x/597x/598x public event surface handled by `internal/controlplane/reactor.go`, together with the related 696x/796x status-result kinds and 3196x read models. The browser and agents observe status/results/read models from the sidecar relay; they do not use SSE or request/response polling.

## Canonical Kind Families

| Family | Kinds | Direction | Purpose |
|--------|-------|-----------|---------|
| Service requests | 5961–5968 | inbound | Service/environment operator commands |
| LLM requests | 5971–5975 | inbound | LLM route/release/deploy/approval/rollback commands |
| Tool provisioning / approval loop | 5976, 5977, 6976, 7976, 7977 | mixed | Agent request, Bahia→operator approval handoff, progress, final result, and operator response |
| Adoption requests | 5978–5979 | inbound | Adoption/import operator commands |
| Public compatibility writes | 5981–5989 | inbound | Service/environment update-delete, artifact register, and policy commands |
| Encrypted requests | 5980 | inbound | Encrypted browser request envelope |
| Service status | 6961–6963 | outbound | Service/action progress updates |
| LLM status | 6973 | outbound | LLM deployment/rollback progress updates |
| Adoption status | 6978 | outbound | Adoption progress |
| Service results | 7961–7966 | outbound | Service terminal operation results |
| LLM results | 7971–7973 | outbound | LLM route/release/deployment terminal results |
| Adoption results | 7978–7979 | outbound | Adoption terminal results |
| Encrypted results | 7980 | outbound | Encrypted browser terminal result envelope |
| Read models | 31961–31970 | outbound replaceable | Current browser/agent state |
| Audit/activity | 31000–31099 | outbound append-only | Recent activity feed |

## Request Kinds

| Kind | Name | Description |
|------|------|-------------|
| 5961 | `DeployRequest` | Deploy an artifact to an environment |
| 5962 | `RollbackRequest` | Roll back a service/environment |
| 5963 | `ServiceAction` | Lifecycle action such as restart/stop |
| 5964 | `ServiceCreate` | Register a service |
| 5965 | `EnvironmentCreate` | Register an environment |
| 5966 | `DeploymentApproval` | Approve or reject an intent |
| 5967 | `ObservationSubmit` | Submit runtime observation |
| 5968 | `DriftRemediate` | Request drift remediation |
| 5971 | `LLMRouteCreate` | Create an LLM route registry entry |
| 5972 | `LLMReleaseRegister` | Register an immutable LLM release |
| 5973 | `LLMDeployRequest` | Deploy an LLM release to an environment |
| 5974 | `LLMDeploymentApproval` | Approve or reject an LLM deployment intent |
| 5975 | `LLMRollbackRequest` | Roll back an LLM route/environment |
| 5976 | `ToolProvisionRequest` | Agent → Bahia tool provisioning workflow request |
| 5977 | `ToolApprovalRequest` | Bahia → operator approval handoff event for tool provisioning |
| 5978 | `AdoptionScanRequest` | Request adoption scan previews |
| 5979 | `AdoptionImportRequest` | Request adoption import |
| 5980 | `EncryptedRequest` | Encrypted browser request envelope |
| 5981 | `ServiceUpdate` | Update a service registry entry |
| 5982 | `ServiceDelete` | Delete a service registry entry |
| 5983 | `EnvironmentUpdate` | Update an environment registry entry |
| 5984 | `EnvironmentDelete` | Delete an environment registry entry |
| 5985 | `ArtifactRegister` | Register an artifact |
| 5986 | `PolicyCreate` | Create a deployment policy |
| 5987 | `PolicyUpdate` | Update a deployment policy |
| 5988 | `PolicyDelete` | Delete a deployment policy |
| 5989 | `PolicyEvaluate` | Evaluate a deployment policy |

Public inbound operator-authored events must be valid signed Nostr events from an authorized pubkey. That includes the normal request/write families (`5961`-`5968`, `5971`-`5976`, `5978`-`5979`, `5981`-`5989`) plus the operator-authored `7977` tool approval response. `5977` is Bahia-authored outbound, not an inbound operator request. `5980` encrypted requests must be sent only to encrypted-request relays, not to the public sidecar. Bahia services remain the final authority for business authorization after relay-side validation.

## Common Tags

Use tags for routing and correlation so subscribers do not need to parse content to filter:

- `["service", "<service_id>"]`
- `["environment", "<environment_id>"]`
- `["artifact", "<artifact_id>"]`
- `["route", "<llm_route_id>"]`
- `["release", "<llm_release_id>"]`
- `["intent", "<intent_id>"]`
- `["run", "<run_id>"]`
- `["e", "<request_event_id>", "", "reply"]` on status/result replies
- `["p", "<requester_pubkey>"]` on status/result replies
- `["status", "..."]` and `["step", "..."]` on progress events

## Example Deploy Request

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

## LLM Request Examples

### Create LLM Route (Kind 5971)

```json
{
  "kind": 5971,
  "content": "{\"name\":\"chat\",\"description\":\"chat completions\",\"gateway_config\":{\"public_model\":\"chat\"}}",
  "tags": [
    ["route", "<optional-client-route-id>"],
    ["model", "chat"]
  ]
}
```

### Register LLM Release (Kind 5972)

```json
{
  "kind": 5972,
  "content": "{\"route_id\":\"...\",\"version\":\"v1\",\"model_ref\":\"hf/org/model\",\"model_source\":\"huggingface\"}",
  "tags": [
    ["route", "<route_id>"],
    ["model", "hf/org/model"]
  ]
}
```

### Deploy LLM Release (Kind 5973)

```json
{
  "kind": 5973,
  "content": "{\"route_id\":\"...\",\"environment_id\":\"...\",\"release_id\":\"...\",\"requested_by\":\"operator\"}",
  "tags": [
    ["route", "<route_id>"],
    ["environment", "<environment_id>"],
    ["release", "<release_id>"]
  ]
}
```

### Approve or Reject LLM Deployment (Kind 5974)

```json
{
  "kind": 5974,
  "content": "{\"intent_id\":\"...\",\"decision\":\"approve\"}",
  "tags": [
    ["intent", "<intent_id>"],
    ["decision", "approve"]
  ]
}
```

### Roll Back LLM Route (Kind 5975)

```json
{
  "kind": 5975,
  "content": "{\"route_id\":\"...\",\"environment_id\":\"...\",\"requested_by\":\"operator\"}",
  "tags": [
    ["route", "<route_id>"],
    ["environment", "<environment_id>"]
  ]
}
```

LLM status/result replies use kind `6973` and `7973` for deploy/approval/rollback and include `route`, `release` when known, `environment`, `intent`, `run`, `e`, and `p` tags. Route create and release register terminal replies use `7971` and `7972`.

MCP LLM tools publish these canonical request events and return `request_event_id`, `request_kind`, `status_kind`, `result_kind`, `registry_kind`, `state_kind`, and resource IDs. Agents should use those fields to subscribe to relay updates; do not poll REST for completion.

## Replaceable Read Models

| Kind | d-tag | Description |
|------|-------|-------------|
| 31961 | `service_id:environment_id` | Current service state in an environment |
| 31962 | `service_id` | Service registry entry |
| 31963 | `environment_id` | Environment registry entry |
| 31964 | `route_id` | LLM route registry entry |
| 31965 | `route_id:environment_id` | Current LLM route state in an environment |
| 31966 | `artifact_id` | Artifact registry entry |
| 31967 | `intent_id` | Deployment intent registry entry |
| 31968 | `run_id` | Deployment run registry entry |
| 31969 | `build_id` | Build registry entry |
| 31970 | `policy_id` | Policy registry entry |

Read-model events are Bahia-signed projections from the database. Clients should query them, wait for EOSE, then keep the live subscription open. Latest `created_at` wins for each `(kind, pubkey, d-tag)` key. Deletions use tombstones (`deleted=true`) rather than relying on Nostr delete events.

`7977` is not a normal Bahia result event. It is the operator's signed response back to Bahia after a prior `5977` approval handoff.

## Encrypted request/result note

Kind `5980` requests and kind `7980` results are used for sensitive browser-facing operations such as notifications, org/member flows, payments history, secrets, and selected log/signature actions. These requests should be sent only to encrypted-request relays, not to the public relay sidecar.

## Legacy 311xx Bridge

Kinds 31100–31105 are deprecated compatibility commands. They are not the supported control-plane contract and new integrations must not publish them. If received, Bahia logs a deprecation warning and operators should migrate publishers to the 596x request kinds above.
