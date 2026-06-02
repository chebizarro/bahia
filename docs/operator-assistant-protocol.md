# Operator Assistant Protocol

Canonical event contract for the LLM-enabled Bahia operator assistant.

This document defines Milestone 1 protocol contracts only. It does not define orchestrator implementation, LLM provider behavior, UI components, or recovery runners.

## Design constraints

- Nostr is the source of truth for assistant sessions and turns.
- The assistant uses event-native pub/sub flows only: subscribe, react to events, publish results, and verify relay responses.
- No downstream control-plane command may be published before explicit operator approval.
- Transport interruptions are not terminal business outcomes. Never infer `completed`, `failed`, or `rejected` from a timeout or missing event.
- Phase 1 downstream commands are signed by the Bahia service key and carry an `agent` tag for assistant attribution.

## Event kinds

| Kind | Name | Author | Semantics |
| --- | --- | --- | --- |
| `31990` | Assistant session read model | Bahia service pubkey | Replaceable read model (`d=<session_id>`) for canonical session state |
| `38420` | Assistant prompt request | Operator browser key | Addressable prompt turn request (`d=assistant-turn:<session_id>:<turn_id>`) |
| `38421` | Assistant plan approval | Operator browser key | Addressable approval decision (`d=assistant-approval:<session_id>:<plan_hash>`) |
| `38422` | Assistant status | Bahia service pubkey | Append-only progress/status event, correlated to a request with an `e` reply tag |
| `38423` | Assistant result | Bahia service pubkey | Append-only terminal/result event, correlated to a request with an `e` reply tag |

## Common tags

All assistant events MUST include:

| Tag | Format | Meaning |
| --- | --- | --- |
| `session` | `["session", "<session_id>"]` | Assistant session correlation key |

Service-authored events SHOULD include:

| Tag | Format | Meaning |
| --- | --- | --- |
| `agent` | `["agent", "<assistant_agent_id>"]` | SoulFactory assistant identity used for attribution |

Events that reply to an operator request MUST include:

| Tag | Format | Meaning |
| --- | --- | --- |
| `e` | `["e", "<request_event_id>", "", "reply"]` | NIP-01 event correlation to the prompt or approval event |

## Kind-specific tag contracts

### `31990` Assistant session read model

Author: Bahia service pubkey.

Semantics: replaceable read model. The newest valid event for `kind + pubkey + d` is the canonical session state.

Required tags:

| Tag | Format |
| --- | --- |
| `d` | `["d", "<session_id>"]` |
| `session` | `["session", "<session_id>"]` |
| `p` | `["p", "<operator_pubkey>", "", "operator"]` |
| `agent` | `["agent", "<assistant_agent_id>"]` |
| `status` | `["status", "idle|planning|awaiting_approval|executing|blocked|completed|failed"]` |

Content JSON SHOULD follow `AssistantSession` from `internal/domain/assistant.go` and carry the current state, operator pubkey, assistant identity, last plan hash, pending steps, and transcript summary.

### `38420` Assistant prompt request

Author: operator browser key.

Semantics: addressable prompt turn. Replaying the same `d` tag MUST NOT create a new plan; consumers should return or project the existing turn result.

Required tags:

| Tag | Format |
| --- | --- |
| `d` | `["d", "assistant-turn:<session_id>:<turn_id>"]` |
| `session` | `["session", "<session_id>"]` |
| `p` | `["p", "<service_pubkey>", "", "service"]` |

Optional tags:

| Tag | Format | Meaning |
| --- | --- | --- |
| `agent` | `["agent", "<assistant_agent_id>"]` | Preferred assistant identity when known |
| `route` | `["route", "<current_route_path>"]` | UI route context hint |
| `resource` | `["resource", "<resource_type>", "<resource_id>"]` | Explicit selected resource hint |

Content JSON contract:

```json
{
  "session_id": "<session_id>",
  "turn_id": "<turn_id>",
  "prompt": "natural-language operator request",
  "route_context": {
    "route": "/deployments/abc123",
    "params": { "id": "abc123" },
    "resource_type": "deployment",
    "resource_id": "abc123"
  },
  "selected_refs": ["deployment:abc123"]
}
```

### `38421` Assistant plan approval

Author: operator browser key.

Semantics: addressable approval/cancel decision. The decision is valid only for the latest session plan hash.

Required tags:

| Tag | Format |
| --- | --- |
| `d` | `["d", "assistant-approval:<session_id>:<plan_hash>"]` |
| `session` | `["session", "<session_id>"]` |
| `plan-hash` | `["plan-hash", "<sha256_hex>"]` |
| `decision` | `["decision", "approve|reject|cancel"]` |
| `e` | `["e", "<plan_or_prompt_event_id>", "", "reply"]` |

Optional tags:

| Tag | Format | Meaning |
| --- | --- | --- |
| `agent` | `["agent", "<assistant_agent_id>"]` | Preferred assistant identity when known |

Content JSON MAY include a human-readable reason:

```json
{
  "reason": "operator-provided approval/rejection/cancel note"
}
```

### `38422` Assistant status

Author: Bahia service pubkey.

Semantics: append-only non-terminal progress. Status events are observable transcript entries and downstream correlation breadcrumbs.

Required tags:

| Tag | Format |
| --- | --- |
| `session` | `["session", "<session_id>"]` |
| `agent` | `["agent", "<assistant_agent_id>"]` |
| `status` | `["status", "planning|planned|awaiting_approval|executing|step_started|step_completed|blocked"]` |
| `e` | `["e", "<request_event_id>", "", "reply"]` |

Optional tags:

| Tag | Format | Meaning |
| --- | --- | --- |
| `plan-hash` | `["plan-hash", "<sha256_hex>"]` | Plan being presented or executed |
| `step` | `["step", "<step_id>"]` | Current plan step |
| `downstream-request` | `["downstream-request", "<event_id>"]` | Published downstream command event |

Content JSON is status-specific. Planned status events SHOULD include the full `AssistantPlan` and `plan_hash`.

### `38423` Assistant result

Author: Bahia service pubkey.

Semantics: append-only terminal result for a turn or execution flow.

Required tags:

| Tag | Format |
| --- | --- |
| `session` | `["session", "<session_id>"]` |
| `agent` | `["agent", "<assistant_agent_id>"]` |
| `status` | `["status", "completed|blocked|failed|rejected|cancelled|needs_clarification"]` |
| `e` | `["e", "<request_event_id>", "", "reply"]` |

Optional tags:

| Tag | Format | Meaning |
| --- | --- | --- |
| `plan-hash` | `["plan-hash", "<sha256_hex>"]` | Terminal plan hash |
| `downstream-request` | `["downstream-request", "<event_id>"]` | Downstream command that produced the terminal outcome |

Content JSON SHOULD include a concise human summary plus structured metadata such as downstream terminal event IDs, token/cost accounting, or error details. A final assistant result MUST accurately reflect the downstream terminal result when one exists.

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

The hash binds an operator approval to one exact session plan. A `38421` approval with a hash that does not match the latest session plan MUST be rejected as stale and MUST NOT publish downstream commands.

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
