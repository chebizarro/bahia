# Nostr event-store lifecycle

This runbook operates Bahia's two-phase PostgreSQL event archive. Never delete
`nostr_events` rows with `psql`. All mutations use `bahia-event-archive`, which
reads the normal Bahia config and refuses to prune without a protected-object
receipt.

## Preconditions

1. Record a verified PostgreSQL backup and immutable version.
2. Confirm `/health` and `/ready`; record `bahia_nostr_outbox_depth` and
   `bahia_nostr_outbox_failed`.
3. Mount `/var/lib/bahia/nostr-archive` mode `0700` on durable storage.
4. Record relation bytes, estimated rows, oldest event, startup duration, and
   the query plans used by the archive selection.

## Prepare online access paths

Run before the first batch and after any schema update that adds an archive
index or constraint:

```sh
bahia-event-archive --config /etc/bahia/config.yaml --action ensure-indexes
```

The command is idempotent. It creates large indexes concurrently, validates the
archive ownership foreign key and publish-state checks, and converts supported
transitional Security publication states before validation. `claim-export`
fails closed until every required access path and constraint is ready.

Verify the publish-state paths:

```sql
SELECT convalidated FROM pg_constraint
WHERE conname = 'nostr_events_publish_state_check';
SELECT indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'idx_nostr_events_publish_failed';
SELECT conname, convalidated FROM pg_constraint
WHERE conname IN ('security_observable_publications_publish_state_check',
                  'security_scan_runs_publish_state_check');
```

All returned booleans must be true before canary export.

## Protect the publish outbox

The local publish outbox (`nostr.local_store.outbox_path`, default
`outbox.bolt` beside the local event store) is authoritative delivery state,
not a cache. Back it up with the daemon data and never delete it while pending
entries exist. PostgreSQL `nostr_events` also holds transaction-bound audit
outbox rows and may hold undelivered imported rows; the publisher drains those
in place. Rows whose `publish_target` starts with `local:` mirror the local
outbox outcome and are never drained from PostgreSQL.

- `bahia_nostr_outbox_depth` combines pending local entries with pending
  PostgreSQL-drained rows.
- `bahia_nostr_outbox_failed` combines abandoned local entries with failed
  PostgreSQL-drained rows. PostgreSQL failed rows are counted only after
  `ensure-indexes` creates `idx_nostr_events_publish_failed`.
- Local per-relay acceptance is durable, so restart resends only to relays that
  have not accepted. PostgreSQL-drained rows may be resent after restart;
  relay `OK duplicate:` responses count as acceptance.
- A terminal failure is never reset to pending. Correct the relay policy or
  event and reproduce the content as a new signed event.

See [WS6 alerts](ws6-alerts.md#bahianostroutboxfailed) for response guidance.

## Export a canary

Start with a small, high-volume transport batch:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action claim-export --kinds 1059,21059,25910 \
  --older-than 168h --batch-size 1000
```

`--cutoff <RFC3339>` overrides `--older-than`; `--all-kinds` is an explicit
alternative to `--kinds`. The result contains the batch UUID, artifact path,
SHA-256, compressed size, row count, and cutoff. Exported rows remain in
PostgreSQL, including across a process crash.

## Protect and confirm

Copy the artifact through the approved backup control plane, verify it there,
and record the immutable object URI and version. Then confirm the receipt:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action confirm --batch-id BATCH_UUID \
  --object-uri kopia://REPOSITORY/OBJECT \
  --object-version IMMUTABLE_VERSION
```

`file://` receipts and digest mismatches are rejected.

## Prune and prove restore

Prune one bounded chunk per invocation:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action prune --batch-id BATCH_UUID --batch-size 1000
```

Repeat until the JSON response reports `done=true`. Watch outbox and relay
health throughout. Then perform a restore proof:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action restore --batch-id BATCH_UUID
```

Restore verifies the compressed artifact digest and inserts by event ID
idempotently. A second restore must report zero inserts.

## Stop or recover

Stop invoking `claim-export`; the daemon never claims rows automatically.
Claimed or exported rows remain live. Protected or pruned batches remain
restorable from their immutable artifacts. Revert an application image only
after confirming schema compatibility; leave additive archive metadata intact.

Record backup and object versions; batch UUID, cutoff, kinds, row count, digest
and size; before/after storage and query measurements; outbox observations;
crash recovery; relay-unavailability behavior; and the idempotent restore proof.
