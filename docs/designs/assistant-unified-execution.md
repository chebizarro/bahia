# Assistant unified execution contract (item 1)

Status: **contract and pure compatibility policy only**. Production still uses its
v1 batch dispatcher and agent loop until items 2 and 3 switch both paths
atomically. The browser changes are item 4. This document is the frozen
cross-agent contract for `bahia-1qkfk`, with item 1 tracked as `bahia-rhevv`.

## Decision 1: persisted workflow and lifecycle

`domain.AssistantWorkflow` is closed to `batch` and `iterative`. New sessions use
`bahia.assistant-session.v2` on kind 30900 at
`d=bahia.assistant-session.v2:<session_id>`, with `execution_version=2`,
workflow, run, revision, phase, public approval state and checkpoint reference.
Existing `AssistantSession` and `ComputePlanHash` remain the v1 reader/hash.
`AssistantPromptRequest` adds optional `contract_version` and `workflow`.
Selection order for a new turn: explicit request workflow, persisted session
workflow, then `assistant.default_workflow`; absent new config maps the old
`assistant.agentic.enabled` (`true` = iterative, `false` = batch). Once a session
exists, current config never changes its behavior. Workflow switches require a
finished prior run with no unresolved effects. A running, awaiting, blocked or
accounting-pending run rejects an overlapping prompt with `run_in_progress`.
The old flag is only a default-selection input; it is never a constructor or
authorization gate (item 3, `bahia-oknmu`).

`AssistantExecution` has one ordered work array and cursor, with exactly the
phase/work-state enums in `internal/domain/assistant_execution.go`. Nil
`AllowedTools` means unrestricted; an empty non-nil slice denies every tool.
JSON always emits `allowed_tools` so storage round trips preserve this boundary.
Work arguments and private command scope are deeply copied, encrypted in
checkpoints, and never handed to proposers as mutable authoritative pointers.

## Decision 2: exact batch revision approval

A v2 `assistant/approval` batch decision names session, run, workflow,
proposal, base revision/hash, approved revision/hash and request identity.
Unchanged approval uses the same revision; edited approval uses base + 1.
The candidate preserves step order, requires unique nonempty IDs and object
arguments, rejects unknown structural fields, and strips previews/model-supplied
idempotency keys. Whole-batch preparation and policy validation precede approval.
A hook changing effective arguments creates a new reviewable draft; it never
executes under the stale approval. Zero steps is a valid no-op.

The new hash is SHA-256(lowercase hex) of [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785.html) canonical JSON over the exact
`AssistantBatchApprovalHashInput` envelope: `version=2`, session, run,
`workflow=batch`, proposal ID, revision, **public approval-scope commitment**,
and normalized plan. Scope includes command name, `allowed_tools`, selected
refs, and an RFC 8785 SHA-256 digest of private command arguments where present.
This binds the complete private scope without publishing those arguments in a
public 30900 projection. Browser and Go byte-for-byte vectors are in
`testdata/assistant/batch_approval_hash_vectors.json`; v1 `ComputePlanHash` is
unchanged. The hash input does not include previews or dispatch keys.

Iterative v2 approval names a single action and exact argument digest. A shared
tool name does not authorize a different call. Rejection records a denied
observation, not run cancellation.

## Decision 3: one durable executor

`service.AssistantTurnEngine` freezes `StartTurn`, `Decide`, `Cancel`, `Recover`
and `Reconcile` plus request/result shapes. `AssistantBatchProposer` and
`AssistantIterativeProposer` only propose; they do not dispatch or mutate the
execution record. An iterative response's **entire** ordered call sequence must
be checkpointed before its first call. Batch continuation advances that cursor
without calling the model.

