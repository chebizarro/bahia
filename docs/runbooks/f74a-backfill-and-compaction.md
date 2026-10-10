# F74a backfill and observation compaction

This procedure verifies the derived PostgreSQL source and canonical F74a
publication, and measures potential redundant historical runtime samples.
Normal daemon startup and PostgreSQL reconnect do not launch a legacy F74a
import. A populated SQL index does not authorize canonical publication. Run
any legacy import only through an explicit, separately admitted migration
command after a dry-run census; if the candidate image lacks that governed
command, stop rather than using daemon restart as a substitute.

The explicit `bahia-migrate --config "$CONFIG" --confirm-quiesced f74a-import`
requires the daemon and all SQL writers to be stopped. It uses the daemon's
exclusive local event store and outbox. Its completion marker, F74a delivery
receipts, and fleet OCK manifest are scoped to the service signer, effective
control-plane write-relay set, and publish quorum. Relay ordering does not
change that scope. Changing the write set or quorum reopens completion and
requires fresh relay `OK` proof for the retained signed events; historical
ACK flags alone do not suffice. A policy-unscoped legacy receipt is not proof.
If the signed event is no longer retained, or a current relay refuses it,
the import remains incomplete. Restore the original local event store/outbox
from a verified backup or resolve the relay refusal before retrying; do not
edit the receipt or completion marker to bypass verification.
**Confirmed deletion and live compaction are not available.** The shipped
`f74a-census`, `f74a-compact --cutoff`, `f74a-restore-preflight --cutoff`,
and `f74a-verify-receipt` actions are read-only;
`f74a-compact --confirm` is rejected. There is no `--batch-size` or
`--backup-id` flag. Do not use another tool or manual SQL to bypass this
guard. The archive repository has bounded, transactionally checked batches
and protects archived successors of backdated writes. The signed receipt
verifier can authenticate an independent attestor claim for this exact
PostgreSQL database, but it cannot verify live backup object retention,
revocation, or credential recovery before each deletion batch. Generic `backup_runs` and `backup_restores` records can describe
unrelated workload targets; their success is not a database backup receipt.
The deletion gate remains blocked until the attestor is operational and
current object custody and credential recovery can be independently rechecked
in a transactional, restart-safe bounded deletion path. This runbook
applies only to a Bahia image exposing both read-only actions. If either
action is absent, stop: the older image cannot perform this procedure. PostgreSQL is a
derived index; preserve the service key, relay-held canonical records, and
local event store/outbox when backing up or restoring the daemon.

This procedure never archives or deletes `nostr_events`. That table contains
multiple kinds of inbound and outbound events and has its own
[event-store lifecycle](nostr-event-store-lifecycle.md) procedure. Do not
substitute manually authored production SQL for the commands below.

Archived observations keep their original deployment-unit identity. Removing
an active unit with archived observations retires it into an immutable
PostgreSQL tombstone; a unit without archived history is deleted. Active unit
lookups omit retired units, while an archived observation can still be restored
with its original foreign key. The archive-to-unit foreign key rejects environment
deletion that would cascade through a referenced unit. If migration reports
orphan archived unit IDs, stop and restore the original unit rows and their
parent environments from a verified backup; do not invent replacement unit
metadata or bypass the guard.

## 1. Establish a baseline

Use the candidate image and record its digest, Bahia commit, schema version,
service-key identity, deployment topology, and relay set. Confirm that only
one authority for this signing key/local outbox runs the backfill. Record
`/health`, `/ready`, `canonical_delivery`, outbox pending/failed counts,
relay acceptance/refusal state, and process RSS before work begins. A ready
HTTP response means the daemon serves traffic; it does not prove that F74a
backfill completed or that relays accepted every staged event.

Choose one UTC cutoff and keep it unchanged through the rehearsal. A cutoff
is a boundary for eligibility, not permission to delete every older row.

```sh
CUTOFF=2026-10-01T00:00:00Z
CONFIG=/etc/bahia/config.yaml
bahia-migrate f74a-census --config "$CONFIG" --cutoff "$CUTOFF"
```

The read-only F74a commands have a 30-minute deadline; use
`--f74a-timeout 1h` for an approved larger census (maximum 24 hours).
Deadline expiry cancels the SQL snapshot without advancing archive state.

Capture the structured census result and command exit status. Distinguish
physical package rows from distinct semantic package coordinates and
duplicates; historical observations from state-linked current observations;
and material transitions from suppressible no-op samples. Do not equate 1.5
million observations with 1.5 million canonical publications: only the
state-linked current observation for each state is a backfill source. Check
that the reported cutoff is exactly the requested cutoff. Investigate any
foreign-key or coordinate mismatch before interpreting a dry-run estimate.

## 2. Rehearse backup and restore

