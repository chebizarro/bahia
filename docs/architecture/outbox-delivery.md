# Outbox delivery and abandonment

Every canonical event the daemon signs goes through the **local outbox**
(`internal/adapters/nostr/localstore`, bbolt) before any relay attempt. The
outbox is what makes "the daemon published X" durable across crashes, and this
page is the contract for what happens when a relay quorum can no longer be
reached. It binds every domain that publishes through the outbox, not only
payments and security.

## Durable publish

- `Publisher.PublishProjection` writes the signed event to the outbox first;
  per-relay `OK` tracking is persisted, so a publish interrupted mid-delivery
  resumes exactly where it stopped after a restart.
- Relay-settings `30900` state, `4903` audit facts, and service `10050` DM
  relay lists use the service publisher's outbox and current service write
  relays. A queued policy change may reconfigure those relays before its
  next delivery round; the pending event then retries against the new set.
- The caller-facing publish calls (`PublishSignedEventWithResults`,
  `PublishProjection`, `publishCanonicalFirst`) run one delivery round inline
  and return:
  - `nil` — the quorum accepted the event;
  - `ErrPublishIncomplete` — fewer relays than the quorum accepted, the event is
    durably queued and the outbox runner keeps delivering it;
  - `ErrPublishAbandoned` — the quorum became unreachable in this round.
- The outbox runner abandons an entry when the quorum is unreachable
  (permanent `blocked:` / `invalid:` / `pow:` rejections) or when the bounded
  attempt budget is spent. Abandoned entries are terminal `failed` rows,
  retained for `failedOutboxRetention` (7 days) with their reason.
- The currently disabled backup-run admission path would stage its initial
  service-signed state and immutable request keys in one outbox transaction.
  Its separate admission record keeps the exact state event ID and quorum-ACK
  result after a settled entry is pruned. A pending or failed entry does not produce an accepted run intent;
  the same signed request can be replayed after delivery to report acceptance.

For service-signed backup recipe, repository, policy, and run-state `30900` events,
and deployment-policy registry `30900` events, the outbox also records a
non-prunable delivery proof in the same transaction as
the publisher's verified quorum-reaching round. `PublishProjection` and
`EnqueueBackupRun` admit exact signed events before relay I/O.
`PublishBeforeCommit` makes its relay attempt before admission and is not
suitable for a crash-safe import; a policy proof therefore does not imply
outbox-first admission. Verified quorum outcomes are committed before success
is reported; a crash before commit leaves a retryable row without acceptance
proof. Generic enqueue and round calls cannot mint a proof.
It pins the exact signed event, publish target, configured write-relay set,
required quorum, and each relay's accepted `OK`. Backup execution-snapshot
validation requires this proof and checks the signed event retained in it;
the prunable published row and a bare `Delivered` flag are insufficient.
Older rows without policy provenance are not promoted into proofs and refuse
backup acceptance even if their event cache entry remains. The proof's signed
event also preserves a pinned older config version when the replaceable local
event cache has moved to a newer version.
For a run staged by the tested admission path or before the production pause,
the publisher's verified quorum round also updates the durable admission
record. The final signed accepted intent status is
enqueued in a separate atomic transaction after that outcome; a startup pass
repairs a crash between the two transactions without signing a second status.
Both the run state and accepted status remain pinned through ordinary pruning
until the status itself has an exact operator-relay publisher-quorum proof. Failed rows
can retry the same signed event ID. Exhaustion alone never signs a rejection
because one relay may already hold the event; MCP reports acceptance only
after both the run and final-status proofs exist.

A pre-admission backup request retained in the local pending ledger may expire
or meet a definitive run-coordinate conflict. Its `rejected` kind-30315
terminal status is staged atomically with an operator-relay outbox row. The
ledger keeps that exact status ID and, once the publisher records quorum OK,
the durable proof. Partial OK, `CLOSED`, and `blocked:`/`invalid:`/
`auth-required:` refusal cannot mark it delivered. Failed rows stay pinned
for same-ID retry, and settled rows may be pruned only after the proof is
recorded. The ledger remains the replay authority; PostgreSQL is not involved.

## Write relays and quorum

A publisher's **write relays** are the relays each delivery round targets and
counts `nostr.publish_quorum` against; they are read every round, so a relay
policy change applies to pending entries on their next round. The service
publisher's write relays are the service write relays of the effective relay
policy (`NostrConfig.ServiceWriteRelays`: the canonical service relays once
received, the static ones until then; the sidecar counted once, by its
backend URL). Loom relays share the service pool but are never write relays,
so they can neither receive service publications nor satisfy the quorum. A
quorum above 1 needs at least that many service write relays: configuration
with fewer fails validation and canonical relay settings with fewer are
refused ([relay configuration](../runbooks/relay-configuration.md)).

Every relay pool also refuses to write a service-signed control-plane event
(and, on control-plane pools, a gift wrap) to a relay outside
`nostr.controlled_relays`; that relay's result is a local
`blocked:` rejection, permanent for the entry like a relay-sent one. With the
effective policy validated against the same set, the guard does not fire for
a publisher's own write relays.