The executor persists ready/dispatching, then invokes once, then persists a
sync observation, async receipt, failure or `uncertain`. It persists an
observation before transcript consumption. Checkpoint revision and predecessor
ID provide an unambiguous chain; equal timestamps and conflicting successors
must not select a winner by arrival order. A required checkpoint must have
accepted relay publication before the next side effect. Retry the same signed
event. This does not make provider submission transactional: a recovered
`dispatching` item without a receipt is `uncertain`, never auto-replayed. An
operator may reconcile only against a verified exact downstream request event;
absence at EOSE is not proof of non-submission. Batch failure/denial skips
successors; iterative observations may feed reasoning under the original guard.

### Registry verdict for the checkpoint

Checked `docs/nostr-event-implementation-guide.md` section 4 and
`cascadia-nips/nips/NIP-CAS-0001.md` canonical kind/tag table. **4903 is
CAS_AUDIT, a regular append-only kind**, not a replaceable state coordinate.
Its required tags are `domain`, `type`, `schema`; `e` source correlation and
`p` requesting actor are appropriate when known. Use
`domain=assistant`, `type=execution-checkpoint`,
`schema=bahia.audit.assistant-execution-checkpoint.v1`, plus `session` and `run`
for scoped reads, `revision` and `prev` as untrusted lookup hints,
optional `e`/`p`, and **no `d` tag**. The predecessor in authenticated
plaintext is authoritative; public tag values never select a chain. Content must be the
service-held authenticated encrypted envelope; never place work arguments,
transcript text, approvals or secret scope values in public content or tags.
Retention/archive and accepted-OK semantics still need item 2 implementation
proof. No new kind number or SQL table is authorized by this design.

## Decision 4: common authorization

Both workflows pass registered descriptor/schema validation, persisted command
scope, current permission policy, PreToolUse, revalidation after hook changes,
exact approval binding, and PostToolUse through one tool runtime. A hard deny
cannot be overridden by human approval. Hooks may tighten but not loosen.
Batch review remains mandatory; iterative autonomy remains under existing
permission modes. Internal tools and subagents keep their separate registration
and no-parent-effects boundaries. An approved argument change blocks rather
than silently substituting new content.

## Decision 5: cancellation and accounting

`assistant/cancel` is a new version-2 run/session operation; it is not
`assistant/approval` with `decision=cancel`. It requires session, run and scope,
rejects a stale run, persists stop intent, blocks new dispatch/model work and
skips undispatched work. Already-submitted work remains observable; there is
no rollback. The phase stays `cancelling` while known/uncertain effects remain.
The executor uses short per-session reservations, no lock across model/provider
I/O, and process-lifetime observation contexts. One active writer per service
identity is required; active-active execution is outside this change.

## Decision 6: explicit v1 classification

`ClassifyAssistantLegacySession` is pure, consumes validated source event ID,
v1 JSON and optional validated transcript evidence, and publishes/dispatches
nothing. Its results are policy classifications, not dispatch authority.

| Historical state | Conversion policy |
| --- | --- |
| Terminal with no unresolved effects/actions | Read-only; further work needs a new v2 session. |
| Pure batch awaiting approval with matching plan/hash/turn/request and no dispatch evidence | V2 draft; approval still passes current common policy. |
| Pure batch executing/blocked with exact ordered step/key/receipt correlation | Observe known receipts; unreceipted work is uncertain; park successors for renewed review. |
| Pure iterative awaiting approval with matching run/turn/action/call and object arguments | V2 pending action; approval still passes current policy. |
| Pure iterative waiting on exact run/call receipt | Accounting-only unless full ordered call sequence and consumed observations reconstruct; then continuation may resume. |
| Iterative running without a complete checkpoint | Park. |
| Active mixed, malformed, contradictory or orphan receipt | Park; never choose using current config or same-tool-name fallback. |
| Terminal with unresolved effects | Preserve historical terminal outcome, create blocked accounting state when identities correlate; otherwise park accounting for reconciliation. |

A migrated execution records source event/schema, deterministic migrated run
ID and any original v1 plan hash. A migrated batch draft receives a freshly
computed v2 proposal hash over normalized input; the v1 hash is retained only
as `migration.legacy_plan_hash` for old unedited approval compatibility. Never
put the historical hash into the v2 proposal `hash` field. Conversion checkpoints are item 2; original v1 events remain unchanged.
Unversioned, unedited batch approval may bind only a migrated current draft.
Unversioned edited `ModifiedPlan` must return `approval_contract_upgrade_required`.
Old action approval requires unique matching run/turn/call; old plan-hash cancel
maps only to the current migrated batch run. New runs require v2 contracts.

