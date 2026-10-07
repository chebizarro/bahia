# Postgres `nostr_events` lifecycle

When Postgres is configured, the `nostr_events` table is an append-only audit
record of published events and the durable index of the publish outbox. It is
a derived store: canonical state lives on relays and in the daemon's local
event store. This page is the contract for keeping the hot table bounded
without weakening the outbox or discarding audit evidence; the operator
procedure is `docs/runbooks/nostr-event-store-lifecycle.md` and the tooling is
`cmd/bahia-event-archive` (`-action ensure-indexes|claim-export|confirm|prune|restore|stats`).

## Retention classes

Retention controls hot Postgres residency, not evidence lifetime: every row
leaving the hot table must first exist in a protected, versioned object.

| Class | Hot window | Examples |
|---|---:|---|
| transport | 7 days | `1059`, `21059`, `25910` |
| replaceable state | 30 days | `30000-39999`, including `30900` |
| audit | 90 days | `4903` and other append-only facts |
| outbox | until accepted | every `publish_state = pending` row |

The archive command requires an explicit kind list or `--all-kinds`. Shortening
a class is a reviewed operational change and never bypasses protected-object
confirmation.

## Two-phase protocol

1. `ensure-indexes` creates the archive and replay indexes with
   `CREATE INDEX CONCURRENTLY` (not part of startup migration) and validates
   the constraints that startup migrations add `NOT VALID`: the archive
   ownership foreign key (migration 000062), the widened `publish_state` check
   (000071) and the narrowed security publish-state checks (000072, after
   converting leftover `failed_retryable` scan runs).
2. `claim-export` creates a manifest row and claims at most the configured batch in
   `(received_at, id)` keyset order with `FOR UPDATE SKIP LOCKED`. Pending
   outbox rows are never eligible.
3. Claimed rows are written as gzip NDJSON to a mode-0600 temporary file,
   fsynced, atomically renamed and SHA-256 hashed.
4. `confirm` re-hashes the artifact and records a non-file protected object URI
   and immutable object version.
5. `prune` deletes only rows carrying that protected batch id, in bounded
   transactions. A crash before confirmation cannot delete data; retries are
   idempotent.
6. `restore` verifies the digest, inserts event ids idempotently and clears
   archive ownership so restored rows are live again.

The protocol prefers duplicate artifacts or rows after a crash over any state
in which the only copy can be deleted.

## Indexes and observability

Online indexes cover: pending outbox `(received_at, id)` (publish runners
filter `publish_target` within this small partial set rather than indexing
it); archive eligibility excluding pending/claimed rows; archive batch
membership; recent kind queries `(kind, created_at DESC, id DESC)`;
author-aware replay `(kind, pubkey, created_at DESC, id DESC)`; entity history
`(entity_type, entity_id, created_at DESC, id DESC)`; and abandoned outbox rows
`(received_at, id) WHERE publish_state = 'failed'` (metrics only).

Metrics expose hot relation bytes, estimated live/dead rows, oldest hot event,
claimed/exported/protected batch counts, pending outbox depth and the failed
outbox row count (`-1` until its index exists). Alerts fire on sustained
hot-byte growth, a protected batch that cannot prune, oldest hot age beyond
policy, pending outbox growth, or any increase in failed rows
(`docs/runbooks/ws6-alerts.md`).

Native range partitioning is the long-term hot-table shape, but it requires
the time key in every unique constraint, which conflicts with the global
event-id idempotency contract; it is not adopted until an event-id registry and
shadow-table migration are designed separately.
