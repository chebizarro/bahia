# Upstream patch series for fiatjaf.com/nostr

These patches are prepared against `fiatjaf.com/nostr v0.0.0-20260916040958-27e395a0f6e7`
(commit `27e395a0f6e7`). They have not been submitted upstream yet.

## Patches

### 0001: Per-subscription non-blocking event ordering

`Subscription.dispatchEvent` spawns a goroutine per event, so events delivered
on `sub.Events` arrive in random order under contention. Replace with a
non-blocking per-subscription inbox (mutex-guarded FIFO slice with a 1-buffered
signal channel) and a single dispatcher goroutine. `push()` never blocks the
relay's main-loop goroutine, which also delivers OK, EOSE, CLOSED, AUTH and
NOTICE. A bounded channel would cause head-of-line blocking (one slow
subscriber stalls all) and deadlock a consumer that publishes to the same relay
and waits for OK inside an event handler. The inbox has a cap (4096); overflow
closes the subscription with a reason so the consumer can resubscribe from its
cursor. Backward compatible: subscriptions without `PrepareSubscription` fall
back to the legacy goroutine path.

**Status**: Applied to Bahia's vendored copy. Ready for upstream review.

### 0002: Second CLOSED goroutine leak

`handleClosed` starts a goroutine unconditionally. A relay that sends two
CLOSED frames leaks the second goroutine (it blocks forever on the buffered-1
`ClosedReason` send). Guard with `atomic.Bool`.

**Status**: Applied to Bahia's vendored copy. Ready for upstream review.

### 0003: countInternal data race

`countInternal` sets `sub.countResult` after `PrepareSubscription` stores the
subscription. A COUNT reply arriving in between finds `countResult == nil` and
is dropped. Move initialization into `PrepareSubscription` via an unexported
`SubscriptionOptions.isCount` field.

**Status**: Applied to Bahia's vendored copy. Ready for upstream review.

### 0004: khatru listener-before-query

Khatru registers a REQ's live listener AFTER its stored query, and handles
every message on its own goroutine. An event saved while the query runs is
neither replayed nor broadcast: a live subscriber misses it forever even
though its publisher got OK true. A client CLOSE arriving during the (possibly
slow) query also finds nothing to remove, and the listener lingers until
disconnect. Fix: run the acceptance policy for every filter first (a rejected
REQ must never expose a listener, even transiently), then register all
listeners, then run the stored queries. Events both replayed and broadcast are
duplicates the client deduplicates, as NIP-01 already allows.

**Status**: Applied to Bahia's vendored copy, extended to the policy-first
three-phase form (see BAHIA_PATCHES.md, "khatru: listener-before-query"). The
relay sidecar's `pendingListener` dedup was adapted to the new ordering: its
drain moved from `OnListenerAdded` to the end of the query iterator
(`finishPending`), keeping delivery exactly-once. Regression test:
`khatru/listener_before_query_bahia_test.go`.

## Submission

Wait for upstream activity in `subscription.go` / `relay.go` / `khatru/handlers.go`
to avoid unnecessary conflicts. Patches 0001–0003 are independent; 0004 depends
on the server not having its own gap-buffer mechanism (Bahia's relay sidecar
had one; it was adapted to the new ordering when 0004 was applied).

### 0005: slicestore reads without the lock

`SliceStore.QueryEvents` and `CountEvents` iterate the backing slice while
`SaveEvent`/`DeleteEvent` may be shifting it from another goroutine (khatru
handles REQ and EVENT on separate goroutines). Snapshot the queried window under
the lock and iterate the copy; hold the lock in `CountEvents`.

**Status**: Applied to Bahia's vendored copy. Ready for upstream review.