Take a consistent, verified backup through the approved PostgreSQL backup
control plane, and back up Bahia's local event store and outbox through their
normal durable-volume procedure. Record the immutable backup reference,
backup completion receipt, cryptographic digest, retained object/version, and
the exact database/schema identity. A backup job merely reporting success is
not a restore proof.

Run the bounded inventory preflight at the same cutoff on the quiesced
source, restore the backup into an isolated database, then run it there:

```sh
bahia-migrate --config "$SOURCE_CONFIG" --cutoff "$CUTOFF" f74a-restore-preflight
bahia-migrate --config "$STAGING_CONFIG" --cutoff "$CUTOFF" f74a-restore-preflight
```

Compare `unauthenticated_inventory_sha256`, schema-version count, observation
count, archived-row count, state-link count, and hot candidate count. The
inventory covers every historical observation value, hot/archive placement,
exact state-link row identities, archive batch/run journals, original unit identity/retirement and schema version, in bounded keyset pages within a single read-only SQL snapshot,
with journal timestamps normalized to UTC. An integrity mismatch aborts the preflight. A matching
hash detects an inconsistent restore but **does not authenticate its source**:
the output always says `deletion_authorized false`. `database_name` is only a
diagnostic hint; a logical restore may use a different name. Capture both
preflight outputs with the independently signed backup object digest, exact
source database/schema identity, backup retention/credential status, and an
attested isolated-restore result bound to the same cutoff. Generic backup
run/restore success or an operator-supplied reference is not that evidence.

A separately operated backup attestor may sign a `bahia-f74a-backup-restore-v1`
receipt with a **separately pinned Ed25519 public key** configured as
`db.f74a_backup_attestor_public_key` (or
`BAHIA_DB_F74A_BACKUP_ATTESTOR_PUBLIC_KEY`). Do not use the Bahia service
signing key as this pin. The signed payload binds the source PostgreSQL
cluster system identifier, database OID/name, fixed cutoff, snapshot ID and
SHA-256 object digest, source and isolated-restore inventory digests, distinct
restore database identity, chronology, and expiry. The JSON envelope has `payload` and a hex `signature`. The signature is
Ed25519 over the UTF-8 bytes `bahia-f74a-backup-restore-v1` followed by one
NUL byte and the **exact JSON bytes of `payload` as embedded in the envelope**.
The payload fields are `version`, `receipt_id`, `source_database`,
`restore_database`, `cutoff`, `snapshot_id`, `backup_object_ref`, `snapshot_created_at`,
`backup_object_sha256`, `source_inventory_sha256`,
`restore_inventory_sha256`, `restore_verified_at`, `issued_at`, and
`expires_at`. Database identities contain `name`, `oid`, and
`system_identifier`; SHA-256 digests are lowercase 64-character hex, and
timestamps are RFC3339. Unknown fields, trailing JSON, expired receipts,
non-distinct source/restore identities, and changed local inventories are
rejected. Only the independent attestor signs; a supplied JSON path cannot
override the configured public key. The maintenance database
role must be able to read `pg_control_system()` (for example via `pg_monitor`);
missing privilege is a hard failure rather than a fallback to a configured
DSN or database name. With the attestor's signed JSON receipt at a local path:

```sh
bahia-migrate --config "$SOURCE_CONFIG" --f74a-receipt "$SIGNED_RECEIPT" f74a-verify-receipt
```

The verifier checks the signature against the configured pin and re-reads
the physical source database identity and full inventory. For a receipt whose
signed `backup_object_ref` is a local `file:///absolute/path`, add
`--f74a-verify-object` to open that exact regular file and stream its actual
bytes through SHA-256 under the F74a deadline. The option fails closed for a
missing, changed, unreadable, expired, or non-local object, and reports
`backup_object_hash_verified true` only when the observed digest matches the
signed digest. It uses bounded memory but reads the entire object. Do not
substitute an operator-provided path: the path comes from the signed receipt.

This is a **read-only point-in-time verification**, not deletion admission.
A local file hash cannot prove independent custody, future retention, a live
revocation check, or restore-credential recovery; other object-store URI
schemes have no verifier yet. The command always reports
`revocation_status_verified false` and `deletion_authorized false`, even when
the local object hash matches. `f74a-compact --confirm` remains rejected.
A verified receipt can describe unarchived hot no-op samples; it does not
authorize their deletion. The future bounded delete path must recheck an
immutable archive copy, its digest, the material predecessor, current state
links, and the signed receipt/custody status under its transaction before
each batch, with a durable restart cursor.
The attestor must operate outside Bahia and must verify actual backup object
custody and an isolated restore before signing. Do not hand-author or
self-sign a receipt with Bahia's service key.

Run `f74a-census` on the isolated staging database
with the same cutoff and compare counts and a sample of retained state links
and material transitions to the source receipt. Confirm the restored daemon
uses a staging key and isolated relays, or leave it stopped; never let a
restored production key publish concurrently with the live authority.

