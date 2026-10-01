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

## NIP-42 AUTH state (bahia-irsry.10.2)

Client side (`relay.go`). `Relay` kept its NIP-42 state (`challenge`, the
`performAuth` `sync.Once`, and `authed`) in plain fields. The reader goroutine
rewrote `challenge` and reset `performAuth` on every AUTH frame, while
`Relay.Auth` read them from the caller's goroutine. With
`RelayOptions.AuthHandler` set, every AUTH frame also started another `Auth`
goroutine. khatru re-sends its challenge before each `auth-required:`
rejection, so those goroutines overlapped the next reset. The result was data
races and, when `performAuth` was reset while another goroutine was inside
`Do`, `fatal error: sync: unlock of unlocked mutex`. `AuthHandler` also
discarded the AUTH OK, so no caller could learn whether it worked.

- `authMu` guards `challenge`, `authed` and the new `authing` field, which
  holds the attempt in flight. `performAuth` is gone.
- An AUTH frame records the challenge and starts an `AuthHandler` attempt only
  when the connection is not yet authenticated and no attempt is in flight.
- `Relay.Auth` returns nil on an authenticated connection. If an attempt is in
  flight, it waits for that attempt and returns its outcome; otherwise it
  starts one. A failed attempt leaves the connection free to try again.
- New `RelayOptions.AuthResultHandler(relay, err)` receives the outcome of
  every attempt: nil for OK true, otherwise the OK false reason, a timeout or a
  signing error.

Server side (`khatru`). `GetAuthed`, `GetAllAuthed`, `IsAuthed`, `ListClients`
and `GetClientSnapshot` read `WebSocket.AuthedPublicKeys` without the
`authLock` that the AUTH handler writes it under. They now read a copy through
`WebSocket.authedPublicKeys()`, which takes the lock. The relay sidecar calls
these accessors in its REQ and EVENT policy.

Tests: `relay_auth_race_test.go`.
- `TestRelayAuthHandlerWithOverlappingChallengesIsRaceFree`: concurrent
  rejected REQs, `AuthHandler` and `Relay.Auth` against an in-process khatru
  relay that requires NIP-42. It uses only the pristine API. On the pristine
  copy it reports data races and aborts with the fatal error above. Patched,
  it passes `-race -count=30`.
- `TestRelayAuthResultIsObservable`: `AuthResultHandler` delivery, `Auth`
  joining an in-flight attempt, and recovery after a failed attempt.

Bahia side: the shared `RelayPool` wires `AuthHandler` and
`AuthResultHandler` once per connection (see `internal/adapters/nostr`
`relay_pool_stack_test.go` (`TestRelayPoolNIP42ThroughAuthHandlerIsRaceFree`)).

## Removal criteria

Drop the `replace` and this directory once upstream carries equivalent fixes
(for the khatru part, any server-side listener close keyed so that a reused
subscription id is unaffected):
bump `fiatjaf.com/nostr` to that version and keep both regression tests (the
Bahia-side one must keep passing under -race). The previous local copy was
removed the same way in c70042c0.
