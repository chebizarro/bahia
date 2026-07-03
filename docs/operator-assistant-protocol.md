# Operator Assistant Protocol

Canonical event contract for the LLM-enabled Bahia operator assistant.

This document defines Milestone 1 protocol contracts only. It does not define orchestrator implementation, LLM provider behavior, UI components, or recovery runners.

## Design constraints

- Nostr is the source of truth for assistant sessions and turns.
- The assistant uses event-native pub/sub flows only: subscribe, react to events, publish results, and verify relay responses.
- No downstream control-plane command may be published before explicit operator approval.
- Transport interruptions are not terminal business outcomes. Never infer `completed`, `failed`, or `rejected` from a timeout or missing event.
- Phase 1 downstream commands are signed by the Bahia service key and carry an `agent` tag for assistant attribution.

## Production event surfaces

| Surface | Kind(s) | Author | Semantics |
| --- | --- | --- | --- |
| Assistant prompt and approval intents | ContextVM `25910`, optionally wrapped in `1059`/`21059` | Operator browser key | JSON-RPC methods such as `assistant/prompt`, `assistant/approve`, `assistant/reject`, and `assistant/cancel` |
| Assistant session state | `30900` or `30078` | Bahia service pubkey | Replaceable canonical projection keyed by `d=<assistant-session-coordinate>` |
| Assistant status | `30315` | Bahia service pubkey | NIP-38 progress/status events correlated to the ContextVM request with `e` and resource tags |
| Assistant transcript | `30316` | Bahia service pubkey | Append-only encrypted conversation/tool transcript entries using a service-held symmetric-key AEAD envelope in `content` |
| Assistant audit/result facts | `4903` | Bahia service pubkey | Immutable terminal, approval, execution, and provenance facts |
| Discovery and relay topology | `11316`-`11320`, `30002` | Bahia service pubkey | ContextVM tool/resource announcements and relay sidecar/bootstrap sets |

Legacy assistant custom kinds `31990` and `38420`-`38423` are migration inventory only. Active clients and agents must not publish or subscribe to those kinds as the live assistant protocol.

## Common tags and JSON-RPC metadata

All assistant ContextVM requests and canonical observables MUST carry a session correlation value, either as a Nostr `session` tag, JSON-RPC metadata, or both:

| Field/tag | Format | Meaning |
| --- | --- | --- |
| `session` | `["session", "<session_id>"]` or `params.session_id` | Assistant session correlation key |
| `agent` | `["agent", "<assistant_agent_id>"]` or `params.agent_id` | SoulFactory assistant identity used for attribution |
| `e` | `["e", "<request_event_id>", "", "reply"]` | Correlation from service-authored observables back to the ContextVM request |

## ContextVM method contracts

### `assistant/prompt`

Author: operator browser key.

Semantics: prompt turn intent. Replaying the same JSON-RPC `id` or idempotency key MUST NOT create a duplicate plan; consumers should return or project the existing turn result.

Content JSON contract inside kind `25910`:

```json
{
  "jsonrpc": "2.0",
  "id": "assistant-turn:<session_id>:<turn_id>",
  "method": "assistant/prompt",
  "params": {
    "session_id": "<session_id>",
    "turn_id": "<turn_id>",
    "prompt": "natural-language operator request",
    "route_context": {
      "route": "/deployments/abc123",
      "params": { "id": "abc123" },
      "resource_type": "deployment",
      "resource_id": "abc123"
    },
    "selected_refs": ["deployment:abc123"],
    "_meta": { "progressToken": "assistant-turn:<session_id>:<turn_id>" }
  }
}
```

### `assistant/approve`, `assistant/reject`, and `assistant/cancel`

Author: operator browser key.

Semantics: approval/cancel decision. Legacy approvals remain valid by `plan_hash`; agentic approvals add `action_id` to resume one deferred action. `cancel_scope` scopes cancel decisions (`action`, `turn`, or `session`) without removing the existing `plan_hash` compatibility field.

Content JSON contract:

```json
{
  "jsonrpc": "2.0",
  "id": "assistant-approval:<session_id>:<plan_hash>",
  "method": "assistant/approve",
  "params": {
    "session_id": "<session_id>",
    "plan_hash": "<sha256_hex>",
    "action_id": "<agentic-action-id>",
    "cancel_scope": "action",
    "decision": "approve",
    "reason": "operator-provided note",
    "_meta": { "progressToken": "assistant-approval:<session_id>:<plan_hash>" }
  }
}
```

### Assistant session/status/result observables

Author: Bahia service pubkey.

