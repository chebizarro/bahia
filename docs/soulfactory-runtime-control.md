# SoulFactory Runtime Control Contract

> Source plan: [`docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`](plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md)

This document defines the shared `soulfactory.*` runtime control contract for the Bahia-owned OpenClaw sidecar/control-driver path and Metiq Go implementations. It is the schema source for bridge work and tests; implementation must not fork field names or error shapes by runtime.

## Event kinds

- `30317` — runtime capability announcement.
- `38384` — runtime control request, signed by a trusted SoulFactory controller key.
- `38386` — runtime control result, signed by the target runtime key.

Bahia-facing UX remains on `31952` drafts, `5950` provisioning requests, `1950` lifecycle action requests, `6950` progress, `7950` terminal results, and `31951` soul read models. Runtime completion is translated back to `6950/7950`; legacy `KindSoulAction + 1` results are migration aliases only.

## Required `38384` tags

Every runtime control request MUST include:

| Tag | Value | Purpose |
| --- | --- | --- |
| `p` | target runtime pubkey | NIP-01 addressed runtime target. |
| `method` | one `soulfactory.*` method | Dispatch key. |
| `e` | original operator request event id | Correlates to `5950` or `1950`. |
| `soul` | soul id or `31951` coordinate | Soul read-model correlation. |
| `agent-id` | stable agent id | Runtime-local binding key. |
| `controller` | SoulFactory controller pubkey | Trust/audit identity. |
| `idempotency-key` | stable deterministic key | Replay safety. |
| `spec-hash` | hash of resolved desired spec | Draft/runtime consistency. |
| `schema` | `soulfactory-runtime-control/v1` | Compatibility gate. |

Recommended tags: `capability` (`30317` event id or coordinate), `request-kind` (`5950` or `1950`), `action`, `relay` hints, and `t` task/correlation ids.

## Request content envelope

`38384.content` MUST be JSON:

```json
{
  "schema": "soulfactory-runtime-control/v1",
  "method": "soulfactory.provision",
  "idempotency_key": "sha256:...",
  "requested_at": 1715700000,
  "operator": { "pubkey": "<operator-pubkey>", "request_event": "<5950-or-1950-id>" },
  "controller": { "pubkey": "<soulfactory-controller-pubkey>" },
  "target": { "runtime": "openclaw", "runtime_pubkey": "<runtime-pubkey>", "agent_id": "agent-alice" },
  "soul": { "id": "agent-alice", "event": "<31951-id-if-existing>", "draft": "<31952-id>", "spec_hash": "sha256:..." },
  "params": {}
}
```

Field names are snake_case for wire compatibility. TypeScript may map to camelCase internally, but serialized events MUST use the documented snake_case names.

## Methods and params

All methods MUST reject unknown required fields only if they change meaning; additive optional fields MUST be ignored when unsupported and reported in warnings when useful.

### `soulfactory.provision`

Creates or binds a runtime-managed agent from an exact resolved draft.

Required params:

- `identity`: `{ "name", "purpose", "tier", "nip05"? }`
- `runtime`: `{ "target": "openclaw" | "metiq", "capability_ref" }`
- `permissions`: `{ "allowed_kinds": number[], "tool_grants": string[], "approval_policy": string }`
- `relay_policy`: `{ "read": string[], "write": string[], "control": string[], "nip65_discovery"?: boolean }`
- `workspace`: `{ "repo"?, "branch"?, "environment"? }`
- `assets`: `{ "avatar_ref"?, "voice_ref"? }`
- `bahia`: optional deployment/registration intent metadata.

### `soulfactory.update`

Applies a new desired spec to an existing managed agent.

Required params: `patch` or `resolved_spec`, `previous_spec_hash`, `new_spec_hash`, and `update_mode` (`merge` or `replace`).

### `soulfactory.persona.update`

Hot-reloads persona and system-prompt configuration for an existing managed agent without reprovisioning.

Required params:

- `schema`: `soulfactory-persona/v1`.
- `persona`: normalized `SoulPersonaSpec` with `traits`, `style`, `tone`, `constraints`, and canonical `system_prompt_sections` keys (`role`, `guidelines`, `red_lines`).
- `openclaw`: `{ "system_prompt_sections", "system_prompt_override", "agent_defaults_patch" }`, where `system_prompt_override` is the deterministic composite prompt assembled from `role`, `guidelines`, and `red_lines` sections. `agent_defaults_patch` uses OpenClaw-native config keys such as `systemPromptOverride`.

### `soulfactory.suspend`

Stops active runtime execution without deleting identity, state, or workspace bindings.

Required params: `reason` and optional `until` timestamp.

### `soulfactory.resume`

Resumes a previously suspended managed agent.

Required params: `reason`; optional `expected_state` may be used for optimistic validation.

### `soulfactory.redeploy`

Recreates or refreshes runtime deployment/session bindings for the same soul.

Required params: `reason`, `strategy` (`restart`, `rebuild`, or `migrate`), and optional `target_environment`.

### `soulfactory.revoke`

Terminates runtime authority for the managed agent. This is destructive for runtime access but MUST preserve enough audit/read-model state for Bahia to publish final `31951/7950` results.

Required params: `reason`, `revoke_runtime_credentials` boolean, and optional `delete_workspace` boolean.

## `38386` result content

Runtime results MUST use one JSON envelope for success and failure:

```json
{
  "schema": "soulfactory-runtime-control/v1",
  "method": "soulfactory.provision",
  "idempotency_key": "sha256:...",
  "request_event": "<38384-id>",
  "operator_request_event": "<5950-or-1950-id>",
  "status": "success",
  "result": {
    "agent_id": "agent-alice",
    "runtime": "openclaw",
    "runtime_binding": "openclaw://agents/agent-alice",
    "state": "running",
    "spec_hash": "sha256:...",
    "capability_ref": "<30317-id>",
    "observed_at": 1715700005,
    "warnings": []
  },
  "error": null
}
```

`status` is `success`, `rejected`, or `failed`. `rejected` means no side effect was performed because validation/authorization failed. `failed` means the runtime accepted the request but execution failed or partially failed.

## Error shape

`error` MUST be either `null` or:

```json
{
  "code": "unauthorized_controller",
  "message": "controller pubkey is not trusted by this runtime",
  "retryable": false,
  "details": {
    "controller": "<pubkey>"
  }
}
```

Standard codes: `invalid_schema`, `unsupported_method`, `unsupported_schema_version`, `missing_required_tag`, `missing_required_param`, `invalid_signature`, `unauthorized_controller`, `misaddressed_request`, `stale_request`, `duplicate_conflict`, `spec_hash_mismatch`, `runtime_unavailable`, `execution_failed`, `publish_failed`.

## Idempotency

- `idempotency-key` tag and `idempotency_key` content field MUST match.
- Keys SHOULD be deterministic from controller pubkey, method, operator request event id, target runtime pubkey, agent id, and spec hash.
- Runtimes MUST persist enough request/result state to recognize replay after reconnect.
- Exact replay returns the previous `38386` result without repeating side effects.
- Same key with different method, target, agent id, or spec hash MUST return `duplicate_conflict` and perform no side effect.

## Trust model

- Operators sign Bahia-facing `31952`, `5950`, and `1950` events.
- SoulFactory validates operator authorization, resolves drafts/spec hashes, then signs `38384` as the configured controller key.
- Runtime bridges trust only configured SoulFactory controller pubkeys. Capability metadata MAY advertise accepted controller pubkeys, but local runtime config is authoritative.
- Runtimes MUST reject unsigned, invalidly signed, self-authored, stale, unauthorized, malformed, or misaddressed `38384` events before local agent/session changes.
- `38386` results are accepted by SoulFactory only when signed by the targeted runtime pubkey and correlated to the original `38384` and operator event.

## Validation rules

Before side effects, runtimes MUST verify:

1. NIP-01 event id and Schnorr signature are valid.
2. `created_at` is not stale or too far in the future according to runtime policy.
3. `kind` is `38384` and `schema` is supported.
4. Required tags are present and match content fields.
5. `p` addresses this runtime pubkey.
6. `controller` is trusted and matches event `pubkey`.
7. `method` is supported by the current runtime capability.
8. `spec-hash` matches the resolved params/spec hash where applicable.
9. `idempotency-key` has not been used for a conflicting request.

SoulFactory MUST verify relay `OK` acceptance for published requests/results and MUST handle `EVENT`, `EOSE`, `CLOSED`, and `AUTH` explicitly per the plan.

## Capability compatibility (`30317`)

Runtime capabilities SHOULD include JSON content with:

```json
{
  "schema": "soulfactory-runtime-capability/v1",
  "runtime": "openclaw",
  "methods": ["soulfactory.provision", "soulfactory.update", "soulfactory.persona.update", "soulfactory.suspend", "soulfactory.resume", "soulfactory.redeploy", "soulfactory.revoke"],
  "control_schema": "soulfactory-runtime-control/v1",
  "controller_pubkeys": ["<trusted-controller-pubkey>"],
  "relay_hints": { "read": [], "write": [], "control": [] }
}
```

Bahia MUST capability-gate runtime target choices on live, trusted, compatible `30317` announcements. Static allowlists may remain as an additional safety gate until both bridges are deployed.

## Go and TypeScript compatibility rules

- Wire JSON uses snake_case field names and Unix seconds for timestamps.
- Numeric Nostr kinds remain numbers, not strings.
- Unknown optional fields are ignored; unknown required method params reject with `missing_required_param` or `invalid_schema`.
- Errors use the standard shape above in both languages.
- Fixtures generated from this document should be consumed unchanged by Go and TypeScript tests.
- Method names are exact lowercase strings prefixed with `soulfactory.`.

## Shared example flow

1. Operator signs a `31952` draft and a `5950` provisioning request.
2. SoulFactory validates the request, resolves the draft, captures draft event id and spec hash, publishes `6950` progress, and signs a `38384` `soulfactory.provision` event to the chosen runtime.
3. Runtime validates trust/tags/schema/idempotency, provisions or binds the agent, and publishes a correlated `38386` result.
4. SoulFactory validates the runtime result, publishes final `31951` with runtime binding/capability/spec refs, then publishes terminal `7950` correlated to the original operator request.

No step may infer terminal completion from timeout, missing events, or relay/subscription closure.

The `5950` request is the Bahia-facing provisioning intent for this SoulFactory flow. Browser and Go clients MUST keep its wire shape aligned: tags carry `draft`, `draft-event`, `spec-hash`, runtime/capability tags, `method=soulfactory.provision`, and `request-kind=5950`; content carries `schema=soulfactory-provisioning/v1`, `method=soulfactory.provision`, draft refs, `spec_hash`, `brief`, and `requested_at`. Durable completion still comes from subscribed `6950`/`7950`/`31951` events and validated `38386` runtime results.

REST provisioning and lifecycle endpoints are intentionally not part of this control plane. HTTP may serve docs or MCP tooling, but it must not become the source of truth for SoulFactory provisioning, lifecycle, progress, or terminal completion.
