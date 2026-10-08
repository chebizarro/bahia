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

## NIP-77 authenticated sessions (bahia-irsry.47)

`nip77.NegentropySync` dialed with fixed options, so its connection had no
NIP-42 signer. A relay that answers `NEG-OPEN` with an AUTH challenge and
`NEG-ERR auth-required:` (khatru does this when `OnRequest` refuses a
negentropy session) could not be reconciled at all: the daemon fell back to
paged REQs on its pool connection, and lost negentropy for exactly the
protected sets.

- `nip77/nip77.go`: new `NegentropySyncWithOptions(…, options nostr.RelayOptions)`;
  `NegentropySync` keeps its signature and passes empty options. The session
  connection uses the given options (Bahia passes the pool's
  `buildRelayOptions`: the same `AuthHandler`, `AuthResultHandler` and NOTICE
  logging as every pool connection). `options.CustomHandler`, if set, still
  receives the frames the session does not handle itself.
- On the first `NEG-ERR` whose reason starts with `auth-required:`, and only
  when `options.AuthHandler` is set, the session calls `Relay.Auth` (which
  joins the attempt the challenge already started) from a separate goroutine,
  because the AUTH OK arrives on the read loop the handler runs on, and then
  re-sends its original `NEG-OPEN`. This happens once per session: a second
  refusal, a failed AUTH or no AuthHandler ends the session with the relay's
  `NEG-ERR`, as before.
- The local vector and the `NEG-OPEN` frame are built before dialing, and the
  relay is created with `NewRelay` before `Connect` (instead of
  `RelayConnect`). The frame handler therefore never sees a nil relay or an
  unbuilt `NEG-OPEN`, without extra synchronisation. The connection still
  closes when the session returns.

Tests: `nip77/nip77_bahia_test.go`
(`TestNegentropySyncWithOptionsAuthenticates`: refusal, AUTH and one re-open
that downloads the protected event; a refusal after AUTH ends after exactly
one re-open; no re-open without an AuthHandler) and
`internal/adapters/nostr/relay_pool_stack_test.go`
(`TestRelayPoolNegentropyAuthenticates`, through the pool's
`negentropySyncRelay` with its own signer),
both against in-process khatru relays that require AUTH for negentropy.

## OK callback reset and dial cancellation (bahia-irsry.47)

- `relay.go` (`Relay.publish`): when the connection closes while a publish
  waits for its OK, the publish resets `okCallbacks`. That reset assigned the
  map without `okCallbacksMutex`, while other publishes register callbacks and
  the read loop dispatches OKs under it: a data race whenever a connection
  closes with more than one publish (or an AUTH) in flight. The reset now
  takes the mutex. Test: `relay_callback_reset_race_test.go`
  (`TestOKCallbacksResetOnCloseIsRaceFree`, run with -race; it reports the
  race on the unpatched line).
- `relay.go` (`Relay.newConnection`): the dial context's cancel function from
  `context.WithTimeoutCause` was discarded (`dialCtx, _ = …`), which
  `go vet` reports as a lost cancel and which kept the 7-second timer alive
  after the dial. It is now kept and deferred. `go vet` on this package is
  clean.

## khatru: concurrent NIP-11 requests (bahia-irsry.47)

`HandleNIP11` copied the document with `info := *rl.Info` and then appended
the NIPs implied by the relay's configuration (9 with `DeleteEvent`, 45 with
`Count`, 77 with `Negentropy`) to `info.SupportedNIPs`. The struct copy shares
the slice's backing array, and `UseEventstore` leaves spare capacity in it (it
appends NIP-40 to the five-element default, giving length 6 and capacity 10).
Every NIP-11 GET to a relay with an eventstore therefore wrote the same slots
of `rl.Info`'s array: a data race between overlapping requests, which could
also leak one response's entries into another. K4 found it in fipsbridge and
discovery tests, which had worked around it by pre-seeding the NIP list.

- `khatru/nip11.go`: each request clones `SupportedNIPs` before appending.

Test: `khatru/nip11_race_bahia_test.go`
(`TestHandleNIP11ConcurrentRequestsAreRaceFree`: 16 concurrent GETs against
an in-process relay with an eventstore and negentropy; every document lists
9, 40 and 77 once, and `rl.Info` is unchanged). On the unpatched handler,
`-race` reports the race at the `AddSupportedNIP` calls.