The repository's isolated PostgreSQL 16 rehearsal exercises the archive schema
and repository on a disposable Docker container. It creates a unit with two
archived observations, retires the unit, rehydrates one state-linked observation,
then takes a custom-format `pg_dump`. It first forces a `pg_restore
--single-transaction` collision and checks that no partial archive remains;
it then restores into the clean database and compares archived IDs, row digests,
original unit foreign keys, retired-unit status, hot-row presence, and state
links. On the restored copy it rolls back an attempted rehydration, then commits
one and verifies the archive remains unchanged.

```sh
BAHIA_F74A_RESTORE_CONFIRM=disposable \
  go test -tags=integration ./internal/repository \
  -run '^TestF74aPostgres16BackupRestoreAfterUnitRetirement$' -count=1 -v
```

Docker must be available and able to run `postgres:16-alpine`. The test owns
and removes its container and databases; it does not accept a production URL.
Capture the test exit status and printed dump SHA-256 digest. This repository
proof does not substitute for a receipt from the approved backup control plane,
an isolated restore of actual operational data, or the staging checks below.

## 3. Run the read-only compaction estimate

On the restored staging database, run only the read-only estimate:

```sh
bahia-migrate f74a-compact --config "$STAGING_CONFIG" --cutoff "$CUTOFF"
```

The `hot_suppressible_observations_before_cutoff` result identifies the exact
physical candidates in one repeatable-read snapshot: **unlinked, older no-op
observation samples** while preserving the first row of every material run, every
state-linked row, and rows newer than the cutoff. It also reports duplicate
packages; package physical deletion is disabled. The historical
`suppressible_observations_before_cutoff` count includes archived rows and is
not a physical deletion count. Record the estimated count,
cutoff, and a sample of preserved forensic transitions. An unexpected count,
an absent cutoff, or a proposed state-link deletion is a stop condition.

There is no executable confirmed-deletion command. Concurrent writes can
invalidate a dry-run decision, and read-only signed receipt verification
does not prove current backup object custody or authorize deletion. Confirmed batching through the
operator command, post-deletion census, and a second restore from the same
operational backup remain **unmet acceptance checks**. Do not use repository
tests or a generic workload backup record as a substitute for this gate.

## 4. Exercise the scaled integration fixture

Use a dedicated disposable PostgreSQL database with enough disk and WAL
capacity for 20,000 package rows and 1,500,000 observations. The fixture
migrates that database, inserts two physical rows per semantic package, links
one current observation, verifies counts and a bounded state-linked query,
and rolls back its data transaction. Migration changes are not rolled back.

```sh
BAHIA_F74A_SCALE_DATABASE_URL="$DISPOSABLE_DATABASE_URL" \
BAHIA_F74A_SCALE_CONFIRM=disposable \
go test -tags=integration ./test/integration \
  -run '^TestF74aScaleFixture$' -count=1 -v
```

The fixture validates **data shape**, not throughput, memory ceilings, relay
ACKs, or compaction correctness. Capture its runtime, database size/WAL,
query plans for the candidate repository's package and state-linked
observation keyset reads, and peak process RSS. A skip without the two
explicit environment variables is not a pass.

## 5. Docker/staging soak and restart proof

Use the real candidate image and the scaled fixture in isolated staging. This
is an external acceptance gate, not a conclusion from a unit test. Record a
single evidence bundle containing image digest, schema version, dataset
counts, timings, peak RSS, outbox high-water mark, canonical-coordinate
counts, health transitions, and relay `OK` outcomes. Exercise these
boundaries without replacing relays with a mock:

1. Start the daemon with the scaled SQL source and verify `/ready` and
   canonical publication count do not depend on its cardinality. Observe
   migration progress through the explicit command's independent status,
   not daemon readiness.
2. Interrupt and restart the explicit migration during each keyset phase, including after an event
   is durably queued but before the cursor advances. Confirm resumed cursors,
   unchanged semantic queued cardinality on retry, and eventual relay
   acceptance. Distinct legacy-coordinate tombstones count separately.
3. Cause relay refusal, authorization challenge, and a pending-outbox
   admission pause. A refusal must not be reported as acceptance or advance
   completion; after recovery, delivery and the completion marker must
   converge without duplicate semantic coordinates.
4. Write a new package/state transition while the pass is active. Confirm
   the dirty generation forces a complete recheck before completion.
5. Rehearse the backup, isolated restore, and read-only estimate from
   steps 2–3. Record confirmed batching, post-deletion census, and rollback
   from a compacted backup as blocked, not passed.

Record which checks were observed and which could not run. Do not mark the
F74a rollout accepted or production-ready from a skipped fixture, an
unavailable staging environment, a queued-but-unacknowledged outbox event, or
an unproved restore. **Live compaction is blocked** even if all read-only
checks pass. Its future gate requires concurrency-safe deletion, confirmed
bounded batches on an isolated restore, post-deletion census, a second
restore proving rollback, and explicit operator approval.
