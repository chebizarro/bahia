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
3. Restart the daemon after the index is healthy if SQL-dependent capabilities
   are required. The background recovery probe migrates and closes its own pool;
   it does not attach repositories or restart the application.
4. Recheck `/ready` and `canonical_delivery`; SQL recovery alone is not proof of relay catch-up or outbox acceptance. Confirm no unexpected increase in canonical coordinates, service-key signatures or local outbox entries merely because the restored database contains extra rows.

Legacy SQL-only state requires a separately governed migration with a dry-run census, stable semantic coordinates, admission limits, durable progress, crash/retry proof, and independent status. Do not trigger such a migration by restarting the daemon, reconnecting PostgreSQL, or manually changing SQL publish flags. The [startup source audit](../analysis/postgres-startup-source-audit.md) identifies the code paths that must be absent before this runbook is an acceptance proof.

The optional `runtime-observation-retention` runner starts only when PostgreSQL is connected. It deletes at most 500 unlinked `runtime_observations` older than 30 days per transaction, waits 250 ms between full batches, retries failures after one minute, and checks again hourly when caught up. The current state-linked row is never removed by this job, even if older than 30 days. Track `bahia_runtime_observation_rows_retired_total`, `bahia_runtime_observation_retention_batches_total{outcome=...}` and the runner warning log. A PostgreSQL outage does not block readiness or alter canonical relay records. Existing immutable F74a archive rows are distinct from this hot-table retention job; their removal belongs to the observation-history cutover, not this runner.

## Disposable startup and reconnect matrix

The opt-in repository test starts and removes its own `postgres:16-alpine`
container. It migrates an empty index, populates derived service rows, and
records a signed SQL-only pending service-registry event, with the production
cp-state envelope and a service payload decoded by the projection catalog,
through the repository API. It
boots the application with PostgreSQL absent, empty, populated, divergent,
and slow to answer the startup handshake. A proxy then makes the same
divergent index reachable for a recovery probe. For each case it checks the
startup duration, bounded `/health` and `/ready` responses, unchanged core
readiness checks, and the actual local bbolt event store and publish outbox:
the SQL-only event must never appear there, while a distinct local signed
pending event remains intact. The recovery probe must not import the SQL row.

```sh
BAHIA_OPTIONAL_PG_MATRIX_CONFIRM=disposable \
  go test -tags=integration ./internal/app \
  -run '^TestOptionalPostgresStartupAndRecoveryMatrix$' -count=1 -v
go test ./internal/adapters/nostr \
  -run '^TestLocalOutboxWithoutPostgresRestartResumesPendingDeliveries$' -count=1 -v
```

The second test proves that the local outbox actually resumes delivery to a
relay after restart without PostgreSQL. The matrix itself uses no relays, so
its `/ready` response remains relay-gated; it proves PostgreSQL does not alter
that core decision, **not** that relay catch-up completed. Record the matrix's
per-case timings and both test exit statuses. Exact-image, exact-digest Docker
and staging soak with real relay `EOSE`/`OK` outcomes, production-scale SQL
cardinality, and process RSS remains a separate unverified acceptance gate.

## Workflow authority and supported slices

SQL queues and schedules are not signed-intent authority. Do not clear or
manually advance rows, restore old SQL coordinators, or infer execution consent
from a queued row. Preserve the local receipt ledger and compare every effect
with the exact retained signed request, canonical revisions and relay outcomes.

The canonical implementations have bounded supported paths:

- Backup async intake is **default off**. When explicitly enabled with its
  startup prerequisites, a single unfiltered Kopia snapshot uses pinned Signet
  credentials, durable receipts, relay acknowledgment and tag-based crash
  observation. Multi-step, filtered, non-Kopia, policy/verification, restore and
  retention execution remain fail closed (`bahia-cd0wr`).
- Fleet-operator-signed tool requests and exact-event approvals support the
  canonical build/Docker deploy executor. Unsigned MCP/assistant requests and
  legacy ContextVM approvals cannot authorize those effects (`bahia-4f10q.2.1`).
- Signed LLM lifecycle intents reach the external-API gateway route executor
  with version-pinned Signet credentials and replay receipts. It is not accepted
  for production multi-environment routing: physical route identity currently
  omits the environment (`bahia-8ii1f`). Do not promote this path across
  environments until resource ownership and shared fencing are resolved. Runtime-managed
  model placement/provisioning and unsupported mixed backends remain fail
  closed (`bahia-4f10q.2.2`).
- Docker-only canonical auto-apply uses exact signed decisions and pinned input
  revisions under the local effect fence. Compose, public-route, canary and
  runtime-release auto-apply remain fail closed (`bahia-4f10q.2`).

The runtime is the authority on supported inputs and refusals; historical plans
are not permission to enable broader paths. A local filesystem fence requires
one active daemon per service identity and ledger; it is not a cross-host
lease. Live signer/relay/Kopia/runtime and exact-artifact recovery acceptance
remain separate gates even when deterministic tests pass.