## Abandonment: two cases, decided by who was told

1. **Abandoned in the caller's round.** The caller receives
   `ErrPublishAbandoned`, treats the operation as failed and commits nothing
   derived from it (no SQL index row, no "queued" producer state). The outbox
   entry is `failed` and the event is **removed from the local event store**: it
   is not the daemon's output. Nothing is lost because the caller knows.
2. **Abandoned after the caller was told "queued".** The call returned `nil` or
   `ErrPublishIncomplete`, derived state may already exist, and the runner gives
   up later. This must never be a silent drop:
   - The outbox entry stays `failed` with the abandonment reason.
   - The event **stays in the local event store** as committed state, and the
     store records an **undelivered marker** on its coordinate
     (`localstore.Undelivered`: `<kind>:<pubkey>:<d>` for replaceable and
     addressable events, the event id otherwise) with the event id, reason and
     time. Canonical reads keep answering with that state, so a SQL index written
     after the queued publish agrees with the canonical read.
   - Readers can expose the flag: the local event repository reports the record
     with `PublishState = failed`, which also stops the projector's dedupe cache
     from treating the abandoned content as published (its `created_at` still
     floors the coordinate, so a replacement is strictly newer everywhere).
   - The failure is **surfaced on readiness**: the `canonical_delivery` health
     check turns `warn` (daemon `degraded`, still `ready`) with the count, the
     coordinates and the oldest failure. The outbox's failed count stays on the
     metrics/alert path.
   - Redelivery happens on **explicit operator action** — the MCP tool
     `bahia_outbox_retry` (platform admin) moves the daemon's entry back to
     `pending` for the runner's next pass — **or on the next publish of the same
     coordinate**: a
     canonical-first producer re-asserts state on its next mutation and the
     projector re-signs on its next trigger or repair; the replacement is
     strictly newer than the flagged event.
   - The marker is **cleared** when a quorum accepts the flagged event (retry)
     or when a newer version is saved on the coordinate — produced by the daemon
     (its own delivery then decides) or served by a relay (the coordinate moved
     on without this daemon). A late abandonment report for an older version
     never re-flags a coordinate that has moved on.
   - The local event store remains a rebuildable cache: when the runner starts,
     the publisher re-marks the coordinates of every retained failed entry, so a
     deleted store file loses no flag for as long as the outbox retains the entry.

What is deliberately not done: the marker is not a second retry loop (no
ticker; the runner and the next publish of the coordinate are the only
redelivery paths), and a flagged coordinate does not make the daemon unready —
the state is committed and served locally, and operators need the daemon up to
act on it.

## Outbound admission

Every delivery round crosses the process-wide outbound admission controller
(`internal/nostrout`; [runbook](../runbooks/nostr-outbound-admission.md))
before relay I/O. Admission refusal — a lane, aggregate, or per-relay wire
budget, the shared rate-limit circuit breaker, the operator kill switch, or
controller capacity — **skips** the round:

- A skipped round does not count against the bounded attempt budget, does not
  write per-relay state, and cannot abandon the entry or set an undelivered
  marker. Admission is back-pressure, not abandonment: a kill switch left on
  for a day leaves entries pending for a day, and delivery resumes where it
  stopped when the gate lifts.
- The round is rescheduled about one second out, jittered, so a restart's
  hydration of a large pending outbox cannot resynchronize into a burst when
  capacity returns.
- A process-wide gate (kill switch, open circuit, exhausted capacity) stops
  the whole redelivery pass at the first refusal; a lane budget refusal only
  skips the affected entries, because another lane may still have capacity.
- `PublishBeforeCommit` keeps its reversed order: a refused round queues
  nothing, the caller gets an error, and the producer commits nothing.

Abandonment therefore still means exactly what it always meant — permanent
relay rejections or a spent attempt budget — and both remain relay-reported
outcomes, never controller-side ones.

## Operator surfaces

| Surface | Purpose |
|---|---|
| `bahia outbox list [--failed\|--pending] [--daemon]` | List entries of the CLI outbox (`$XDG_DATA_HOME/bahia/outbox.bolt`, fallback `~/.local/share/bahia/outbox.bolt`) or, with `--daemon`, the daemon's outbox read-only |
| `bahia outbox counts` | Pending/failed totals |
| `bahia outbox retry <event-id>` / `--all` | Re-enqueue failed entries of the CLI outbox (the daemon's outbox is read-only from the CLI) |
| `bahia outbox prune` | Remove settled entries |
| MCP `bahia_outbox_status` | Counts by default; `include_details` for authorized callers |
| MCP `bahia_outbox_retry` | Platform-admin re-enqueue of a failed daemon entry |
| `GET /ready` → `canonical_delivery` | Warn with undelivered coordinates |

The CLI's own intents (see [CLI and MCP](cli-and-mcp.md)) use the same outbox
code with a separate file, so a CLI publish is durable before the first relay
attempt and resumes per-relay delivery on retry.