Recovery must use the checkpoint chain, not the 500-event startup cache as a
historical census. A caller-supplied unknown session ID needs scoped EOSE-aware
coordinate lookup before creation. Receipt subscriptions validate event ID,
signature, provenance, kind and request/resource correlation; EOSE ends
backfill, not the operation. CLOSED/AUTH loss blocks observation and triggers
reconnect/reissue, not synthetic failure.

## Rollout and rollback

Item 1 is additive and must not alter production execution. Item 2 implements
the store/runtime/observer; item 3 switches routing, config and DI atomically
with item 2; item 4 consumes these exact contracts and fixture vectors.
Stop old writers before enabling v2 mutations. Rollback disables assistant
mutations and retains v2 history; never downgrade an approved v2 queue into
v1 executable `PendingSteps`. Historical inventory is bounded by retained
relay/archive evidence, not by startup hydration limits.

## Item 2 implementation notes (`bahia-0th5g`)

The executor is `AssistantExecutionEngine` (`internal/service/assistant_execution.go`),
the only component that dispatches assistant work. Item 3 (`bahia-oknmu`) wired
it and removed the v1 dispatchers; see "Item 3 implementation notes" below.

**Commit before effect.** Every state change is committed by appending an
encrypted kind-4903 checkpoint and adopting it only after a relay OK. The
`dispatching` reservation (immutable arguments and executor-issued key) is
committed before the provider is invoked. A checkpoint that is not confirmed
fences the session: no later checkpoint and no side effect occur, and
`Decide`/`Cancel`/`Reconcile`/`StartTurn`/`Recover` first retry the identical
signed event (the store refuses a different payload at the same revision).
Only on acceptance is the fence lifted; in-process knowledge then resolves the
fenced item (never invoked -> `ready`, receipt or observation known -> recorded).
After a restart that knowledge is gone and a receipt-less `dispatching` item
becomes `uncertain`.

**Cursor and phase.** The cursor is always the first unfinished work item.
Phases after approval are derived from work states: any `uncertain` item
blocks; cancellation stays `cancelling` while any item is `dispatching`,
`waiting_async`, `observed` or `uncertain`; a failed or denied batch step skips
undispatched successors. A journal transition validator enforces append-only
work, forward-only states, and immutable dispatched input, keys, receipts and
observations.

**Observation.** One backfill-plus-live subscription per receipt, keyed by
run/work/request. EOSE completes backfill only. CLOSED/AUTH or a stream end
shows the session projection as `blocked` while the journal stays
`waiting_async` (loss of relay visibility is not execution state and must not
fence the journal); the subscription is reissued after reconnect backoff and
the projection returns to `waiting_async` after the fresh EOSE. A terminal
event is checkpointed as `observed` before the transcript append; the append
is idempotent by logical ID (`<run>:<work>:observation`), so replays after a
crash never duplicate transcript entries or advance the cursor twice.

**Ambiguous dispatch.** `uncertain` work is never redispatched. `Reconcile`
accepts only an operator-named request event that
`AssistantContextVMRequestEvidenceResolver` fetches by ID through EOSE and
proves: command-signer author, kind 25910, `d` equal to the executor key, a
method the tool publishes (`mcp.AssistantAsyncToolRequestMethods`) and params
equal to the effective arguments. Absence at EOSE stays unresolved.

**Migrated runs.** Accounting classifications (`batch_accounting`,
`iterative_accounting`, `terminal_accounting`) only observe correlated receipts
and accept reconciliation; they never dispatch or resume reasoning, and remain
`blocked` until the operator cancels. `batch_draft` and `iterative_action`
continue through the common executor.

