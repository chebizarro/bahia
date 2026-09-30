# Bahia local patches to fiatjaf.com/nostr

Base: `fiatjaf.com/nostr v0.0.0-20260916040958-27e395a0f6e7`, copied unchanged
from the module cache (commit "vendor pristine fiatjaf.com/nostr@27e395a0f6e7"),
wired in via `replace fiatjaf.com/nostr => ./third_party/nostr` in the root
`go.mod`. `libsecp256k1/` is excluded by the module's own `.gitignore`; it is
only compiled under the `libsecp256k1` build tag, which Bahia does not use.

## Build and test wiring

- `Dockerfile` copies `third_party/nostr/go.mod` and `go.sum` before
  `go mod download`, because the replacement module's go.mod must exist at
  that step. Every image build in CI (`.github/workflows/*`,
  `.gitea/workflows/release.yml`, `make docker`) uses that Dockerfile with the
  repository root as context. There is no root `.dockerignore`.
- This directory is its own Go module, so the repo's `./...` patterns
  (`go build/vet/test ./...`, `make test`/`race`/`lint`, Go CI) never include
  it. Its upstream tests, some of which fail as noted below, only run if
  invoked explicitly (`go test fiatjaf.com/nostr/...`). `make fmt` excludes
  `third_party/`.
- Dependabot ignores `fiatjaf.com/nostr`: a bump would not take effect behind
  the `replace`.
- To re-vendor: copy the new module version from the module cache, reapply
  the patches below, keep the `Dockerfile` COPY line, and rerun the regression
  tests.

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

## nip77: NEG-ERR label (bahia-irsry.9.1)

`nip77.ErrorEnvelope` marshalled its frame as `["NEG-ERROR", …]`, while NIP-77
and the package's own `ParseNegMessage` use `NEG-ERR`. Every error khatru sent
to a negentropy client (a refused filter, an oversize set, a reconcile
failure) was therefore dropped by spec clients, including
`nip77.NegentropySync`, which then waited until its context expired. The
reason was also written unescaped.

- `nip77/envelopes.go`: `ErrorEnvelope` marshals as `NEG-ERR` through
  `encoding/json`; `ParseNegMessage` still accepts `NEG-ERROR` from older
  relays.

Tests: `nip77/envelopes_bahia_test.go`, and end to end
`internal/relaysidecar` `TestSidecarNegentropyRefusesSetsLargerThanTheLimit`.

## nip77: NegentropySync connection lifecycle (bahia-irsry.10.1)

`NegentropySync` dials its own connection with `nostr.RelayConnect`, which
binds it to `context.Background()`, and never closed it: every sync left a
websocket and its goroutines open for the life of the process. The daemon
runs a sync per relay and filter on startup and on every reconnect, so this
leaked without bound. Its relay-frame handler also sent outcomes on an
unbuffered channel, so a second frame (a NEG-ERR or NEG-CLOSE after the first
outcome was taken) blocked the connection's read loop forever.

- `nip77/nip77.go`: close the relay when `NegentropySync` returns (also after a
  failed dial), and report outcomes through a one-slot channel with a
  non-blocking send, so only the first outcome is kept.

Callers must still pass a `handle` that returns when its context ends:
`Direction.Items` is never closed when a relay refuses or abandons a session.
`internal/adapters/nostr/relay_pool_sync.go` does this.

Test: `nip77/nip77_bahia_test.go` (in-process khatru relay, completed and
NEG-ERR sessions).

## Removal criteria

Drop the `replace` and this directory once upstream carries equivalent fixes
(for the khatru part, any server-side listener close keyed so that a reused
subscription id is unaffected):
bump `fiatjaf.com/nostr` to that version and keep both regression tests (the
Bahia-side one must keep passing under -race). The previous local copy was
removed the same way in c70042c0.
