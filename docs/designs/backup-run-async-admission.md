# Backup-run asynchronous admission

This document describes the current bounded implementation, not an unmerged
candidate or permission to restore a SQL coordinator. The operator contract is
[PostgreSQL loss and recovery](../runbooks/postgres-loss-and-recovery.md#workflow-authority-and-supported-slices)
and the remaining scope is tracked in `bahia-cd0wr`.

## Enabled subset and startup gate

`backup_run.async_intake_enabled` is **default off**. When explicitly enabled,
startup checks signer/Signet, writable original ledger, relay configuration,
Kopia and the effect fence. `internal/app/app.go` constructs
`NewBackupRunExecutor`, wires both publishers' delivery notifications to it,
and registers it with the background manager. The single unfiltered Kopia
snapshot path executes through that runner; it is not a disabled placeholder.
Multi-step, filtered, non-Kopia, policy/verification, restore and retention
execution remain fail closed. Deterministic tests do not enable live intake or
satisfy signer/relay/Kopia production acceptance.

## Signed intake and durable admission

1. `BackupIntentHandler` validates the exact signed UUIDv7 request, actor,
   coordinate, freshness/expiration and execution snapshot. It records the full
   signed request in the outbox-side pending ledger, not SQL or a disposable
   event cache. Matching replay returns durable state; changed ID/coordinate
   bindings conflict. Generic processed-marker success is not acceptance.
2. The history/admission runner operates outside the inbound sync callback.
   It checks every configured canonical relay, exact service author/run
   coordinate, NIP-11/paging completeness, per-relay EOSE, stable connection
   and topology evidence, plus local history. Missing, limited, interrupted or
   ambiguous history cannot establish a new run coordinate. EOSE alone is
   not a reservation or global absence proof.
3. The fenced stage transaction binds the request, full signed run-state
   event, coordinate indexes and outbox entry. Admission retains exact event
   bytes through delivery failures; replay does not sign another run state.
4. Exact run-state quorum proof permits staging the request-correlated signed
   accepted status. Execution requires both run-state and accepted-status
   quorum proofs; outbox enqueue or a cached accepted event is insufficient.

The production deployment requires one active daemon/service-key writer with
its preserved receipt ledger. A local filesystem lock is not cross-host
fencing. Do not activate another daemon with the same identity and a separate
outbox, infer a pristine fleet from a replaced ledger, or replay SQL queues.

## Credential binding, external effect and restart

The execution snapshot binds canonical configuration and Signet credential ID
and version; it never carries secret values or legacy unversioned credential
fields. `BackupRunExecutor` resolves the pinned version, writes private
apply-scoped Kopia configuration, and persists a nonsecret pre-effect receipt.
The snapshot is tagged with the bound run/step identity. Restart observes those
exact Kopia tags before deciding whether a checkpoint can advance. A failed or
ambiguous observation requires operator recovery, not another blind effect.

Signed step checkpoints and terminal state/status use retained publisher-quorum
proofs before completion/compaction. Crash between an effect and checkpoint,
or checkpoint and publication, reuses the immutable receipt and signed event
rather than reconstructing authority from SQL or re-signing a new outcome.
Ephemeral credential files are removed; they are not durable effect inputs.

## Source and verification map

| Boundary | Implementation / proof |
|---|---|
| Signed input and deferred outcome | `internal/controlplane/backup_intent_handler.go`, `backup_signed_run_request.go` |
| Pending/indexed atomic stage and durable request binding | `internal/adapters/nostr/localstore/backup_run_pending.go`, `backup_run_admission.go` |
| Relay-history evidence and wake/retry behavior | `internal/adapters/nostr/backup_run_history.go`, `backup_run_history_runner.go` |
| Delivery proof and retry retention | `internal/adapters/nostr/localstore/outbox.go`, publisher delivery tests |
| Pinned credentials, tagged effect, step/terminal proof | `internal/controlplane/backup_run_executor.go`, `backup_run_receipts.go`, executor tests |
| Real production construction | `internal/app/app.go`, backup startup/wiring tests |

Preserve the outbox, local events, receipt journals, trust state and containment
markers together. Missing/corrupt original ledgers are not empty history; use
operator-controlled restoration/reconciliation. Operator enablement and
exact-artifact staging remain acceptance gates in `bahia-cd0wr`; no old plan
instruction to disable the wired executor overrides current code or those gates.
