# Bahia Nostr Control-Plane Events

Bahia's supported Nostr command contract is the canonical 596x request series handled by `internal/controlplane/reactor.go`. The browser and agents observe status/results/read models from the sidecar relay; they do not use SSE or request/response polling.

## Canonical Kind Families

| Family | Kinds | Direction | Purpose |
|--------|-------|-----------|---------|
| Requests | 5961–5968 | inbound | Operator/agent commands |
| Status | 6961–6962 | outbound | Progress updates |
| Results | 7961–7966 | outbound | Terminal operation results |
| Read models | 31961–31963 | outbound replaceable | Current browser/agent state |
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

All inbound requests must be valid signed Nostr events from an authorized pubkey. The sidecar verifies event ID/signature/timestamp and accepts request kinds only from `nostr.authorized_pubkeys`; Bahia services remain the final authority for business authorization.

## Common Tags

Use tags for routing and correlation so subscribers do not need to parse content to filter:

- `["service", "<service_id>"]`
- `["environment", "<environment_id>"]`
- `["artifact", "<artifact_id>"]`
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

## Replaceable Read Models

| Kind | d-tag | Description |
|------|-------|-------------|
| 31961 | `service_id:environment_id` | Current service state in an environment |
| 31962 | `service_id` | Service registry entry |
| 31963 | `environment_id` | Environment registry entry |

Read-model events are Bahia-signed projections from the database. Clients should query them, wait for EOSE, then keep the live subscription open. Latest `created_at` wins for each `(kind, pubkey, d-tag)` key. Deletions use tombstones (`deleted=true`) rather than relying on Nostr delete events.

## Legacy 311xx Bridge

Kinds 31100–31105 are deprecated compatibility commands. They are not the supported control-plane contract and new integrations must not publish them. If received, Bahia logs a deprecation warning and operators should migrate publishers to the 596x request kinds above.
