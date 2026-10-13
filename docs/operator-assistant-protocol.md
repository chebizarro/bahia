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

## Iterative agentic execution

`workflow: "iterative"` selects the provider-neutral agent loop. A model response
may contain several ordered `tool_calls` (`id`, registered `name`, JSON-object
`arguments`). The proposer records the user/model messages in the protected
transcript and hands the **whole** call sequence to the v2 executor. The
executor checkpoints that sequence before dispatching its first call; the
model is asked for another response only after the prior observations are
consumed. Tool calls are proposals, not permission grants or completed work.

The transcript uses `AssistantAgentMessage` roles `system`, `user`,
`assistant`, and `tool`. Content blocks are `text`, `tool_call`,
`tool_observation`, or `json`. A tool message carries the original
`tool_call_id`, tool `name`, and a normalized `observation`. These are
provider-neutral, encrypted transcript values, **not** new ContextVM methods
or public session-projection fields. The observation shape has
`observation_id`, `tool_name`, `status`, and optional `tool_call_id`, `effect`,
`risk`, `execution_mode`, `summary`, `content`, `result`, `error`, `receipt`,
`deferred_action`, `event_id`, `observed_at`, and `metadata`.

| Observation status | Meaning |
|---|---|
| `succeeded` | A synchronous result or correlated terminal async event succeeded. |
| `failed` | A definite tool failure or correlated terminal async event failed. |
| `denied` | The registered-tool, scope, permission, hook, or approval gate refused the call. |
| `deferred` | Human approval is required; `deferred_action` identifies the exact pending action. |
| `waiting_async` | Submission has a receipt but no correlated terminal result yet. |
| `cancelled` | A call was cancelled without claiming a successful effect. |

The status vocabulary is the normalized observation type; it does not make a
receipt terminal. In v2, `waiting_async` is checkpointed as a work state with
an `AsyncToolReceipt` (`request_event_id`, request/result kinds, correlation
and idempotency key). The executor observes the matching signed event,
checkpoints `observed`, then appends the terminal tool observation to the
transcript exactly once. The checkpoint's ordered `work` array and `cursor`
(first unfinished work item) survive restart. Work states also include
`awaiting_approval`, `uncertain`, and `abandoned`; the public projection
exposes only phase, approval state, checkpoint reference, and effect counts,
not private arguments, receipts, or transcript content.

`allowed_tools` is an authorization boundary, not merely a model hint. The
command scope is persisted before proposal; JSON `null` means unrestricted,
while `[]` denies all tools. The model sees only in-scope schemas, and the
executor rechecks that same scope, registered schema, current permission
policy, hooks, and exact approval binding before dispatch. A model-named tool
that was not advertised remains unauthorized. A resumed turn keeps the
checkpointed scope; a config or model change cannot expand it. An iterative
approval binds one `action_id` and effective argument digest. A hook changing
arguments creates a new reviewable draft, never execution under an old hash.

Configured subagents and skills are service-owned internal tools, not external
MCP methods. `bahia_assistant_delegate_subagent` takes a configured subagent
name and task, returns a synchronous observation, and lets the child call only
read-only synchronous MCP tools allowed by the parent's persisted scope.
`bahia_assistant_skill_load` exposes a configured skill body or a file within
that skill's directory; the initial model context contains only skill names
and descriptions. Both internal tools pass the common registration, scope,
permission, and hook gate. Configured hooks run at `UserPromptSubmit`,
`SessionStart`, `PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`, and
`SessionEnd`; they may tighten a tool decision but not override a hard deny.
Hook handlers are prompt or read-only MCP-tool evaluations, not shell commands.

The older `AssistantAgentLoopMetadata` fields (`state: running`,
`last_observation_id`, `transcript_cursor`, and `allowed_tools`) are a v1
session-metadata decode shape. New execution uses the v2 checkpoint's
`phase`, `work`, `cursor`, and `scope` instead; clients must not infer a v2
wire state from the historical `agent_loop` metadata.

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

- evidence resolution supplies the exact signed kind-`30900` intent event ID
  matched to the durable processor marker; the correlated kind-`30315`
  status remains the outcome evidence. An ephemeral kind-`25910` request ID
  cannot be looked up later and is not reconciliation evidence;
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
