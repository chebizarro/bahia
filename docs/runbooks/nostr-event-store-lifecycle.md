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
outbox rows and may hold undelivered legacy rows. A daemon restart does not
import or drain those SQL rows into the local outbox; neither does reconnect.
Preserve them for an explicit, reviewed recovery operation; do not make
PostgreSQL the source of a new canonical publish. Rows whose `publish_target`
starts with `local:` mirror local-outbox events, not SQL delivery work.

- `bahia_nostr_outbox_depth` combines pending local entries with pending
  PostgreSQL rows, including legacy rows not automatically delivered after
  restart. A positive SQL contribution requires operator review; the combined
  count is not evidence that the daemon will deliver every counted row.
- `bahia_nostr_outbox_failed` combines abandoned local entries with failed
  PostgreSQL rows. PostgreSQL failed rows are counted only after
  `ensure-indexes` creates `idx_nostr_events_publish_failed`.
- Local per-relay acceptance is durable, so restart resends only to relays that
  have not accepted; relay `OK duplicate:` responses count as acceptance.
  PostgreSQL records no per-relay OK state. Even a row with
  `publish_attempts=0` may have reached a relay before the old process crashed;
  never infer that no relay accepted it.
- A terminal failure is never reset to pending. Correct the relay policy or
  event and reproduce the content as a new signed event.

To inventory legacy signed-outbox rows without writing, run the action for
each target:

```sh
bahia-migrate outbox-transfer --config "$CONFIG" --target default
bahia-migrate outbox-transfer --config "$CONFIG" --target control-plane
```

Each invocation reads a bounded page. If `next_after` is nonempty, save that
token and pass it as `--after <token>` on the next invocation for the same
target. The token carries the cumulative conflict count; a later page with no
new conflicts is **not** a clean scan if earlier pages reported conflicts.
Keep all `conflict` lines for operator reconciliation. The cursor is **not** a
snapshot: an insert or update ordered behind it during the scan can be missed.
For an exhaustive inventory, stop all SQL writers for the whole census. If
writers remain active, repeat the census from the beginning (omit `--after`)
and reconcile changed counts and rows; no single moving scan proves the backlog
is exhausted. `next_after` empty means only that this pass found no later row
at query time, not that the target has no pending rows or any event was
delivered.

There is no apply mode. PostgreSQL does not retain per-relay OK state, even
when `publish_attempts=0`. Moving a drainable entry into bbolt before claiming
its SQL row could publish an event whose SQL claim failed; claiming SQL first
could strand an event absent from the local outbox. The daemon's effective
relay pool can also be changed by durable relay-policy state after static
configuration is loaded. Do not manually reset, re-sign, or re-target these
rows based only on this inventory; a separately designed crash-safe transfer
procedure is required.

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
