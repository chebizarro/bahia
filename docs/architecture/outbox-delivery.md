# Outbox delivery and abandonment

Every canonical event the daemon signs goes through the **local outbox**
(`internal/adapters/nostr/localstore`, bbolt) before any relay attempt. The
outbox is what makes "the daemon published X" durable across crashes, and this
page is the contract for what happens when a relay quorum can no longer be
reached. It binds every domain that publishes through the outbox, not only
payments and security.

## Durable publish

- `Publisher.PublishBeforeCommit` writes the signed event to the outbox first;
  per-relay `OK` tracking is persisted, so a publish interrupted mid-delivery
  resumes exactly where it stopped after a restart.
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
