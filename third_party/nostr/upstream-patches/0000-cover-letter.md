# Upstream patch series for fiatjaf.com/nostr

These patches are prepared against `fiatjaf.com/nostr v0.0.0-20260916040958-27e395a0f6e7`
(commit `27e395a0f6e7`). They have not been submitted upstream yet.

## Patches

### 0001: Per-subscription event delivery order

`Subscription.dispatchEvent` spawns a goroutine per event, so events delivered
on `sub.Events` arrive in random order under contention. Replace with a bounded
FIFO inbox (capacity 256) and a single dispatcher goroutine per subscription.
Backward compatible: subscriptions created without `PrepareSubscription` fall
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

Khatru registers a REQ's live listener AFTER its stored query. A client CLOSE
arriving during the (possibly slow) query finds nothing to remove, and the
listener lingers until disconnect. Fix: register all listeners before running
stored queries; clean up on filter rejection.

**Status**: NOT applied to Bahia's vendored copy. The relay sidecar's
`pendingListener` dedup mechanism assumes the original ordering and breaks with
listener-before-query (stored query results dispatched live during the query
cause duplicates). Prepared as a standalone patch for upstream submission, where
the `pendingListener` interaction does not apply.

## Submission

Wait for upstream activity in `subscription.go` / `relay.go` / `khatru/handlers.go`
to avoid unnecessary conflicts. Patches 0001–0003 are independent; 0004 depends
on the server not having its own gap-buffer mechanism.