- `30900`/`30078` carries the latest assistant session projection with `d`, `session`, `p=<operator_pubkey>`, `agent`, `status`, `domain=assistant`, and `schema` tags.
- `30315` carries non-terminal progress such as `planning`, `planned`, `awaiting_approval`, `executing`, `step_started`, `step_completed`, or `blocked`.
- `30316` carries durable transcript messages. Its `content` is not per-recipient sealed; it is a service-held symmetric-key AEAD envelope with `schema=bahia.assistant-transcript.v1`, `envelope=service-held-symmetric-key-aead`, `key_ref`, `key_version`, `nonce`, and `ciphertext`. Tags mirror `domain=assistant`, `schema`, `session`, `turn`, `role`, `seq`, `key_ref`, `key_version`, `key_rotation`, and `envelope` for scoped replay and key rotation.
- `4903` carries immutable approval, execution, and terminal facts such as `completed`, `blocked`, `failed`, `rejected`, `cancelled`, or `needs_clarification`.

Observable content SHOULD include concise human summaries plus structured metadata such as `AssistantPlan`, `plan_hash`, downstream ContextVM request event IDs, token/cost accounting, or error details. Terminal facts MUST accurately reflect the downstream terminal result when one exists.

## Plan JSON schema

The assistant plan is the central contract shared by the LLM planner, backend validator, and frontend approval UI.

Go structs:

```go
type AssistantPlan struct {
    Summary            string              `json:"summary"`
    NeedsClarification bool                `json:"needs_clarification"`
    ClarifyingQuestion string              `json:"clarifying_question,omitempty"`
    RiskLevel          string              `json:"risk_level"`
    ContextRefs        []string            `json:"context_refs,omitempty"`
    Steps              []AssistantPlanStep `json:"steps"`
}

type AssistantPlanStep struct {
    StepID         string         `json:"step_id"`
    Title          string         `json:"title"`
    Description    string         `json:"description"`
    ToolName       string         `json:"tool_name"`
    ToolArgs       map[string]any `json:"tool_args"`
    ArgsPreview    map[string]any `json:"args_preview,omitempty"`
    IdempotencyKey string         `json:"idempotency_key,omitempty"`
}
```

JSON Schema:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "AssistantPlan",
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "needs_clarification", "risk_level", "steps"],
  "properties": {
    "summary": { "type": "string", "minLength": 1 },
    "needs_clarification": { "type": "boolean" },
    "clarifying_question": { "type": "string" },
    "risk_level": { "type": "string", "enum": ["low", "medium", "high"] },
    "context_refs": { "type": "array", "items": { "type": "string" } },
    "steps": {
      "type": "array",
      "items": { "$ref": "#/$defs/AssistantPlanStep" }
    }
  },
  "$defs": {
    "AssistantPlanStep": {
      "type": "object",
      "additionalProperties": false,
      "required": ["step_id", "title", "description", "tool_name", "tool_args"],
      "properties": {
        "step_id": { "type": "string", "minLength": 1 },
        "title": { "type": "string", "minLength": 1 },
        "description": { "type": "string", "minLength": 1 },
        "tool_name": { "type": "string", "minLength": 1 },
        "tool_args": { "type": "object" },
        "args_preview": { "type": "object" },
        "idempotency_key": { "type": "string" }
      }
    }
  }
}
```

`risk_level` is informational. It is rendered to the operator but does not gate approval by itself.

## Plan hash

`plan_hash` is:

```text
sha256(canonical_json({"session_id": <session_id>, "plan": <AssistantPlan>}))
```

The hash binds an operator approval to one exact session plan. An `assistant/approve` ContextVM request with a hash that does not match the latest session plan MUST be rejected as stale and MUST NOT publish downstream commands.

At execution time, each step receives a derived idempotency key:

```text
assistant:<session_id>:<plan_hash>:<step_id>
```

## Session state machine

```text
idle
  -> planning             prompt received
planning
  -> awaiting_approval    side-effecting plan generated
  -> idle                 clarification/read-only answer produced
awaiting_approval
  -> executing            latest plan approved
  -> idle                 latest plan rejected
executing
  -> completed            all steps reach terminal success
  -> blocked              relay CLOSED/auth interruption before terminal downstream result
  -> failed               downstream terminal failure or operator cancel
blocked
  -> failed               operator cancels stuck session