Not patched: `go vet` on `khatru` still reports a lost cancel in
`handlers.go` (`cancelReqCtx`) and an `unsafe.Pointer` conversion in
`relay.go`. Both are upstream code that this wave does not touch.

## nip11: omit a zero created_at_lower_limit (bahia-irsry.44)

The generated `RelayInformationDocument` encoder wrote `created_at_lower_limit`
unconditionally, so a relay that sets no lower bound still advertised
`"created_at_lower_limit": 0`. Every other numeric limitation field is omitted
when zero. The relay sidecar only caps the age of regular and ephemeral kinds
(replaceable and addressable events and deletion requests are accepted at any
age), and NIP-11 has no per-kind form, so it must not advertise a lower limit
at all.

- `nip11/easyjson.go`: `created_at_lower_limit` is written only when non-zero.
  `created_at_upper_limit` is still always written, now with the leading-comma
  handling it needs when no earlier field was written. Decoding is unchanged.

Test: `nip11/limitation_bahia_test.go`
(`TestLimitationOmitsZeroCreatedAtLowerLimit`; on the unpatched encoder the
document contains `"created_at_lower_limit":0`). End to end:
`internal/relaysidecar` `TestSidecarNIP11AdvertisesAccurateCapabilities`.

The eventstore itself is not patched for bahia-irsry.44. Its tag index still
skips values longer than 100 bytes (`eventstore/boltdb/helpers.go`); raising
the limit would change the on-disk index. The relay sidecar works around it
with its own deletion index (`internal/relaysidecar/deletion_index.go`).

## Removal criteria

Drop the `replace` and this directory once upstream carries equivalent fixes
(for the khatru part, any server-side listener close keyed so that a reused
subscription id is unaffected):
bump `fiatjaf.com/nostr` to that version and keep both regression tests (the
Bahia-side one must keep passing under -race). The previous local copy was
removed the same way in c70042c0.

## Per-subscription event delivery order (bahia-irsry.58)

`Subscription.dispatchEvent` spawned a new goroutine for every event. The
goroutines raced to send on `sub.Events`, so events arrived in random order.
This is observable under `-cpu=2` or higher with bursts of 256+ events, and
always observable on single-CPU machines under contention. Any consumer that
assumes the relay's wire order (e.g. a cursor that tracks the most recent
`created_at`) could skip events or move a resume cursor backward.

