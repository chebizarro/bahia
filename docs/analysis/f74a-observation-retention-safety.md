# F74a observation-history retention safety

**Decision:** `bahia-migrate f74a-compact` remains a read-only dry run. Its
`--confirm` refusal must not be removed on the strength of the current census
or a delete-time check of `environment_service_state.current_observation_id`.
No confirmed compaction or backup/restore path is implemented by this note.

## Why the current candidate set is not safe to delete

The census orders observations by `(service_id, environment_id, observed_at,
id)` and calls an older unlinked row suppressible when it equals its preceding
material state. Given `A@t1, A@t3`, `A@t3` is a candidate. A later accepted
`B@t2` makes `A@t3` the return transition of `A→B→A`. A fixed cutoff and a
second candidate check immediately before `DELETE` do not prevent a write after
that transaction from changing this classification. The existing
`TestF74aDryRunReclassifiesBackdatedMaterialTransition` demonstrates this
without executing a delete.

The problem is broader than transition classification:

- `RegistryService.RecordObservation` accepts an observation older than the
  latest row, persists it, and deliberately does not advance current state.
  Backdated writes are part of the current behavior, not an invalid input.
- Reconciliation may persist an observation and fail while updating its state
  link. On retry it reuses the unlinked row. Its ID therefore remains eligible
  for a future `environment_service_state.current_observation_id` foreign key
  even though it was unlinked during a compaction scan.
- `PgRuntimeObservationRepository.GetByID`, `GetLatest`, and
  `ListByServiceEnv` read only `runtime_observations`. Removing a row changes
  reconciliation, registry ordering, and historical-read semantics unless all
  those reads have a transparent archive path. `UpsertObservation` compares
  current and incoming rows in that table; an archive-to-unit foreign key and
  an immutable retired-unit row retain placement identity for hot restore.
- The state foreign key points at `runtime_observations`, not an archive. A
  later state link to an archived row would fail unless that row is restored
  into the hot table *inside the linking transaction*.

The PostgreSQL rows are derived, but relay-canonical state does not prove that
every historical observation sample can be rebuilt byte-for-byte. The current
F74a backfill publishes state-linked observations, not the entire history.
Neither relay replay nor a count-only dry run is an observation-history backup.

## Preferred design: lossless archive with transparent recovery

The archive must be a first-class, queryable copy of **every column** of a
moved observation, keyed by the original UUID, with immutable batch identity,
archive time, and an exact row digest. Preserve original timestamps, JSON,
nullable fields, and deployment-unit identity. An archive row must never be
removed by service/environment/deployment-unit cascading cleanup. Audit access
must expose the original row by ID and ordered history, including its archive
provenance. This design reduces the hot table and its indexes, not total
database storage; a separate durable storage tier is a later policy choice.

The following invariants must be enforced by database constraints/triggers and
repository reads, not merely by the maintenance command:

1. For each observation ID, exactly one original row value is recoverable from
   hot or archive storage. A temporary copy in both locations during
   rehydration must have identical values. An ID/value mismatch is an error,
   never `ON CONFLICT DO NOTHING` without verification.
2. Every state-linked row is present in the hot table when the linking
   transaction commits. A database `BEFORE INSERT/UPDATE` state-link trigger
   must rehydrate an archived target before the existing foreign key fires.
   A repository-only hook is insufficient for direct SQL and other writers.
3. `GetByID`, `GetLatest` (with `(observed_at, id)` tie ordering), ordered
   history, census/material-run classification, and deployment-unit reference
   checks see the union of hot and archive rows, deduplicated by ID. The F74a
   state-linked backfill continues to resolve exact current IDs from hot rows.
4. A backdated insert cannot make a historical transition disappear: union
   reads immediately classify `A@t3` as the return transition after `B@t2`.
   If product policy requires every material transition to reside *hot*, an
   insert-time neighbor reclassification and promotion trigger is additionally
   required; archive-only visibility is not a substitute for that decision.
5. A batch never destroys its only copy. Archive insert, identical-row
   verification, hot deletion, and cursor/count update commit in one database
   transaction. A crash before commit changes none of them; a crash after
   commit changes all of them. A unique archive ID and committed journal make
   retry idempotent.