```

Valid states:

- `idle`
- `planning`
- `awaiting_approval`
- `executing`
- `blocked`
- `completed`
- `failed`

## Approval semantics

- Every side-effecting plan requires explicit operator approval.
- Read-only explanatory turns and clarification prompts may complete without approval.
- `decision=approve` executes only if the approval `plan-hash` matches the latest session plan hash.
- `decision=reject` returns the session to `idle` and MUST NOT publish downstream commands.
- `decision=cancel` is valid for `executing` or `blocked` sessions. It moves the session to `failed`, stops observation, and does not attempt rollback.
- Duplicate approval for the same already-submitted plan MUST NOT republish downstream commands.

## No-timeout semantics

The assistant MUST NOT use timeout-based completion logic for event delivery or downstream execution.

- Historical catch-up completion is determined by EOSE.
- Publish acceptance is determined by relay `OK` messages, including both accepted flag and message.
- Relay `CLOSED`, auth loss, or subscription interruption before a downstream terminal result moves the session to `blocked`, not `failed`.
- Missing events or transport failure never imply success, failure, or rejection.
- Reconnect uses exponential backoff and reissues subscriptions; event handlers dedupe by event ID.

## Assistant-safe tool catalog scope

Assistant tools are backed by ContextVM JSON-RPC methods carried as Nostr kind `25910`, usually encrypted with CEP-4 / NIP-59 wrappers (`1059` or `21059`) for sensitive payloads. Tool responses are acknowledgments only; the assistant follows canonical observables for progress and terminal truth.

| Action | ContextVM method | Observable follow-up |
| --- | --- | --- |
| Service deploy | `service/deploy` | `30315`, `4903`, `30900` scoped by `service` / `environment` / `artifact` |
| Service rollback | `service/rollback` | `30315`, `4903`, `30900` scoped by `service` / `environment` |
| Service restart/stop | `service/restart`, `service/stop` | `30315`, `4903`, `30900` scoped by `service` / `environment` |
| LLM deploy | `llm/deploy` | `30315`, `4903`, `30900` scoped by `route` / `environment` / `release` |
| LLM approval | `llm/approve` | `30315`, `4903`, `30900` scoped by `intent` |
| LLM rollback | `llm/rollback` | `30315`, `4903`, `30900` scoped by `route` / `environment` |
| ML deploy | `ml/inference-deploy` | `30315`, `4903`, `30900` / `30078` scoped by endpoint/model tags |
| ML approval | `ml/inference-approve` | `30315`, `4903`, `30900` scoped by deployment/intent tags |
| ML rollback | `ml/inference-rollback` | `30315`, `4903`, `30900` scoped by endpoint/environment tags |

Excluded unless explicitly allowlisted:

- mutation methods not present in the assistant-safe catalog
- raw REST mutation fallbacks after any relay has accepted a signed ContextVM request
- legacy Bahia request/status/result kinds except as migration fixtures

The planner output MUST be validated against this catalog before a plan is shown to the operator.

## Normalized async tool receipt

```go
type AsyncToolReceipt struct {
    ToolName        string
    Method          string
    RequestEventID  string
    ObservableKinds []int
    DTag            string
    ResourceTags    map[string]string
    IdempotencyKey  string
    PublishedRelays []string
}
```

The receipt records the ContextVM request event and the exact canonical kinds/tags the assistant observes for progress and terminal outcomes. Legacy `request_kind`, `status_kind`, `result_kind`, and read-model kind fields may appear only in migration fixtures or historical conversion reports.

## Signing model

Production signing model:

- Operator browser key signs ContextVM `25910` prompt requests, approval decisions, and other operator intents.
- Sensitive assistant/operator intents are wrapped with CEP-4 / NIP-59 `1059` or `21059`; authorization uses the verified inner ContextVM event pubkey after unwrap.
- Bahia service key signs assistant state/discovery/canonical observable events, including `11316`-`11320`, `30900`, `30315`, `4903`, `30002`, and `30078` where applicable.
- Bahia service key may initiate ContextVM downstream methods on behalf of the assistant only when the approved plan and authorization policy allow it.
- Downstream ContextVM requests published by the assistant MUST include an `agent` tag or equivalent JSON-RPC metadata for audit attribution.
- Legacy assistant custom kinds are migration inventory only and are not the production signing surface.

## Validation requirements

Consumers and handlers MUST validate inbound events before acting:

- NIP-01 event ID hash matches serialized event.
- Schnorr signature is valid for the event pubkey where the runtime can verify signatures.
- For encrypted ContextVM, unwrap first, then verify the inner event signature and authorize the inner pubkey.
- Timestamp is reasonable.
- Required tags for the event kind are present.
- JSON-RPC content matches the expected ContextVM method contract.
- Events are deduped by event ID.
- Replaceable/addressable semantics keep only the latest event by service pubkey and `d` tag for `30900`, `30078`, `11316`-`11320`, and `30002`.
