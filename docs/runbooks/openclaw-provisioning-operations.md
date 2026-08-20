# OpenClaw provisioning operations

Task: **bahia-openclaw-rollout-conformance-20260819**

This runbook operates the durable saga and dedicated runtime contract. Supply values through protected environment or files; never put bunker URIs, NIP-46 keys, tokens, private keys, or DM plaintext on a command line, in logs, or in evidence.

## Deploy

1. Verify the release record contains exact Bahia/OpenClaw/Signet/relay image digests and source commits.
2. Back up database, relay data, saga state, Signet enrollment/client-reference state, and per-soul volumes.
3. Render Compose and verify every promoted image is repository@sha256 with 64 lowercase hexadecimal characters.
4. Enable one disposable canary only. Keep incumbents and previous release available.
5. Gate on Bahia readiness, Signet connectivity, relay NIP-11/AUTH/EOSE, runtime health, real inference, independent encrypted DM round-trip, terminal 7950 plus 31951, and Marjam/SNR reachability.
6. Record only run/request IDs, public event IDs, one-way resource refs, commits, digests, timestamps, and outcomes.

## Inspect and trace

Run the saga operator inspect dry-run first. Correlate request_id, run_id, agent_id, current stage/age, build/instance, one-way resource references, and public 7950/31951 event IDs.

Do not infer success from Compose health, an agent record, 100% progress, ContextVM acknowledgment, or subscription closure. Success requires dm_verified, running, and correlated terminal evidence.

Prometheus series use the bahia_openclaw_provisioning_ prefix. Run labels are intentional for tracing and must not contain payload content.

## Retry

1. Inspect failed stage and external reality.
2. Confirm it is recoverable and spec/run correlation is unchanged.
3. Retry the same request ID.
4. Verify inspection precedes mutation and exact replay does not repeat effects.
5. Confirm a new durable stage or sanitized failure record.

Never retry an ownership conflict by changing labels or deleting the conflict.

## Reconcile

1. Reconcile in dry-run mode.
2. Review the proposed action and one-way resource reference.
3. Reconcile the same request.
4. Verify checkpoint version advances and reality matches.
5. After relay recovery, require historical EVENT processing through EOSE.

## Abort

1. Inspect and dry-run safe-abort.
2. Confirm candidates are created, owned by the same run, and match spec/correlation.
3. Execute safe-abort.
4. Verify dependency-ordered compensation and rolled_back terminal projection.
5. Investigate orphan metrics; never delete adopted, pre-existing, Marjam, or SNR resources to clear an alert.

## Backup and restore

Back up together:

- Bahia database and durable saga
- relay event store and policy baseline
- Signet service state and enrollment/client-reference files
- each managed soul's Compose/config/agent/workspace
- release record, provenance, and sanitized inventory

Restore first into an isolated disposable environment. Start relay and Signet, restore Bahia/saga, then reconcile runtimes. Verify exact replay, NIP-46 reconnect without one-time handoff, EOSE backfill, inference, DM gate, and terminal projection. Restore is incomplete if it recreates an identity or reintroduces a consumed pairing secret.

## Policy rotation

1. Inventory exact-client public keys and allowed methods/kinds.
2. Apply least-privilege policy live with signetctl; do not restart Signet merely to load policy.
3. Verify authorized signing and expected denial.
4. Retain prior policy revision for restoration.
5. Revoke old client policy only after every runtime reconnects durably.

## Credential cleanup

Remove only consumed one-time handoff and proven failed-run client material. Retain protected durable client references needed for restart. Scan sanitized evidence for credential markers. Never delete an incumbent identity or SNR key as cleanup.

## Rollback

1. Disable new provisioning admission.
2. Restore previous pinned Bahia/sidecar/runtime release and policy.
3. Keep newly created valid identities and durable client state.
4. Reconcile existing runs; do not blindly replay mutation intents.
5. Verify Marjam, SNR, canaries, relay backfill, inference, DM, and terminal projections.
6. Re-enable only after the incident is understood.

## Incident and alert response

| Alert | Immediate action |
| --- | --- |
| BahiaOpenClawProvisioningStageStuck | Inspect run/stage and dependency readiness; dry-run reconcile. |
| BahiaOpenClawProgressWithoutTerminal | Treat as not running; inspect correlated terminal publish and relay OK. |
| BahiaOpenClawFalseRunning | Disable admission and trace DM gate plus terminal lineage. |
| BahiaOpenClawOrphanCandidates | Freeze cleanup, verify ownership, dry-run safe-abort. |
| BahiaOpenClawRepeatedSignetUnauthorized | Freeze policy changes; compare client public key and policy revision. |
| BahiaOpenClawCorrelationMismatch | Stop affected run; preserve evidence and investigate lineage. |

For critical alerts preserve the previous release, capture sanitized metrics/logs and public IDs, and avoid destructive host edits. Escalate Marjam changes for separate review.
