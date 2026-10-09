# Relay-canonical startup and recovery

Design input: Manifest task `bahia-postgres-canonical-dependency-removal-20261008`
(revision `5e0a78f9e32c4e7ffe565ba9fba311bfde4588968f2ede3f45566fd716f72b95`).
This is a disposition and implementation design, not evidence that the code or
acceptance gates have changed. The companion source audit should be used as the
complete hook inventory.

## Authority boundary

Normal boot may hydrate the daemon's bbolt event store from configured relays,
resume its **local** signed-event outbox, rebuild PostgreSQL *from* validated
canonical events, and reconcile observed runtime state against canonical
desired state. It must not treat SQL row existence, queue status, or a missing
SQL-to-relay marker as authorization to sign an event or execute work. SQL
cardinality must not determine readiness latency or startup publication count.
This does **not** prohibit derived-index reads for optional UI/query features,
telemetry, or an index rebuild. A derived row used by a control-plane decision
must carry a source event id/coordinate and be checked against the current
canonical version; SQL alone cannot be a publication trigger.

Canonical ingestion retains the existing rules in
[`event-lifecycle.md`](../architecture/event-lifecycle.md): validate id,
signature and tags; resolve addressable versions by latest `created_at`, lowest
id on ties; apply authorized NIP-09 and NIP-40; dedupe by event id and intent
id. Use narrow long-lived REQs, persisted cursors, EOSE and NIP-77 completion
before declaring a family caught up. A warm local snapshot can serve reads,
but side-effecting reconcilers wait for the relevant catch-up barrier. Relay
failure can leave readiness false under the existing relay-quorum contract;
PostgreSQL failure cannot. Service-authored output still enters the local
outbox before delivery, with per-relay OK and `canonical_delivery` reporting
per [`outbox-delivery.md`](../architecture/outbox-delivery.md).

## Hook disposition

| Normal-path source (current code) | Disposition |
| --- | --- |
| `BootstrapF74aCanonical` in `internal/app/app.go`; adoption, Hive-CI, policy, security and initiation `BackfillFromIndex` post-warm hooks; `LocalManagedInstanceState.BackfillFromIndex` in `ManagedInstanceSupervisor.Run` | **Remove from daemon boot and database recovery.** Keep only an explicit governed legacy-import command if required. Post-warm timing and once markers do not make SQL-derived publication canonical. Keep the separate `RebuildIndex` direction, but ensure it reads canonical state, runs as optional bounded index work and cannot gate core readiness. |
| `Publisher.MigratePendingPostgresRows` in `App.New` | **Move to explicit outbox-transfer migration**, not a state backfill. These may be already-signed, undelivered events rather than rebuildable index rows. Preserve exact event ids/signatures, target, per-relay OK state and durable transfer cursor; never re-sign from SQL or silently discard an untransferred pending event. Inventory and drain/transfer must be a deployment prerequisite before retiring the old outbox. |
| `BootstrapLocalDNS` and `BootstrapLocalML` in `App.New` | **Remove automatic SQL seeding** of local control records. Hydrate desired DNS/ML records from canonical events; put any historical SQL import behind the same explicit migration gate. Config-declared DNS zones/backends and Hive policies need a clearly authorized config-fabric or signed-intent path, not an accidental SQL precedence rule. |
| `databaseRecoveryRunner` | May reconnect/rebuild an optional index, but must not restart into any of the above imports, replay SQL queue work, or change core readiness. Prefer enabling index consumers without replacing the canonical read model; if a process restart remains necessary, prove the restart cannot publish from SQL. |
| `reconcile.Reconciler`, `DNSProjector`/`DNSReconciler`, stale-run detector | **Retain behavior, replace SQL authority.** Enumerate canonical desired states and run/status records from the relay-hydrated local read model. Runtime probes and Loom/agent status subscriptions supply fresh observations; a material observation or canonical desired-state change may cause output. A stale timer is a deadline over a canonical run/status record, not a scan of a SQL-only run. Reconciliation must not re-publish solely because an index row is present or missing. |
| `Coordinator.RecoverNonTerminalRuns`; Hive pending-result resumer; LLM, ML, backup/restore/retention and other PG-backed recovery runners | **Retain crash recovery, redesign the durable work source.** Accepted signed intents, canonical run/result/checkpoint records and the local event store determine eligible work. Reattach to external jobs by stable job/run id; execute missing steps idempotently; persist a canonical progress transition through the outbox before advancing the derived index. Do not turn a legacy SQL `queued` or `approved` row into a fresh action on startup. |
| Managed-instance and route-canary supervisors' ongoing observation | Keep their local canonical-state and configured-spec inputs and fresh probes. Remove only SQL ledger/maintenance backfill. SQL health/history rows remain write-behind; a PG advisory-lock fallback is not a cross-daemon execution fence, so multi-daemon recovery needs a canonical lease/fencing or an enforced single-writer topology before side effects. |