Use the next available additive migration for an archive table, archive
identity/digest constraints, a compaction-run journal, supporting
`(service_id, environment_id, observed_at, id)` indexes on both tables, and
the state-link rehydration trigger. Do not modify the initial schema. The run
journal fixes the cutoff at creation, records a bounded batch size and the
exclusive composite cursor, and advances the cursor only in the same
transaction as a successful move. Each batch rechecks current state links and
material neighbors. A write behind an already committed cursor remains in
the hot table until another pass; it is not silently counted as compacted.
Use database-level coordination for the archive move and state-link trigger
on the same observation ID, and test both lock orders. An unverified lock
protocol is not a safety argument.

Repository changes must cover every SQL reader of `runtime_observations`,
including `PgRuntimeObservationRepository`, census, `UpsertObservation`, and
`DeleteIfUnreferenced`. `UpsertObservation` may keep its hot-table comparison
only after the trigger guarantees that both its incoming and current linked
IDs are hot. The archive interface should provide an explicit audited
`GetArchivedByID`/ordered history route and a restore operation for disaster
recovery; normal reads should not require callers to know storage location.
The CLI remains dry-run by default and must not accept `--confirm` until this
entire migration/interface/test slice lands. Package-row deletion has a
separate semantic-coordinate/tombstone proof obligation and must stay disabled.

## Alternative: a sealed cutoff

A simpler physical delete is possible only if a database-enforced retention
seal makes old history immutable. The seal must be committed before the first
delete; all writers, including direct SQL, replay, and imports, must reject
`INSERT` or `UPDATE` that creates or moves an observation at or before the
sealed cutoff. State links to candidate IDs must also be blocked or otherwise
made safe before a row can disappear. The seal and every batch's fixed cutoff
must be durable and monotonic across restarts, with a batch journal and
delete-time protection of linked and material-transition rows.

This alternative changes current accepted behavior: legitimate backdated
events would fail to project into PostgreSQL. It requires an explicit product
and canonical-replay decision, plus a demonstrated alternate recovery path for
those events. A repository-only timestamp check cannot enforce the seal. Do
not select this alternative by default merely because it is easier to code.

## Required proof before enabling `--confirm`

Run against a disposable PostgreSQL instance with real migrations and
repository APIs; SQL mocks and a scale fixture that only counts rows do not
prove retention safety.

| Gate | Required assertion |
| --- | --- |
| Historical equivalence | Before/after `GetByID`, `GetLatest`, ordered history, census/material runs, and deployment-unit guards return the same logical results and exact row bytes. |
| Backdated writes | Compact `A@t3`, insert `B@t2`, and recover the `A→B→A` history; also insert behind a committed cursor and show it is not lost. |
| Delayed state link | Fail a state write after observation insertion, compact the still-unlinked row, retry the link, and prove the FK, row content, and F74a state-linked read succeed. |
| Concurrent lock orders | Race archive move against direct SQL state linking and backdated insertion in both orders; no broken FK, missing row, deadlock, or silently skipped error. |
| Restart boundaries | Inject termination before archive insertion, after insertion but before deletion, before commit, and after commit before caller acknowledgement; retry with the same run ID/cutoff and assert exact once-only rows and journal counts. |
| Bounded work | At 1.5 million observations, batch size and memory remain bounded; `(service, environment, time, id)` plans use the intended indexes; cancellation stops at a committed boundary. |
| Backup and restore | Take a real database backup before compaction, restore into an isolated database, compare row IDs and cryptographic row digests, execute compaction, restore again after a forced partial run, and prove original hot/archived history and state links are recoverable. Record backup identity, checksum, restore result, and database identity in the operator evidence. |
| Operational rehearsal | Run the shipped command and rollback procedure on the scaled staging image; record timing, disk/WAL growth, query plans, and health. Do not treat an unavailable staging environment as a pass. |

Until these gates pass, the precise F74a acceptance gap is **confirmed,
restart-safe, backed-up observation-history compaction**. The read-only census
and dry run remain useful for sizing, but their candidate count is a snapshot,
not a deletion authorization.