**Projection clock.** The v2 projection is replaceable and NIP-01 resolves
equal `created_at` by lowest event ID, so the engine stamps
`created_at = max(now, last + 1)` per session. The clock is recovered from the
latest published projection (recovery hydration, or a scoped EOSE lookup before
the first operation on a session in a process), so it stays monotonic across
restarts. Bursts run ahead of wall-clock by one second per extra projection;
that is intended.

**Iteration guards.** The engine blocks an iterative run after
`MaxConsecutiveToolFailures` trailing failed/denied observations or
`MaxWorkItems` work items. Counting model calls needs model history, which the
execution record does not hold; that bound belongs to the iterative proposer.

**Recovery.** `AssistantSessionRecoveryRunner` has one path: validate
service-signed projections (NIP-01 latest; v2 preferred over v1), classify v1
through `ClassifyAssistantLegacySession`, append the conversion root once
(conversion run IDs are deterministic), hydrate identity, then call `Recover`.
Without an engine and store it logs and parks every session.

## Item 3 implementation notes (`bahia-oknmu`)

**One dispatcher.** `AssistantOrchestrator` is request routing only: it
validates requests, supplies workflow-selection inputs and maps engine
outcomes onto the ContextVM result shape. It no longer plans, dispatches,
observes or writes v1 session state; `submittedPlans`, the direct batch
dispatch loop, `observeDownstreamResult`, the runtime session persister and
`AssistantAgentLoop`'s `Execute`/`ExecuteWithHooks` dispatch are deleted.
`AssistantToolRuntime.DispatchPreparedWork` is the only caller of
`InvokeAssistantAsyncTool` for assistant work and is called only by the
engine and by the gated subagent child path (sync-only); an AST guard test
(`TestAssistantDispatchHasOneBoundary`) pins these call sites.

**Proposers.** `AssistantBatchPlanner` (planner model, streaming status
chunks, catalog validation) and `AssistantAgentLoop.ProposeIterative` (all
calls of one model response; model-iteration cap derived from the durable
transcript) produce proposals only. `AssistantProposalContext` is the shared
command expansion and SessionStart/UserPromptSubmit hook preparation and is
the engine's scope resolver, so the command scope is persisted in the run's
root checkpoint before either proposer runs. Batch continuation never calls a
model.

**Internal tools.** Subagent delegation and skill loading keep a separate,
service-owned registration (`AssistantToolRuntime.RegisterInternalTools`,
never the MCP registry, never the batch catalog) and run as executor work
items through the same registration/schema/scope/permission/hook gate.
Subagent child calls use `ExecuteSubagentTool`: the parent's persisted scope
and current policy apply, and only allowed synchronous MCP tools run.

**Transport.** `assistant/prompt`, `assistant/approval`, `assistant/cancel`
and `assistant/reconcile` are registered. Accepted requests answer
`{status:"accepted", step:<acknowledgment>, phase, run_id, session}`;
refusals keep `{status:"failed", step:<reason>, summary, error}`. Unversioned
approvals pass the v1 compatibility decoder (Decision 6 rules) in
`internal/controlplane/assistant_handlers.go`. A caller-supplied unknown
session ID is created only after a scoped v1/v2 coordinate lookup through
EOSE; v1-only history refuses new turns (`legacy_session_read_only`).

**Wiring and recovery.** `internal/app/assistant_execution.go` always builds
the runtime, permission engine, transcript and checkpoint stores, observer,
evidence resolver, iterative proposer and the engine when the assistant is
enabled. The batch proposer is built only when `assistant.llm_model` is set,
which config validation requires when the default workflow is batch. Without
it, a new turn resolving to batch is refused with `workflow_unavailable` and
never downgraded; approving or rejecting an existing batch draft,
cancellation, reconciliation and continuation of approved batch runs need no
proposer and are unaffected. The engine runs under an application-lifetime context owned by a background
runner. Startup recovery receives the engine and store and
resumes runs. A finished run's checkpoint chain is loaded before the next
turn on that session so a recorded session-scope cancellation stays enforced
after restart.
