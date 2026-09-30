# Bahia local patches to fiatjaf.com/nostr

Base: `fiatjaf.com/nostr v0.0.0-20260916040958-27e395a0f6e7`, copied unchanged
from the module cache (commit "vendor pristine fiatjaf.com/nostr@27e395a0f6e7"),
wired in via `replace fiatjaf.com/nostr => ./third_party/nostr` in the root
`go.mod`. `libsecp256k1/` is excluded by the module's own `.gitignore`; it is
only compiled under the `libsecp256k1` build tag, which Bahia does not use.

## Why a local copy (bahia-irsry.17)

`Subscription.dispatchEvent` sent on `sub.Events` from per-event goroutines
without synchronising with the teardown goroutine in `PrepareSubscription`,
which closes `sub.Events` once `sub.Context` is done. A relay-sent `CLOSED`
(`handleClosed` → `cancel`) arriving while post-EOSE events were still being
dispatched was a data race and could panic the whole process with
`send on closed channel`: once the context is done, both `Events <- evt`
(closed) and `<-Context.Done()` are ready in the sender's `select`, and Go
picks at random. The same applies to `Unsub()`/context cancellation, and the
`COUNT` reply send in `handleMessage` had the same shape against
`close(sub.countResult)`.

Nothing in upstream history between the pin and 58e4c715 (2026-09-28, latest)
touches `subscription.go`, `relay.go` or `khatru/`, so no newer version fixes
this. A Bahia-side guard is impossible: the panicking goroutine belongs to the
library and cannot be recovered or synchronised with by callers, and any relay
(not just the Bahia sidecar) may send `CLOSED`.

## The patch (subscription.go, relay.go)

- `Subscription.mu` becomes a `sync.RWMutex` and gains `channelsClosed`.
- `dispatchEvent` goroutines hold the read lock across the send and return
  early if `channelsClosed` (or the subscription is no longer live).
- New `dispatchCount` does the same for `countResult`, and also selects on
  `Context.Done()` so a late `COUNT` cannot block the read loop.
- The teardown goroutine sets `channelsClosed` and closes the channels under
  the write lock. Teardown only runs after `Context` is done and every sender
  selects on `Context.Done()`, so the write lock is granted promptly.

Tests: `subscription_teardown_race_test.go` (library level, -race) and
`internal/adapters/nostr/library_closed_race_test.go` (end-to-end over a real
websocket relay that sends EOSE, an EVENT burst, then CLOSED).

Note: several upstream tests in this package (`TestSubscribeBasic`,
`TestEOSEMadness`, `TestNestedSubscriptions`, `TestCount`, `TestPublish`,
`TestPublishBlocked`) dial public relays or are flaky, and fail identically on
the pristine copy. So does `khatru`'s `TestWithServiceURL`.

## khatru: server-side listener close (bahia-irsry.18)

Khatru had no way to close a subscription from the server side. After the
relay sidecar sent an overflow `CLOSED`, the internal listener lingered until
the client disconnected, because go-nostr clients re-subscribe under a new id
and, per NIP-01, don't send `CLOSE` for a subscription the relay closed.

- `khatru/listener.go`: `Relay.RemoveListeners(ws, ssids...)` removes listeners
  by their internal id (the `ssid` given to `OnListenerAdded`), cancels their
  REQ context with `ErrSubscriptionClosedByRelay`, and fires
  `OnListenerRemoved`, the same as a client `CLOSE`. Removing by ssid rather
  than subscription id leaves a re-REQ under the same id untouched.
- `khatru/utils.go`: `GetSubscriptionID` returns `""` outside a REQ instead of
  panicking. NIP-77 negentropy also runs `OnRequest`/`QueryStored`, without a
  subscription id.

Test: `khatru/listener_remove_test.go`.

Not patched here (handled in `internal/relaysidecar/fanout.go`): khatru runs
a filter's stored query before it registers the live listener, which leaves a
gap. The sidecar closes it with `OnRequest` + `QueryStored` hooks. A natural
upstream fix is to register the listener, or buffer, before `QueryStored`.

## Removal criteria

Drop the `replace` and this directory once upstream carries equivalent fixes
(for the khatru part, any server-side listener close keyed so that a reused
subscription id is unaffected):
bump `fiatjaf.com/nostr` to that version and keep both regression tests (the
Bahia-side one must keep passing under -race). The previous local copy was
removed the same way in c70042c0.
