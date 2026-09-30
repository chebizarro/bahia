# Bahia Nostr event-store lifecycle runbook

This runbook operates the two-phase archive introduced for
`bahia-nostr-events-growth-retention-partitioning-20260911`. Never delete rows
with `psql`. All mutations use `bahia-event-archive`, which reads the normal
Bahia config and refuses to prune without a protected-object receipt.

## Preconditions

1. Record a verified, supported PostgreSQL backup and its immutable version.
2. Confirm Bahia health/readiness and record pending outbox depth.
3. Confirm the archive volume is mounted at
   `/var/lib/bahia/nostr-archive` and is mode 0700.
4. Record relation bytes, estimated rows, oldest hot event, startup duration,
   and the relevant query plans.

## Install online access paths

Run once after the metadata migration:

```sh
bahia-event-archive --config /etc/bahia/config.yaml --action ensure-indexes
```

The command creates each large index concurrently and validates the archive
ownership foreign key outside the startup migration. `claim-export` fails
closed until every archive index and the constraint are ready.

### After deploying migration 000071 (publish target and `failed` state)

Migration 000071 adds `nostr_events_publish_state_check` as `NOT VALID`, so
startup never scans `nostr_events` under an exclusive lock. Run
`ensure-indexes` again once the new binary is live:

```sh
bahia-event-archive --config /etc/bahia/config.yaml --action ensure-indexes
```

It runs `ALTER TABLE nostr_events VALIDATE CONSTRAINT
nostr_events_publish_state_check`, which holds only `SHARE UPDATE EXCLUSIVE`
(reads and writes continue) while it checks existing rows. The same run
builds `idx_nostr_events_publish_failed` concurrently (a partial index over
`publish_state = 'failed'` rows). It is idempotent; rerun it if interrupted.
Verify:

```sql
SELECT convalidated FROM pg_constraint
WHERE conname = 'nostr_events_publish_state_check';          -- expect t
SELECT indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
WHERE c.relname = 'idx_nostr_events_publish_failed';          -- expect t
```

### After deploying migration 000072 (Security `failed_retryable` retired)

Migration 000072 converts leftover `failed_retryable` Security publications to
`failed_terminal` (through the retry partial index, before dropping it) and
narrows `security_observable_publications_publish_state_check` and
`security_scan_runs_publish_state_check` as `NOT VALID`. The same
`ensure-indexes` run then converts leftover `failed_retryable` scan runs
(`security_scan_runs` has no `publish_state` index, so this is not done at
startup) and validates both checks under `SHARE UPDATE EXCLUSIVE`. Verify:

```sql
SELECT conname, convalidated FROM pg_constraint
WHERE conname IN ('security_observable_publications_publish_state_check',
                  'security_scan_runs_publish_state_check');   -- expect t, t
```

## Publish outbox health

- `bahia_nostr_outbox_depth`: pending outbound rows (all publish targets). The
  control-plane target carries the read-model projector, docs, SBOM and
  config-fabric; the default target carries daemon interop events. Sustained
  growth means a write relay is not accepting.
- `bahia_nostr_outbox_failed`: rows whose delivery was abandoned
  (`publish_state = 'failed'`, reason in `last_publish_error`). It reads `-1`
  until `idx_nostr_events_publish_failed` exists, and the daemon logs one
  warning naming `ensure-indexes`; it never counts by scanning the table.
  `BahiaNostrOutboxFailed` fires while it is above zero (see
  `docs/runbooks/ws6-alerts.md#bahianostroutboxfailed`). Every producer learns
  of the abandonment: a publish call whose first round already makes the
  quorum unreachable returns `ErrPublishAbandoned` (never the queued
  `ErrPublishIncomplete`), and a later abandonment by the runner reaches the
  publisher's `OnDeliveryAbandoned` handlers. Abandoned projector rows are
  republished by the next projector repair once relays accept again; Security
  publications (and their runs) become `failed_terminal`, SBOM manifests
  `failed`, config-fabric versions drop out of desired state, and docs are
  re-signed on the next sync. Nothing resets a failed row to pending.
- Per-relay acceptance is held in memory only. After a daemon restart each
  pending row is resent to every write relay, including relays that had
  already accepted it; they answer OK `duplicate:`, which counts as
  acceptance. The event is the stored signed event (same id), so this costs
  one extra EVENT frame per already-accepting relay per pending row and never
  creates a second copy. Persisting per-relay state was rejected because it
  would put relay topology into Postgres and add a write per relay per
  delivery round.

## Canary export

Start with the high-volume transport class and a small batch:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action claim-export --kinds 1059,21059,25910 \
  --older-than 168h --batch-size 1000
```

The result includes the batch UUID, local path, SHA-256, compressed byte size,
row count, and cutoff. A claimed/exported batch remains in PostgreSQL and is
safe across process crashes.

## Protect and confirm

Copy the artifact through the configured backup control plane. Record the
backend's immutable object URI and version. Then confirm the receipt:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action confirm --batch-id BATCH_UUID \
  --object-uri kopia://REPOSITORY/OBJECT \
  --object-version IMMUTABLE_VERSION
```

`file://` receipts and digest mismatches are rejected. Do not proceed until the
backup backend independently verifies the protected object.

## Canary prune and restore proof

Prune at most one bounded chunk per invocation:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action prune --batch-id BATCH_UUID --batch-size 1000
```

Repeat until `done=true`. Confirm outbox depth and relay delivery throughout.
Then perform the required restore proof:

```sh
bahia-event-archive --config /etc/bahia/config.yaml \
  --action restore --batch-id BATCH_UUID
```

Restore verifies the compressed artifact digest and inserts by event ID
idempotently. A second restore must report zero inserts.

## Rollback

Stop invoking `claim-export`; no daemon claims rows automatically. Never mark an
unprotected artifact protected. Claimed/exported rows remain live. Protected or
pruned batches remain restorable by immutable artifact. Revert the application
image only after confirming schema compatibility; the metadata tables and
nullable ownership column may remain safely in place.

## Required evidence

- backup and archive object versions;
- batch UUID, cutoff, kinds, row count, SHA-256, and compressed bytes;
- before/after hot bytes, live/dead estimates, oldest-hot timestamp, outbox
  depth, startup latency, and query plans;
- crash between claim/export and restart recovery;
- relay unavailability during the canary with pending rows preserved;
- idempotent restore and independent review.
