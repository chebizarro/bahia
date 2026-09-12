# Nostr event-store lifecycle

## Context and scope

Bahia's PostgreSQL `nostr_events` table is both an append-only audit record and
the durable outbound publish outbox. On 2026-09-12 the production relation was
19 GB (13 GB heap, 1.8 GB indexes, approximately 4 GB TOAST), with an estimated
12,046,760 live rows and 836,314 dead rows. The database was also 19 GB, so this
single relation dominated storage. A 0.01% block sample spanned 2026-08-01 to
2026-09-12 and was concentrated in kinds 30900, 25910, and 4903.

The lifecycle must bound the hot table without weakening the durable outbox or
silently discarding audit evidence.

## Existing-solution preflight

- PostgreSQL native range partitioning is the long-term hot-table shape, but an
  in-place conversion would rewrite the existing 19 GB relation. PostgreSQL
  also requires every partitioned unique constraint to include the time key,
  which conflicts with Bahia's global event-ID idempotency contract.
- `pg_partman` automates partition creation, not the global-uniqueness migration
  or protected archival receipt. It also adds an unavailable production
  extension dependency.
- TimescaleDB adds the same extension and migration risks.

The initial implementation therefore uses bounded keyset batches and immutable
archive artifacts. Native partitioning may follow after the event-ID registry
and shadow-table migration are separately designed and production-proven.

## Retention classes

Retention controls hot PostgreSQL residency, not evidence lifetime. Every row
leaving the hot table must first exist in a protected, versioned object.

| Class | Recommended initial hot window | Examples |
|---|---:|---|
| transport | 7 days | 1059, 21059, 25910 |
| replaceable state | 30 days | 30000-39999, including 30900 |
| audit | 90 days | 4903 and other append-only facts |
| outbox | indefinite until accepted | every `publish_state=pending` row |

The command requires either an explicit kind list or an explicit `--all-kinds`
acknowledgement. Operators may choose a longer cutoff. Shortening a class
requires a reviewed operational change and does not bypass protected-object
confirmation.

## Two-phase protocol

1. `ensure-indexes` creates archive and replay indexes with `CREATE INDEX
   CONCURRENTLY`; it is not part of Bahia startup migration.
2. `export` creates a manifest row and claims at most the configured batch size
   using `(received_at,id)` keyset order and `FOR UPDATE SKIP LOCKED`. Pending
   outbox rows are never eligible.
3. The claimed rows are encoded as gzip NDJSON into a mode-0600 temporary file,
   flushed, fsynced, atomically renamed, and hashed with SHA-256.
4. `confirm` re-hashes the local artifact and records a non-file protected
   object URI plus immutable object version.
5. `prune` deletes only rows carrying that protected batch ID, in bounded
   transactions. A crash before confirmation cannot delete data; retries are
   idempotent.
6. `restore` verifies the digest and inserts event IDs idempotently. Restore
   clears archive ownership so restored rows are again live.

The protocol intentionally prefers duplicate artifacts or rows after a crash
over any state in which the only copy can be deleted.

## Access paths and observability

Online indexes cover:

- pending outbox `(received_at,id)` (existing and preserved);
- archive eligibility `(received_at,id)` excluding pending/claimed rows;
- archive batch membership;
- recent kind queries `(kind,created_at DESC,id DESC)`;
- author-aware replay `(kind,pubkey,created_at DESC,id DESC)`;
- entity history `(entity_type,entity_id,created_at DESC,id DESC)`.

Metrics expose hot relation bytes, estimated live/dead rows, oldest hot event,
claimed/exported/protected batch counts, and pending outbox depth. Alerts should
fire on sustained hot-byte growth, a protected batch that cannot prune, oldest
hot age exceeding policy, or any pending outbox growth.

## Rollout and rollback

1. Take and verify a supported database backup.
2. Deploy code and metadata-only migration with archival disabled.
3. Run `ensure-indexes`; prove application writes and startup latency remain
   healthy while each index builds.
4. Enable export-only mode and validate artifact digest/restore in staging.
5. Protect one production artifact through the configured backup repository,
   confirm its object version, and prune a canary batch.
6. Compare relation bytes, startup latency, query plans, outbox depth, and relay
   delivery before increasing batch cadence.

Rollback disables new claims. Claimed/exported batches remain live until
protected, and protected batches may be restored idempotently. No rollback
step requires manual production SQL.