- `subscription.go`: new `subscriptionInbox` struct — a mutex-guarded FIFO
  slice with a 1-buffered signal channel. `dispatchEvent` calls `push()`,
  which appends the item under `inbox.mu` and sends a non-blocking signal.
  **push never blocks the caller** (the relay's main-loop goroutine), avoiding
  head-of-line blocking: the read loop also processes OK, EOSE, CLOSED, AUTH
  and NOTICE for every subscription and publish on the connection. A blocking
  inbox would stall them all and deadlock a consumer that publishes to the
  same relay from an event handler (e.g. Bahia's intent processor / encrypted
  transport receive an event, publish canonical state or a reply, and wait for
  OK through the outbox — the OK arrives on the same read loop).
- When the queue exceeds `subscriptionInboxCap` (4096), the inbox is marked
  closed and the subscription is closed with `handleClosed("error: subscription
  inbox overflow")`. Consumers resubscribe from their resume cursor. No events
  are silently dropped: the consumer knows the subscription was closed.
- The dispatcher goroutine waits on the signal channel, drains the queue, and
  delivers items to `Events` in FIFO order under `mu.RLock`. On `Context.Done`,
  it releases `storedwg` for any remaining items and exits.
- Subscriptions created without `PrepareSubscription` (test helpers that build
  a `Subscription` literal) have a nil inbox and fall back to the legacy
  per-goroutine path.
- `relay.go` (`PrepareSubscription`): creates the inbox and starts the
  dispatcher goroutine. The teardown goroutine sets `channelsClosed`, calls
  `inbox.close()`, waits for the dispatcher to exit, and then closes `Events`
  and `countResult`. This preserves the `mu` discipline from bahia-irsry.17.

Tests: `subscription_order_test.go`:
- `TestDispatchEventPreservesOrderLive`: 1024 live events arrive in wire order.
- `TestDispatchEventPreservesOrderStored`: 256 stored events arrive in wire
  order with EOSE after the last one.
- `TestDispatchEventOrderAcrossStoredAndLive`: stored burst, EOSE, live burst,
  each sub-sequence in order.
- `TestDispatchEventNonBlockingUnblocksOnCancel`: 1000 events dispatched without
  a reader complete instantly (no blocking), then cancel shuts down cleanly.
- `TestInboxDoesNotBlockOtherSubscriptions`: a blocked consumer on sub A does
  not delay delivery to sub B on the same relay.
- `TestPublishFromEventHandlerDoesNotDeadlock`: simulates a consumer that
  publishes to the same relay and waits for OK inside its event handler; the
  read loop remains free to deliver the OK after dispatching 500 events.
- `TestInboxOverflowClosesSubscription`: overflow closes the subscription
  with a reason (no silent drops); context is canceled and Events is closed.

Run with: `CGO_ENABLED=0 go test fiatjaf.com/nostr -cpu=1,2,8 -count=20 -run 'TestDispatchEvent|TestInbox|TestPublishFrom'`.

## Second CLOSED goroutine leak (bahia-irsry.26)

`Subscription.handleClosed` started a goroutine unconditionally. A relay that
sends two CLOSED frames (e.g. an overflow close followed by a disconnect close)
leaked the second goroutine: `ClosedReason` is buffered with capacity 1, and
the second send blocked forever.

- `subscription.go`: new `closedHandled atomic.Bool` with `CompareAndSwap`
  guard. Only the first call starts the goroutine; subsequent calls return
  immediately.

Test: `subscription_closed_test.go` `TestSecondClosedDoesNotLeak`.

## countInternal data race (bahia-irsry.26)

`Relay.countInternal` created the subscription via `PrepareSubscription`, which
stores it in the subscription map at line 715, and then set
`sub.countResult = make(chan CountEnvelope, 1)` AFTER the store. A COUNT reply
arriving between the store and the assignment found `countResult == nil` and
was dropped, causing the count to time out.

- `subscription.go` (`SubscriptionOptions`): new unexported `isCount bool` field.
- `relay.go` (`PrepareSubscription`): when `opts.isCount`, creates `countResult`
  before the store. `countInternal` sets `opts.isCount = true` instead of
  assigning `sub.countResult` after `PrepareSubscription`.

No dedicated test: the existing `TestCountAfterTeardownIsDropped` covers the
post-teardown path, and `TestCount` (upstream, needs a public relay) covers the
happy path. The race window is eliminated by construction.

## khatru: listener-before-query (NOT APPLIED — bahia-irsry.26)

Khatru registers a REQ's live listener AFTER its stored query, leaving a gap
where a client CLOSE finds nothing to remove and the listener lingers until
disconnect. The natural upstream fix is to register the listener before the
stored query, which this wave prototyped and tested.

**Not applied to Bahia's vendored copy**: the relay sidecar's `pendingListener`
mechanism deduplicates events delivered by both the stored query and the live
listener, and it assumes the original ordering (listener after query). Changing
khatru to listener-before-query causes duplicates (the stored query returns
an event, and the now-active live listener delivers it again during the query).
Adapting the sidecar would require a per-subscription stored-ID set with
cross-goroutine synchronization, which is complex and error-prone.

The sidecar already handles both issues:
- **Gap closing**: `beginRequest` / `trackStored` / `listenerAdded` buffer and
  deduplicate events that match during the gap.
- **CLOSE during query**: `listenerAdded` checks `pending.ctx.Err()` and
  schedules removal if the REQ was already canceled.

An upstream patch for listener-before-query is prepared in
`third_party/nostr/upstream-patches/` for submission when the sidecar's
dedup can be simplified, or when upstream adopts it.

## NIP-42 AUTH state (bahia-irsry.10.2) — verified

(Already documented above.) Verified in this wave: `authMu` guards
`challenge`, `authed` and `authing`. The `startAuthLocked` / `runAuth` /
`authAttempt` pattern is correct. `Relay.Auth` joins an in-flight attempt
instead of starting a concurrent one.

**SoulFactory implications**: SoulFactory delegates relay connections to
Bahia's `RelayPool`, which wires `AuthHandler` and `AuthResultHandler` once per
connection. SoulFactory itself does not call `Relay.Auth` directly or hold any
AUTH state. No SoulFactory changes are needed.

## RelayPool consumer order-independence audit (bahia-irsry.58)

All Bahia consumers of `Subscription.Events` are order-independent: they use
`created_at` for cursor tracking and event IDs for deduplication, not arrival
order. Per-subscription ordering (the inbox fix above) improves determinism but
is not required for correctness.

| Consumer | File | Cursor / dedup | Order assumption |
|---|---|---|---|
| `activeMergedSubscription.consume()` | `relay_pool.go:1813` | `relayResumeCursor.observe(ev)` tracks max `created_at`; `EventDeduplicator` by ID | None |
| `Subscriber.drainStored()` | `inbound_sync.go:440` | Tracks min `created_at`; seen map by ID | None |
| `Subscriber.forwardLive()` | `inbound_sync.go:530` | Forwards to `inboundItem` channel; cursor by created_at in downstream `cursorTracker` | None |
| `RelaySubscription.CollectStoredEvents()` | `soulfactory/relay_client.go:505` | Appends to slice; delegates dedup to pool | None |
| `Pool.subMany()` | `pool.go:434` | Forwards `IncomingEvent{Event, Relay}` | None |
| `Pool.subManyEose()` | `pool.go:625` | Same as subMany, with EOSE collection | None |
| `FetchManyReplaceable()` | `pool.go` | Compares by `created_at` (latest wins) | None |

## eventstore/slicestore: unlocked reads (CI race job, 2026-10-05)

`SliceStore.QueryEvents` and `CountEvents` iterated `b.internal` without the
mutex that `SaveEvent`/`DeleteEvent`/`ReplaceEvent` hold while shifting the
backing array. khatru runs REQ and EVENT handlers on separate goroutines, so
any Bahia test that drives a khatru relay over a `SliceStore` (ten files, e.g.
`internal/adapters/nostr/relay_pool_stack_test.go`) can trip `-race` under
load; `make race` on GitHub Actions did (`TestRelayPoolPagesPastNIP11MaxLimit`).

- `QueryEvents` computes the since/until window and clones it under the lock,
  then iterates the clone (the lock cannot be held across `yield`).
- `CountEvents` holds the lock.

Prepared for upstream as `upstream-patches/0005-slicestore-locked-reads.patch`.

## COUNT rejection answers CLOSED (khatru/handlers.go, bahia-amv53)

Upstream `handleCountRequest`/`handleCountRequestWithHLL` answer a filter
refused by `OnCount` with a `NOTICE` and then a `COUNT` of 0, which a client
cannot tell from a real empty count. The `CountEnvelope` branch in
`handleMessage` now runs `OnCount` first and, on refusal, writes
`CLOSED <id> <reason>` (NIP-45 permits CLOSED for a rejected COUNT), issuing
the NIP-42 challenge first when the reason is `auth-required:`, exactly as the
REQ branch does. The read-auth sidecar relies on this so an unauthenticated
COUNT on a protected topic is reported as `auth-required:` rather than 0.
Covered by `internal/relaysidecar/read_auth_default_test.go`.

## NIP-77 session producer/consumer release (nip77/nip77.go, nip77/negentropy/negentropy.go, bahia-outbound-admission)

`NegentropySyncWithOptions` closes the `Haves`/`HaveNots` id channels only
when a `Reconcile` completes. A session that ends in NEG-ERR, a timeout, or a
caller cancellation returns without closing them, so a consumer ranging the
channel blindly blocks forever (the Bahia upload handler leaked a goroutine
per aborted session), and a consumer that exits first leaves `Reconcile`
blocked forever on a full channel emit, wedging the relay read loop. Two
complementary changes:

- `negentropy.Negentropy` gains a `done` channel: `emit` selects on it, and
  the idempotent `Release()` closes it, aborting an in-flight `Reconcile`
  with `errNegentropyReleased`. `NegentropySyncWithOptions` defers
  `neg.Release()`, so producers are freed on every session exit path.
- The Bahia consumers (`moveNegentropyItems`/`uploadNegentropyItems`/
  `downloadNegentropyItems` in `internal/adapters/nostr/relay_pool_sync.go`)
  receive `Items` in a `select` against the session context, which
  `negentropySyncRelay` cancels when it returns for any reason.

Covered by `TestReleaseFreesBlockedEmit` (nip77/negentropy) and
`TestNegentropyUploadReturnsWhenSessionEnds`
(`internal/adapters/nostr/relay_frame_admission_test.go`).
