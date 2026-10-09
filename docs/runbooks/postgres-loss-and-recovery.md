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
