# PostgreSQL loss and relay-first recovery

PostgreSQL is an optional derived index. The daemon's control-plane truth is the latest valid service-signed state on configured relays, reconciled with its local bbolt event store and publish outbox. Database loss does not authorize a new signature, replay of SQL outbox rows, or promotion of SQL-only desired state. See [intents and authority](../architecture/intents-and-authority.md) and [outbox delivery](../architecture/outbox-delivery.md).

## During an outage

1. Preserve the service signing identity, configured relay set, local event store and local outbox. Do not clear the outbox or start a second process with the same signing key and a different outbox.
2. Check `/health` for process liveness and `/ready` for relay quorum, bootstrap catch-up, intent readiness, and required runners. A PostgreSQL-recovery runner is observable but non-required. If relay catch-up is incomplete, investigate relay `AUTH`, `CLOSED`, EOSE and local-store cursor state rather than restoring SQL as an authority.
3. Inspect `canonical_delivery`, local outbox pending/failed counts and per-relay `OK` outcomes. An accepted or queued local event survives PostgreSQL loss. A failed local event requires the explicit outbox retry procedure; changing SQL rows does not redeliver it.
4. Expect SQL-backed routes and integrations to return their documented unavailable response until the index is restored. Do not infer loss of canonical state from those route failures.

## Restoring the index

1. Restore or recreate PostgreSQL as a **derived** store. Keep the daemon's key, relays and bbolt files unchanged. Verify database identity and schema before enabling writers.
2. Rebuild only from validated relay/local canonical events using the domain's supported projection or `RebuildIndex` path. Check latest `(kind, pubkey, d)` coordinates and tombstones against relays, then compare derived rows. A SQL row absent from canonical history is not automatically republished.
3. Recheck `/ready` and `canonical_delivery`; SQL recovery alone is not proof of relay catch-up or outbox acceptance. Confirm no unexpected increase in canonical coordinates, service-key signatures or local outbox entries merely because the restored database contains extra rows.

Legacy SQL-only state requires a separately governed migration with a dry-run census, stable semantic coordinates, admission limits, durable progress, crash/retry proof, and independent status. Do not trigger such a migration by restarting the daemon, reconnecting PostgreSQL, or manually changing SQL publish flags. The [startup source audit](../analysis/postgres-startup-source-audit.md) identifies the code paths that must be absent before this runbook is an acceptance proof.

## Paused workflow recovery

`/ready` reports `backup_recovery`, `backup_scheduler`,
`llm_provisioning_recovery`, and `tool_provisioning_recovery` as warnings.
Automatic recovery and scheduled dispatch for these families are disabled:
their stored SQL queues and schedules are not signed-intent authority. LLM
gateway route repair is also paused because its desired state is SQL-backed.
Do not clear or manually advance the rows. Preserve PostgreSQL and the
relay/local event store, inventory the affected run ids and source event ids,
and compare each against retained validated signed intents and canonical
outcomes. Backup run, restore, and retention request intake and restore
approval are paused, including for valid signed requests; legacy Nostr
requests receive a request-correlated paused rejection and kind-30900 intents
receive a rejected status. Tool request handling remains available, but
manual tool approval is paused. SQL-resolved execution inputs cannot yet be
causally bound to the original signed request and atomically committed before
publication. Treat the lost backup capability as a release blocker; do not
edit SQL rows or manually invoke executors. A queued row without matching
canonical provenance must not be replayed. The
[paused recovery is a release blocker](../designs/relay-canonical-startup-and-recovery.md#workflow-recovery-authority),
not a successful workflow recovery. The
[workflow recovery design](../designs/relay-canonical-startup-and-recovery.md#workflow-recovery-authority)
defines the durable replay and restart evidence required to enable these
runners.
