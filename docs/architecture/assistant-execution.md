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
  The public projection includes an `abandoned_effects` count for the current
  run. Work identities, tools and abandonment attestations remain in the
  encrypted checkpoint; the count alone cannot establish an effect's outcome.
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
`schema=bahia.audit.assistant-execution-checkpoint.v1`, indexed
`t=assistant-checkpoint:<session_id>`, `session` and `run` metadata, `revision`
and `prev` as untrusted hints, no `d`; the authenticated predecessor in
plaintext is authoritative). The executor
persists `ready`/`dispatching` — with immutable arguments and an
executor-issued idempotency key — **before** invoking a provider once, then
persists a sync observation, async receipt, failure or `uncertain`. An
unconfirmed checkpoint fences the session: no later checkpoint or side effect
occurs until the identical signed event is accepted by a relay and retained
in the daemon's local event store; `Decide`,
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
`assistant/reconcile` accepts an operator-named intent event ID only when the
durable intent processor marker proves the exact tool, actor, idempotency key
and signed kind-`30900` event ID. The marker establishes submission, not a
successful downstream effect; the correlated non-ephemeral kind-`30315`
status remains the outcome evidence. Kind-`25910` requests are ephemeral and
are never recovered by a later REQ. Missing marker or result evidence never
proves non-submission and never authorizes redispatch. LLM lifecycle recovery
refuses marker-only evidence because it is not canonical provisioning proof.

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
enforced after restart. `Load` backfills with an indexed `#t` REQ through EOSE,
recovers pre-topic checkpoints with an author/kind REQ when needed, and merges
service-signed local checkpoints into one validated predecessor chain. The
local event store retains every relay-accepted assistant checkpoint,
including run heads and session-closing checkpoints, and the latest session
projection despite regular event pruning. A durable acceptance marker
distinguishes a committed checkpoint from a speculative pre-OK publisher cache
copy; only marked local checkpoints can authorize recovery. PostgreSQL is not a checkpoint source. When a relay lookup of
an unknown session ID reaches EOSE without a projection, signed local
projection or checkpoint evidence refuses creation with
`session_lookup_unavailable`; absence from relays does not prove a new ID.

## Transcript and checkpoint keys

Transcript (`30316`) and execution checkpoint (`4903`) envelopes are
XChaCha20-Poly1305 under one `AssistantTranscriptKeyProvider`,
`service.KeyringTranscriptKeys`: the `assistant-transcript`
[system credentials](secret-store.md#system-credentials) configured in
`secret_store.system_keys.assistant_transcript`, resolved from Signet at
startup and held for the life of the process. `assistant.enabled` requires at
least one. The first configured key seals new events; the others only open
events sealed under them. An event records its key as `key_ref` (the
credential label, `bahia-system/assistant-transcript/<key-id>`) and
`key_version` (its Signet version), in the tags and in the AEAD associated
data (transcripts and checkpoints alike; checkpoints sealed under the retired
nsec-derived key predate the binding and are only opened by the re-encryption
tool). The provider lends a cipher built inside the keyring's callback
(`WithActiveKey`, `WithKey`); key bytes never leave it. An event whose key is not configured, including one sealed under the
retired nsec-derived key (`assistant-transcript/service-nostr-key`), fails
closed with the key named; nothing derives a key from the service key.

Until the keys are resolved, sealing and opening wait within the caller's
deadline and then fail with `ErrUnavailable`; the `system_keys` health check
fails readiness meanwhile.

History sealed under the retired nsec-derived key and signed by the retired
service identity is re-established offline by
`cmd/bahia-assistant-reencrypt`, never at daemon start: it opens the history
with the retired key (read once from a file), re-seals each checkpoint chain
root to head with its predecessor links re-pointed, each transcript on its
coordinate and each session projection with its checkpoint reference
re-pointed, and publishes them signed by the current identity. See
[`assistant-transcript-keys.md`](../runbooks/assistant-transcript-keys.md).
