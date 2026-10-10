# Assistant execution

The operator assistant runs on one durable executor,
`AssistantExecutionEngine` (`internal/service/assistant_execution.go`), the
only component that dispatches assistant work. Transport is ContextVM
(`assistant/prompt`, `assistant/approval`, `assistant/cancel`,
`assistant/reconcile`), registered in
`internal/controlplane/assistant_handlers.go`; wiring is
`internal/app/assistant_execution.go`. Wire shapes are in
`docs/operator-assistant-protocol.md`.

## Sessions and workflows

- `domain.AssistantWorkflow` is `batch` or `iterative`. A session is projected
  as `bahia.assistant-session.v2` on kind `30900` at
  `d = bahia.assistant-session.v2:<session_id>` with `execution_version=2`,
  workflow, run, revision, phase, public approval state and checkpoint
  reference.
- Workflow selection for a new turn: explicit request `workflow`, then the
  persisted session workflow, then `assistant.default_workflow`
  (`assistant.agentic.enabled` maps `true` → iterative, `false` → batch when
  `default_workflow` is unset). Once a session exists, config never changes
  its behaviour; switching workflows requires a finished run with no
  unresolved effects. A running, awaiting, blocked or accounting-pending run
  refuses an overlapping prompt with `run_in_progress`.
- The batch proposer needs `assistant.llm_model`; config validation requires
  it when batch is the default. Without it a turn resolving to batch is
  refused with `workflow_unavailable`, never downgraded; approving, rejecting,
  cancelling, reconciling and continuing existing batch runs need no proposer.
- `AssistantExecution` holds one ordered work array and a cursor (the first
  unfinished item). Nil `AllowedTools` is unrestricted; an empty slice denies
  every tool, and JSON always emits `allowed_tools`. Work arguments and private
  command scope are deep-copied, encrypted in checkpoints and never handed to
  proposers as mutable pointers.

## Proposers and the tool runtime

`AssistantBatchPlanner` and `AssistantAgentLoop.ProposeIterative` only
propose. An iterative response's **entire** ordered call sequence is
checkpointed before its first call; batch continuation advances the cursor
without calling the model. `AssistantProposalContext` resolves the command
scope, which is persisted in the run's root checkpoint before either proposer
runs. `AssistantToolRuntime.DispatchPreparedWork` is the single dispatch
boundary (`TestAssistantDispatchHasOneBoundary` pins the call sites); subagent
delegation and skill loading are service-owned internal tools that run as
executor work items through the same registration, schema, scope, permission
and hook gate, and subagent children run only allowed synchronous MCP tools
under the parent's persisted scope.

Both workflows pass descriptor/schema validation, persisted command scope,
the current permission policy, `PreToolUse`, revalidation after hook changes,
exact approval binding and `PostToolUse` through one runtime. A hard deny
cannot be overridden by approval; hooks tighten but never loosen. A hook that
changes effective arguments creates a new reviewable draft rather than
executing under the stale approval.

## Approval

A batch approval names session, run, workflow, proposal, base revision/hash,
approved revision/hash and request identity; an unchanged approval keeps the
revision, an edited one uses base + 1, and zero steps is a valid no-op. The
hash is SHA-256 (lowercase hex) over RFC 8785 canonical JSON of the
`AssistantBatchApprovalHashInput` envelope — `version=2`, session, run,
`workflow=batch`, proposal id, revision, the public approval-scope commitment
(command name, `allowed_tools`, selected refs, and an RFC 8785 digest of the
private arguments) and the normalized plan. Browser and Go byte-for-byte
vectors are in `testdata/assistant/batch_approval_hash_vectors.json`.

An iterative approval names a single action and its exact argument digest; a
shared tool name does not authorize a different call, and a rejection records
a denied observation rather than cancelling the run.

## Commit before effect

Every state change is an encrypted kind-`4903` checkpoint (`domain=assistant`,
`type=execution-checkpoint`,
`schema=bahia.audit.assistant-execution-checkpoint.v1`, `session` and `run`
tags for scoped reads, `revision` and `prev` as untrusted hints, no `d`; the
authenticated predecessor in plaintext is authoritative). The executor
persists `ready`/`dispatching` — with immutable arguments and an
executor-issued idempotency key — **before** invoking a provider once, then
persists a sync observation, async receipt, failure or `uncertain`. An
unconfirmed checkpoint fences the session: no later checkpoint or side effect
occurs until the identical signed event is accepted by a relay; `Decide`,
`Cancel`, `Reconcile`, `StartTurn` and `Recover` retry it first.

