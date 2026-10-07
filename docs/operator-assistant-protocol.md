# Operator assistant protocol

The operator assistant is a governed ContextVM interaction whose durable
execution state is published as signed, encrypted Nostr evidence. The active
execution contract is version `2` (`domain.AssistantExecutionVersion`).

## Transport and authorization

The daemon advertises four ContextVM methods:

| Method | Purpose |
|---|---|
| `assistant/prompt` | start a turn and create a governed run |
| `assistant/approval` | approve or reject a batch proposal or iterative action |
| `assistant/cancel` | cancel one run or close a session |
| `assistant/reconcile` | attach evidence to uncertain work or attest abandonment |

Requests are JSON-RPC 2.0 kind-`25910` messages gift-wrapped to the Bahia
service pubkey. The outer response uses the same stored/ephemeral lifetime and
correlates with `e=<outer-request-id>,reply`. A routed request receives a
`notifications/progress` processing acknowledgement before the terminal
JSON-RPC result.

The signer must pass the fleet-operator gate and be a participant in the named
session. An accepted JSON-RPC result acknowledges the operation; relay-backed
session/status/checkpoint events are the execution truth.

## Prompt

Version-2 clients send:

```json
{
  "contract_version": 2,
  "workflow": "batch",
  "session_id": "<uuid>",
  "turn_id": "<uuid>",
  "prompt": "Deploy the selected release",
  "selected_refs": ["service:<uuid>"]
}
```

`workflow` is `batch` or `iterative`. `session_id`, `turn_id` and non-empty
`prompt` are required. A command scope can constrain the command name,
selected references, arguments and `allowed_tools`; `null` tools means
unrestricted and an empty list means no tools.

## Plan and approvals

An executable plan has exactly these structural fields:

```json
{
  "summary": "…",
  "needs_clarification": false,
  "clarifying_question": "",
  "risk_level": "medium",
  "context_refs": ["…"],
  "steps": [
    {
      "step_id": "deploy-1",
      "title": "Deploy",
      "description": "…",
      "tool_name": "bahia_deploy",
      "tool_args": {}
    }
  ]
}
```

Step IDs are unique and ordered; `tool_args` must be an object. Presentation
previews and caller-provided dispatch keys are excluded from the executable
plan before hashing.

A batch approval binds version, session, run, workflow, proposal ID, revision,
approval scope and normalized plan. The hash is lowercase SHA-256 of RFC-8785
canonical JSON. The approval request names the exact `run_id`,
`proposal_id`, revision and hash; an edited plan is validated and becomes a
new bound revision. A stale revision/hash is rejected.

Iterative approval binds one `action_id` and the SHA-256 canonical-JSON digest
of its arguments. `decision` is `approve` or `reject`; cancellation is a
separate method.

## Cancellation and reconciliation

Cancel request:

```json
{
  "contract_version": 2,
  "session_id": "<uuid>",
  "run_id": "<uuid>",
  "scope": "run",
  "reason": "operator request"
}
```

`scope` is `run` or `session`. Session cancellation prevents new turns.
Cancellation does not invent a terminal result for a side effect whose outcome
is unknown.

Reconciliation addresses one work item in `uncertain` state:

- evidence resolution supplies the exact `request_event_id` observed for the
  submitted downstream request;
- abandonment supplies a reason and the exact attestation
  `outcome_unknown_cannot_reconcile`.

Abandonment closes accounting but claims neither success nor failure. Missing
evidence never authorizes a replay.

## Durable records

| Record | Shape | Visibility |
|---|---|---|
| Session projection | `30900`, `t=assistant-session`, schema `bahia.assistant-session.v2` | protected read model; no private tool arguments |
| Status | `30315`, `t=assistant-status`, schema `bahia.assistant-status.v1` | public bounded progress/result |
| Transcript | `30316`, `t=assistant-transcript` and session topic | protected; XChaCha20-Poly1305 service-held AEAD |
| Execution checkpoint | `4903`, type `execution-checkpoint`, schema `bahia.audit.assistant-execution-checkpoint.v1` | protected; encrypted work arguments, approvals and receipts |

The session projection exposes workflow, run/revision, phase, approval scope,
proposal, pending approvals and effect counters. It never embeds checkpoint
arguments or approval bindings. Checkpoints chain with the prior event ID and
are durable before execution advances.

Execution phases are `proposing`, `awaiting_approval`, `executing`,
`waiting_async`, `blocked`, `cancelling`, `completed`, `failed` and
`cancelled`. Work-item states include `pending`, `ready`, `dispatching`,
`waiting_async`, `observed`, terminal success/failure/denial/skip, `uncertain`
and `abandoned`.

## Completion rules

- Relay `OK` proves only that a request or checkpoint reached a relay.
- `EOSE` ends stored catch-up only; it is never completion.
- A submitted asynchronous tool is terminal only when its canonical observable
  is received and recorded in the work receipt.
- A restart resumes from the encrypted checkpoint. Read-only synchronous work
  may be released for redispatch; an effectful dispatch without a provable
  outcome becomes `uncertain`.
- Permission decisions are bound to the operator, request, proposal/action,
  argument digest and command scope. The executor rechecks them under the
  session lock immediately before dispatch.

Tool availability and permission policy come from the assistant's registered
MCP catalog and `assistant.permissions`; arbitrary model-supplied tool names do
not become executable.

See [assistant execution architecture](architecture/assistant-execution.md)
and the [event specification](event-spec.md).