The PostgreSQL RBAC fallback described in
[`intents-and-authority.md`](../architecture/intents-and-authority.md#21-trustset)
also needs an explicit retirement or tightly governed bootstrap compatibility
decision: a divergent index must not grant authority that canonical membership
does not. This is an authorization boundary, not a reason to block core startup.

## Workflow recovery authority

The daemon does not register the SQL-scanning backup run, restore, retention,
schedule, LLM provisioning and route repair, or tool provisioning recovery runners. Health reports
each paused family as degraded and warns operators that retained SQL work must
not be replayed without signed-intent provenance. This does not erase queued
rows. Backup and tool requests delivered through their signed request handlers
can still be admitted directly. Backup restore and tool manual approvals are
paused even for otherwise valid requests: SQL-derived restore metadata and
resolved tool packages can change execution inputs after the signed request,
and the existing approval mutation can publish before a second provenance
check. Automatic schedule dispatch and LLM provisioning remain unavailable
until their canonical intake and recovery sources exist.
This pause is a **release blocker for the affected features**, not a completed
recovery replacement or evidence that persisted work will finish after restart.

Recovery requires a durable per-workflow record linking the validated source
intent event id and intent id to a stable run id, external job id/idempotency
key, accepted/running/terminal status, and last completed step. Backup
definitions and schedules also need canonical coordinates and a signed dispatch
intent tied to the schedule version and due instant; a SQL `next_run_at` is not
authorization. LLM and tool requests must be reconstructed from retained signed
intents, not their PostgreSQL `queued` or `approved` rows. Replay must wait for
family catch-up, resolve latest-winner/tombstone state, deduplicate by event and
intent id, then reattach by stable external id before executing any missing
step. Progress must enter the local outbox before PostgreSQL projection.

Approval restoration additionally requires a retained, validated original
request event (not merely an ID copied into SQL), canonical binding of every
effect-bearing input including restore source and resolved tool package/source,
and an atomic compare-and-commit of request, approval, execution inputs and
outbox outcome. If the original request cannot be recovered or any input
differs, leave the row pending with an operator-visible refusal; do not mint a
new service receipt from SQL metadata. Tests must race approval against SQL
mutation, inject fabricated rows and mismatched metadata, and prove zero
executor calls and signed outcomes for every refusal.

For each family, deterministic acceptance tests inject canonical EVENT and
EOSE (including duplicates and divergent SQL), restart after acceptance,
external dispatch, progress publication and terminal publication, and assert
one external job and one semantic outcome. Repeat with PostgreSQL absent,
empty and populated by SQL-only rows: only the canonical case may execute.
Inject relay OK refusal, AUTH and CLOSED and verify the work stays visibly
degraded instead of advancing the SQL queue. Test two daemons against the same
intent with canonical fencing or enforce an explicit single-writer topology.

## Implementation order

1. **Stop boot authority first.** Remove all automatic SQL-to-canonical
   backfills and pending-outbox transfer from `App.New`, projector warm hooks,
   supervisor startup and DB recovery. Split optional derived-index runners
   from required `background_runners`; make `bootstrap_ready` and
   `intent_readiness` depend on relays/local state, not an SQL scan. Keep
   ordinary local outbox redelivery. Add a static architecture gate that rejects
   a PostgreSQL source-to-publisher edge in normal startup/recovery wiring.
2. **Preserve legacy data deliberately.** Ship an opt-in, operator-authorized
   import/transfer workflow separate from the server command. Dry-run report
   physical rows, distinct semantic coordinates, canonical conflicts and
   pending signed outbox entries. Use stable `(kind, service pubkey, d)`
   coordinates, deterministic source ordering, bounded keyset pages, durable
   per-family/source cursor and dirty-generation recheck, admission limits,
   explicit relay OK outcomes, and independently queryable progress. On a
   divergent coordinate, never overwrite relay truth automatically; report a
   conflict for governed resolution. Crash after outbox enqueue but before
   cursor advance must replay without duplicate semantic output. No boot,
   reconnect or database-recovery path may invoke the command.
3. **Replace live SQL decision sources family by family.** Build reusable
   canonical local read models for desired services/deployments, DNS,
   supervision, runs and workflow checkpoints; wire EOSE-aware subscriptions
   before activating each reconciler. For each workflow, define signed intent,
   accepted/running/terminal observables, stable external idempotency key,
   retry/lease ownership and crash boundary. Keep PG projection best-effort,
   and compare any SQL acceleration against source event version. Remove the
   corresponding SQL-driven runner only once event-driven recovery tests pass.
4. **Prove and ratchet.** Add source/dependency tests for `App.New`,
   `databaseRecoveryRunner`, warm hooks and runner registration; document the
   recovery runbook and readiness semantics. Then run the task's full build,
   vet, test, race, Docker, architecture-lint, independent-review and staging
   soak gates. A passing unit test alone is not a production acceptance claim.

## Acceptance tests and measurements

Use the same relay fixture, service key, local-store snapshot and configured
feature set across PostgreSQL **absent**, unreachable, slow, empty, populated
with redundant legacy rows, and divergent-from-relay cases. Measure time to
HTTP `/health`, `bootstrap_ready`, `intent_readiness`, `/ready`, first served
canonical record, outbox pending/failed counts and relay-accepted event ids.
Set a normal-health-window bound before testing. Vary SQL cardinality from zero
to the scaled fixture; readiness distribution and SQL-triggered publish count
must not change. Expect **zero** canonical publications attributable solely to
SQL rows. Count unavoidable identity/readiness publications, replay of
pre-existing local outbox entries, and fresh material runtime observations
separately rather than asserting total output is zero.

- With PostgreSQL absent/slow, core relay/local reads and intent processing
  reach readiness after EOSE/NIP-77 under the same bound; optional index
  features report degraded/503 without blocking core. If relays themselves
  are unavailable, `/health` responds promptly but `/ready` follows the
  existing relay-quorum/EOSE failure semantics.
- With redundant or divergent SQL, relay/local latest-winner and tombstone
  state wins. A stale SQL value cannot resurrect an entity, grant access,
  schedule a run, trigger DNS change, or publish a replacement. Rebuilding
  the index converges it to canonical events; it does not reverse the flow.
- Inject EVENT, EOSE, OK, CLOSED and AUTH; cover reconnect, equal-timestamp
  tie, NIP-09/NIP-40, duplicate event/intent, rejected and partially accepted
  publication, admission pause, outbox crash/restart, and SQL failure during
  projection. Assert exact coordinate/event-id and side-effect counts.
- For each recovery family, restart at accepted intent, external dispatch,
  in-flight status and terminal publish boundaries. Prove reattachment and
  eventual completion from canonical checkpoints with PG absent, and no
  duplicate external job or signed outcome. Multi-daemon tests must prove
  fencing rather than rely on a process-local lock.
- For opt-in migration only, assert dry-run counts, conflict reporting,
  bounded memory/page sizes, stable coordinates, no boot invocation, cursor
  resume after each crash boundary, no duplicate semantic publication, and
  relay ACK/admission/refusal accounting. A queued event is not a successful
  relay publication; incomplete or abandoned delivery remains observable.