Phases after approval derive from work states: any `uncertain` item blocks;
cancellation stays `cancelling` while an item is `dispatching`,
`waiting_async`, `observed` or `uncertain`; a failed or denied batch step
skips undispatched successors. A journal validator enforces append-only work,
forward-only states and immutable dispatched inputs, keys, receipts and
observations. An iterative run blocks after `MaxConsecutiveToolFailures`
trailing failures/denials or `MaxWorkItems` (default 48) items.

## Observation, uncertainty and reconciliation

One backfill-plus-live subscription per receipt, keyed by run/work/request.
EOSE completes backfill only; CLOSED/AUTH loss shows the projection as
`blocked` while the journal stays `waiting_async`, and the subscription is
reissued after reconnect backoff. A terminal event is checkpointed as
`observed` before the transcript append, which is idempotent by
`<run>:<work>:observation`.

Provider submission is not transactional: after a restart a receipt-less
`dispatching` item becomes `uncertain` and is **never redispatched**.
`assistant/reconcile` accepts only an operator-named request event that
`AssistantContextVMRequestEvidenceResolver` fetches by id through EOSE and
proves (command-signer author, kind `25910`, `d` equal to the executor key, a
method the tool publishes, params equal to the effective arguments). Absence at
EOSE is not proof of non-submission.

`assistant/cancel` names session, run and scope, rejects a stale run, persists
the stop intent, blocks new dispatch and model work and skips undispatched
work; submitted work remains observable and there is no rollback. One active
writer per service identity is required.

## Projection clock and recovery

The v2 projection is replaceable and NIP-01 breaks equal `created_at` by
lowest id, so the engine stamps `created_at = max(now, last + 1)` per session,
recovering the clock from the latest published projection. Startup recovery
(`AssistantSessionRecoveryRunner`) validates service-signed projections (v2
preferred), classifies v1 sessions through the pure
`ClassifyAssistantLegacySession` (read-only, draft, pending action, accounting
or parked — never dispatch authority), appends the deterministic conversion
root once, hydrates identity and calls `Recover`. A finished run's checkpoint
chain is loaded before the next turn so a session-scope cancellation stays
enforced after restart.

## Assistant key transition boundary

Historical transcript (`30316`) and execution checkpoint (`4903`) envelopes use
one `AssistantTranscriptKeyProvider`. The deployed v1 provider derives its
XChaCha20 key from `nostr.private_key`. An offline transition primitive in
`internal/app/assistant_wrapped_keys.go` obtains exactly that v1 key from the
matching service configuration, generates a random versioned v2 key, and
NIP-44-wraps both through the writer-fenced Signet service signer. The wrapped
manifest retains the service pubkey and exact key identities; immutable v1
relay events can be read after the nsec is removed only if the v1 wrap remains
available.

`assistant_manifest_store.go` stores one manifest in an existing private
filesystem directory: a synced `0600` temp file is linked to the final path
without replacement, then the directory is synced. Operations pin the owner-
private directory handle; symlinked ancestors and unrecognized hard links are
rejected. If a crash leaves exactly one generated same-inode temp hard link,
restart removes that verified alias, syncs the directory and then loads the
committed file. Missing, corrupt, loose-permission or wrong-generation files fail
closed. A caller must retain the
v2 generation pin independently to detect rollback to a different valid file.
The manifest opener is **read-only**: `ActiveTranscriptKey` rejects new writes.
Assistant startup defaults to the deployed v1 provider. Explicit
`assistant.wrapped_keys.mode=wrapped_read_only` instead requires a local
manifest path, independently pinned v2 generation, real Signet bunker URI,
dedicated NIP-46 owner key that Signet has assigned as the identity's writer
(`agent/writer-acquire`); startup carries no lease epoch or expiry and checks
only that the owner key differs from the service key. Startup validates the
manifest, connects to Signet, unwraps both keys under the existing service
pubkey, then closes the bootstrap connection. It never falls back to the raw
key provider in this mode. Transcript and checkpoint reads are available,
but assistant work that needs a new encrypted event fails closed; this mode
is not a fully functional assistant or service-key cutover. The daemon still
requires `nostr.private_key` and its raw control-plane signer for other paths;
selecting this read-only mode does not remove or replace either. Writer activation
requires a create-once provisioned generation and complete transcript and
checkpoint read-write restart tests.
