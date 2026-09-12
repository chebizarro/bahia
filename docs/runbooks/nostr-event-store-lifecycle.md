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
closed until every index and the constraint are ready.

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
