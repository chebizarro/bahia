package nostr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/boltdb"
	"fiatjaf.com/nostr/khatru"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// End-to-end inbound sync tests against in-process khatru
// relays. Every wait is on a protocol signal surfaced by the subscriber's
// consumer (EOSE commits, catch-up, handler dispatch) under a deadline; none
// sleeps.

const (
	syncTestRegularKind = KindCASAudit    // 4903, regular
	syncTestStateKind   = KindNIP38Status // 30315, addressable
	syncTestTimeout     = 20 * time.Second
)

type recordedReq struct {
	filter     gonostr.Filter
	negentropy bool
}

type syncTestRelay struct {
	relay *khatru.Relay
	store *boltdb.BoltBackend
	url   string
	down  atomic.Bool

	mu   sync.Mutex
	reqs []recordedReq
}

type syncTestRelayOptions struct {
	negentropy       bool
	refuseNegentropy string
}

func startSyncTestRelay(t *testing.T, opts syncTestRelayOptions) *syncTestRelay {
	t.Helper()
	r := &syncTestRelay{relay: khatru.NewRelay(), store: &boltdb.BoltBackend{Path: filepath.Join(t.TempDir(), "relay.bolt")}}
	require.NoError(t, r.store.Init())
	t.Cleanup(r.store.Close)
	r.relay.Negentropy = opts.negentropy
	r.relay.UseEventstore(r.store, 500)
	r.relay.RejectConnection = func(*http.Request) bool { return r.down.Load() }
	r.relay.OnRequest = func(ctx context.Context, filter gonostr.Filter) (bool, string) {
		negentropy := khatru.IsNegentropySession(ctx)
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedReq{filter: filter, negentropy: negentropy})
		r.mu.Unlock()
		if negentropy && opts.refuseNegentropy != "" {
			return true, opts.refuseNegentropy
		}
		return false, ""
	}
	server := httptest.NewServer(r.relay)
	t.Cleanup(server.Close)
	r.url = gonostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	return r
}

// add stores events on the relay without broadcasting them, as if they
// arrived while the daemon was not subscribed.
func (r *syncTestRelay) add(t *testing.T, events ...gonostr.Event) {
	t.Helper()
	for _, ev := range events {
		_, err := r.relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
	}
}

// publishLive stores an event and delivers it to live subscribers.
func (r *syncTestRelay) publishLive(t *testing.T, ev gonostr.Event) {
	t.Helper()
	r.add(t, ev)
	r.relay.BroadcastEvent(ev)
}

func (r *syncTestRelay) has(id gonostr.ID) bool {
	for range r.store.QueryEvents(gonostr.Filter{IDs: []gonostr.ID{id}}, 1) {
		return true
	}
	return false
}

func (r *syncTestRelay) recorded() []recordedReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reqs)
}

func (r *syncTestRelay) resetRecorded() {
	r.mu.Lock()
	r.reqs = nil
	r.mu.Unlock()
}

func fastTestBackoff() *Backoff {
	return &Backoff{Initial: 20 * time.Millisecond, Max: 100 * time.Millisecond, Multiplier: 2}
}

func newSyncTestPool(relays ...*syncTestRelay) *RelayPool {
	urls := make([]string, 0, len(relays))
	for _, relay := range relays {
		urls = append(urls, relay.url)
	}
	pool := NewRelayPool(urls, zap.NewNop())
	pool.newReconnectBackoff = fastTestBackoff
	return pool
}

func syncTestEvent(t *testing.T, sk gonostr.SecretKey, kind gonostr.Kind, createdAt gonostr.Timestamp, tags gonostr.Tags, content string) gonostr.Event {
	t.Helper()
	ev := gonostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags, Content: content}
	require.NoError(t, ev.Sign(sk))
	return ev
}

// syncRun is one running Subscriber and the signals its consumer emits.
type syncRun struct {
	sub      *Subscriber
	cancel   context.CancelFunc
	done     chan error
	handled  chan gonostr.Event
	observed chan gonostr.Event

	// history is every item the consumer applied, in order; changed wakes
	// waiters after each append.
	mu      sync.Mutex
	history []inboundItem
	changed chan struct{}
}

func startSyncRun(t *testing.T, pool *RelayPool, store *localstore.Store, repo repository.NostrEventRepository, cfg InboundSyncConfig, opts ...SubscriberOption) *syncRun {
	t.Helper()
	run := &syncRun{
		done:     make(chan error, 1),
		handled:  make(chan gonostr.Event, 1024),
		observed: make(chan gonostr.Event, 1024),
		changed:  make(chan struct{}, 1),
	}
	opts = append([]SubscriberOption{
		WithKinds([]int{syncTestRegularKind, syncTestStateKind}),
		WithLocalStore(store),
		WithInboundSync(cfg),
		WithHandler(func(_ context.Context, ev *gonostr.Event) { run.handled <- *ev }),
		WithObserver(func(_ context.Context, ev *gonostr.Event) { run.observed <- *ev }),
	}, opts...)
	run.sub = NewSubscriber(pool, repo, zap.NewNop(), opts...)
	run.sub.newRelayBackoff = fastTestBackoff
	run.sub.trace = func(item inboundItem) {
		run.mu.Lock()
		run.history = append(run.history, item)
		run.mu.Unlock()
		select {
		case run.changed <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	go func() { run.done <- run.sub.Run(ctx) }()
	t.Cleanup(run.stop)
	return run
}

func (r *syncRun) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	<-r.done
}

// waitCount waits until at least n applied items match.
func (r *syncRun) waitCount(t *testing.T, ctx context.Context, what string, n int, match func(inboundItem) bool) {
	t.Helper()
	for {
		r.mu.Lock()
		count := 0
		for _, item := range r.history {
			if match(item) {
				count++
			}
		}
		r.mu.Unlock()
		if count >= n {
			return
		}
		select {
		case <-r.changed:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s (%d of %d)", what, count, n)
		}
	}
}

// waitCaughtUp waits for relayURL's n-th completed catch-up (1 on start, one
// more per reconnect).
func (r *syncRun) waitCaughtUp(t *testing.T, ctx context.Context, relayURL string, n int) {
	t.Helper()
	r.waitCount(t, ctx, "catch-up of "+relayURL, n, func(item inboundItem) bool {
		return item.op == opCaughtUp && item.relay == relayURL
	})
}

// waitLive waits for the live REQs' EOSE on relayURL in its first session:
// two commits per filter, the catch-up one and the live one.
func (r *syncRun) waitLive(t *testing.T, ctx context.Context, relayURL string, filters int) {
	t.Helper()
	r.waitCount(t, ctx, "live EOSE on "+relayURL, 2*filters, func(item inboundItem) bool {
		return item.op == opCommit && item.relay == relayURL
	})
}

// waitApplied waits until the consumer has fully applied (stored, dispatched
// and counted toward its cursor) the event with this id.
func (r *syncRun) waitApplied(t *testing.T, ctx context.Context, id gonostr.ID) {
	t.Helper()
	r.waitCount(t, ctx, "event "+id.Hex()+" applied", 1, func(item inboundItem) bool {
		return item.op == opEvent && item.ev.ID == id
	})
}

// drainIDs returns the ids received so far, without waiting.
func drainIDs(ch chan gonostr.Event) []gonostr.ID {
	var ids []gonostr.ID
	for {
		select {
		case ev := <-ch:
			ids = append(ids, ev.ID)
		default:
			return ids
		}
	}
}

func waitEvent(t *testing.T, ctx context.Context, ch chan gonostr.Event, id gonostr.ID, what string) {
	t.Helper()
	for {
		select {
		case ev := <-ch:
			if ev.ID == id {
				return
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func eventIDs(events ...gonostr.Event) []gonostr.ID {
	ids := make([]gonostr.ID, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	return ids
}

func syncTestFilterHash(t *testing.T, sub *Subscriber, persistent bool) string {
	t.Helper()
	filters, err := sub.buildSubscriptionFilters()
	require.NoError(t, err)
	for _, filter := range filters {
		if filter.persistent == persistent {
			return filter.hash
		}
	}
	t.Fatalf("no filter with persistent=%v", persistent)
	return ""
}

func syncTestConfig() InboundSyncConfig {
	return InboundSyncConfig{ResumeOverlap: time.Minute, RegularLookback: 24 * time.Hour, PageLimit: 5, NegentropyTimeout: 10 * time.Second}
}

func isKindReq(req recordedReq, kind gonostr.Kind) bool {
	return len(req.filter.Kinds) == 1 && req.filter.Kinds[0] == kind
}

// a fresh node with an empty store backfills replaceable/addressable
// state however old it is (here, 200 days), reconciling it with NIP-77 rather
// than paging, and never subscribing "from now".
func TestInboundSyncFreshNodeBackfillsPersistentState(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	sk := gonostr.Generate()
	old := gonostr.Now() - 200*24*3600
	var state []gonostr.Event
	for i := range 3 {
		state = append(state, syncTestEvent(t, sk, syncTestStateKind, old+gonostr.Timestamp(i), gonostr.Tags{{"d", "svc-" + strconv.Itoa(i)}}, "healthy"))
	}
	relay.add(t, state...)

	store := openTestLocalStore(t, "")
	run := startSyncRun(t, newSyncTestPool(relay), store, nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, relay.url, 1)

	require.ElementsMatch(t, eventIDs(state...), drainIDs(run.handled))
	var negentropy, pagedState bool
	for _, req := range relay.recorded() {
		if !isKindReq(req, syncTestStateKind) {
			continue
		}
		negentropy = negentropy || req.negentropy
		pagedState = pagedState || (!req.negentropy && req.filter.Limit > 0)
	}
	require.True(t, negentropy, "persistent state is reconciled with NIP-77")
	require.False(t, pagedState, "no paging fallback is needed when NIP-77 succeeds")
	require.True(t, run.sub.IsCaughtUp())
}

// a regular-kind gap larger than one page is fetched completely by
// paging backwards with `until`, including events that share the page
// boundary's created_at.
func TestInboundSyncPagesPastAFullPageWithUntil(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	sk := gonostr.Generate()
	base := gonostr.Now() - 3600
	var audit []gonostr.Event
	for i := range 12 {
		// Pairs share a timestamp, so page boundaries fall inside a second.
		audit = append(audit, syncTestEvent(t, sk, syncTestRegularKind, base+gonostr.Timestamp(i/2), nil, "audit "+strconv.Itoa(i)))
	}
	relay.add(t, audit...)

	store := openTestLocalStore(t, "")
	run := startSyncRun(t, newSyncTestPool(relay), store, nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, relay.url, 1)

	require.ElementsMatch(t, eventIDs(audit...), drainIDs(run.handled))
	var pages, bounded int
	for _, req := range relay.recorded() {
		if isKindReq(req, syncTestRegularKind) && req.filter.Limit == 5 {
			pages++
			if req.filter.Until != 0 {
				bounded++
			}
		}
	}
	require.GreaterOrEqual(t, pages, 3, "12 events at 5 per page need at least three pages")
	require.Equal(t, pages-1, bounded, "every page after the first is bounded with until")

	// After paging, a widened-overlap REQ rechecks for backdated events
	// that may have been published during the paging window.
	// Unlimited (Limit==0) regular-kind REQs with a recent Since: 1 is the
	// live subscription, the other is the paging overlap.
	var unlimitedRecent []gonostr.Timestamp
	for _, req := range relay.recorded() {
		if isKindReq(req, syncTestRegularKind) && req.filter.Limit == 0 && req.filter.Since > gonostr.Timestamp(base+1800) {
			unlimitedRecent = append(unlimitedRecent, req.filter.Since)
		}
	}
	require.Len(t, unlimitedRecent, 2, "one overlap REQ + one live subscription (both unlimited with recent Since)")
	// The overlap Since (~now-120) is earlier than the live Since (~now-60).
	slices.Sort(unlimitedRecent)
	require.Less(t, unlimitedRecent[0], unlimitedRecent[1], "the overlap Since precedes the live subscription Since")
}

// NIP-77 reconciles in both directions: the relay's missing events are
// downloaded, and with upload enabled the local store's are published to the
// relay. A reconnect reconciles again and fetches state stored on the relay
// while the daemon was disconnected.
func TestInboundSyncNegentropyFillsGapsInBothDirections(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	sk := gonostr.Generate()
	now := gonostr.Now()
	common := syncTestEvent(t, sk, syncTestStateKind, now-500, gonostr.Tags{{"d", "common"}}, "")
	relayOnly := syncTestEvent(t, sk, syncTestStateKind, now-400, gonostr.Tags{{"d", "relay-only"}}, "")
	localOnly := syncTestEvent(t, sk, syncTestStateKind, now-300, gonostr.Tags{{"d", "local-only"}}, "")
	relay.add(t, common, relayOnly)
	store := openTestLocalStore(t, "")
	for _, ev := range []gonostr.Event{common, localOnly} {
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
	}

	cfg := syncTestConfig()
	cfg.NegentropyUpload = true
	pool := newSyncTestPool(relay)
	run := startSyncRun(t, pool, store, nil, cfg)
	run.waitCaughtUp(t, ctx, relay.url, 1)

	require.Equal(t, []gonostr.ID{relayOnly.ID}, drainIDs(run.handled), "only the relay's missing event is new")
	require.True(t, localStoreHas(store, relayOnly.ID))
	require.True(t, relay.has(localOnly.ID), "the relay's gap is filled from the local store")

	// The connection drops; state lands on the relay meanwhile.
	run.waitLive(t, ctx, relay.url, 1)
	missed := syncTestEvent(t, sk, syncTestStateKind, now-200, gonostr.Tags{{"d", "missed"}}, "")
	relay.add(t, missed)
	mr, err := pool.managedRelayFor(relay.url)
	require.NoError(t, err)
	mr.mu.Lock()
	connection := mr.relay
	mr.mu.Unlock()
	relay.resetRecorded()
	require.NoError(t, connection.Close())

	run.waitCaughtUp(t, ctx, relay.url, 2)
	waitEvent(t, ctx, run.handled, missed.ID, "the state stored while disconnected")
	require.True(t, slices.ContainsFunc(relay.recorded(), func(req recordedReq) bool { return req.negentropy }),
		"the reconnect reconciles with NIP-77 again")
}

// NegentropyUploadFilter scopes uploads to service relays: a relay allowed by
// the filter uploads, one denied does not.
func TestInboundSyncNegentropyUploadFilterScopesRelays(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()

	serviceRelay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	interopRelay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})

	sk := gonostr.Generate()
	now := gonostr.Now()

	// Both relays hold the same common event; the local store has one extra.
	common := syncTestEvent(t, sk, syncTestStateKind, now-500, gonostr.Tags{{"d", "common"}}, "")
	localOnly := syncTestEvent(t, sk, syncTestStateKind, now-300, gonostr.Tags{{"d", "local-only"}}, "")
	serviceRelay.add(t, common)
	interopRelay.add(t, common)

	store := openTestLocalStore(t, "")
	for _, ev := range []gonostr.Event{common, localOnly} {
		_, err := store.SaveEvent(ev)
		require.NoError(t, err)
	}

	cfg := syncTestConfig()
	cfg.NegentropyUpload = true
	cfg.NegentropyUploadFilter = func(relayURL string) bool {
		return relayURL == serviceRelay.url
	}

	pool := newSyncTestPool(serviceRelay, interopRelay)
	run := startSyncRun(t, pool, store, nil, cfg)
	run.waitCaughtUp(t, ctx, serviceRelay.url, 1)
	run.waitCaughtUp(t, ctx, interopRelay.url, 1)

	// The service relay receives the local-only event via upload.
	require.True(t, serviceRelay.has(localOnly.ID),
		"the service relay receives the upload")
	// The interop relay must NOT receive the upload.
	require.False(t, interopRelay.has(localOnly.ID),
		"the interop relay is excluded by NegentropyUploadFilter")
}

// A relay that refuses the NIP-77 session (NEG-ERR, e.g. a set larger than it
// reconciles) is caught up by paging the full set instead.
func TestInboundSyncNegErrFallsBackToPaging(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true, refuseNegentropy: "blocked: this filter matches more than the 2 events this relay reconciles"})
	sk := gonostr.Generate()
	now := gonostr.Now()
	var state []gonostr.Event
	for i := range 7 {
		state = append(state, syncTestEvent(t, sk, syncTestStateKind, now-100+gonostr.Timestamp(i), gonostr.Tags{{"d", "coord-" + strconv.Itoa(i)}}, ""))
	}
	relay.add(t, state...)

	store := openTestLocalStore(t, "")
	run := startSyncRun(t, newSyncTestPool(relay), store, nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, relay.url, 1)

	require.ElementsMatch(t, eventIDs(state...), drainIDs(run.handled))
	var refused bool
	var pages int
	for _, req := range relay.recorded() {
		if !isKindReq(req, syncTestStateKind) {
			continue
		}
		if req.negentropy {
			refused = true
			continue
		}
		if req.filter.Limit == 5 && req.filter.Since == 0 {
			pages++
		}
	}
	require.True(t, refused, "NIP-77 was attempted first")
	require.GreaterOrEqual(t, pages, 2, "the full set was paged, past a full page, from the beginning")
}

// Cursors are kept per (relay, filter): each relay resumes from what it
// delivered, and after a restart without Postgres (a fresh in-memory audit
// repository) nothing is handled twice because dedup is the local store.
func TestInboundSyncPerRelayCursorsSurviveRestartAndDedupWithoutPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	a := startSyncTestRelay(t, syncTestRelayOptions{})
	b := startSyncTestRelay(t, syncTestRelayOptions{})
	sk := gonostr.Generate()
	now := gonostr.Now()
	shared := syncTestEvent(t, sk, syncTestRegularKind, now-3000, nil, "on both")
	newestOnA := syncTestEvent(t, sk, syncTestRegularKind, now-2400, nil, "only on a")
	newestOnB := syncTestEvent(t, sk, syncTestRegularKind, now-2700, nil, "only on b")
	a.add(t, shared, newestOnA)
	b.add(t, shared, newestOnB)

	path := filepath.Join(t.TempDir(), "daemon.bolt")
	store, err := localstore.Open(path)
	require.NoError(t, err)
	run := startSyncRun(t, newSyncTestPool(a, b), store, nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, a.url, 1)
	run.waitCaughtUp(t, ctx, b.url, 1)
	require.ElementsMatch(t, eventIDs(shared, newestOnA, newestOnB), drainIDs(run.handled), "an event on two relays is handled once")
	hash := syncTestFilterHash(t, run.sub, false)
	run.stop()
	require.NoError(t, store.Close())

	reopened, err := localstore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	cursorA, err := reopened.Cursor(a.url, hash)
	require.NoError(t, err)
	cursorB, err := reopened.Cursor(b.url, hash)
	require.NoError(t, err)
	require.Equal(t, newestOnA.CreatedAt, cursorA)
	require.Equal(t, newestOnB.CreatedAt, cursorB, "b's cursor is what b delivered, not the newest event overall")

	a.resetRecorded()
	b.resetRecorded()
	restarted := startSyncRun(t, newSyncTestPool(a, b), reopened, nil, syncTestConfig())
	restarted.waitCaughtUp(t, ctx, a.url, 1)
	restarted.waitCaughtUp(t, ctx, b.url, 1)
	require.Empty(t, drainIDs(restarted.handled), "replay after a restart re-runs no handler")
	for relay, cursor := range map[*syncTestRelay]gonostr.Timestamp{a: cursorA, b: cursorB} {
		var resumed bool
		for _, req := range relay.recorded() {
			if isKindReq(req, syncTestRegularKind) && req.filter.Limit == 5 {
				require.Equal(t, cursor-60, req.filter.Since, "catch-up resumes at that relay's cursor less the overlap")
				resumed = true
			}
		}
		require.True(t, resumed)
	}
}

// A relay that is down while the others backfill does not hold them back, and
// is caught up on its own once it comes back.
func TestInboundSyncRelayDownDuringBackfillIsCaughtUpIndependently(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	up := startSyncTestRelay(t, syncTestRelayOptions{})
	down := startSyncTestRelay(t, syncTestRelayOptions{})
	down.down.Store(true)
	sk := gonostr.Generate()
	now := gonostr.Now()
	onUp := syncTestEvent(t, sk, syncTestRegularKind, now-600, nil, "up")
	onDown := syncTestEvent(t, sk, syncTestRegularKind, now-900, nil, "only on the relay that was down")
	up.add(t, onUp)
	down.add(t, onDown)

	store := openTestLocalStore(t, "")
	run := startSyncRun(t, newSyncTestPool(up, down), store, nil, syncTestConfig())
	run.waitCount(t, ctx, "the down relay's failed attempt", 1, func(item inboundItem) bool {
		return item.op == opRelayFailed && item.relay == down.url
	})
	run.waitCaughtUp(t, ctx, up.url, 1)
	require.True(t, run.sub.IsCaughtUp(), "one relay down does not hold back caught-up")
	hash := syncTestFilterHash(t, run.sub, false)
	cursor, err := store.Cursor(down.url, hash)
	require.NoError(t, err)
	require.Zero(t, cursor, "a relay that never sent EOSE has no cursor")
	cursor, err = store.Cursor(up.url, hash)
	require.NoError(t, err)
	require.Equal(t, onUp.CreatedAt, cursor)

	down.down.Store(false)
	run.waitCaughtUp(t, ctx, down.url, 1)
	waitEvent(t, ctx, run.handled, onDown.ID, "the event only the recovered relay has")
	cursor, err = store.Cursor(down.url, hash)
	require.NoError(t, err)
	require.Equal(t, onDown.CreatedAt, cursor)
}

func TestLoomStatusProofRequiresEveryConfiguredRelayAndLiveEOSE(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	up := startSyncTestRelay(t, syncTestRelayOptions{})
	down := startSyncTestRelay(t, syncTestRelayOptions{})
	down.down.Store(true)
	status := syncTestEvent(t, gonostr.Generate(), gonostr.Kind(KindLoomJobStatusUpdate),
		gonostr.Now()-gonostr.Timestamp((48*time.Hour)/time.Second),
		gonostr.Tags{{"d", "job-1"}, {"e", "job-1"}, {"p", "client"}, {"status", "running"}}, "")
	down.add(t, status)
	pool := newSyncTestPool(up, down)
	store := openTestLocalStore(t, "")
	run := startSyncRun(t, pool, store, nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{up.url, down.url}))
	run.waitCaughtUp(t, ctx, up.url, 1)
	run.waitLive(t, ctx, up.url, 1)
	require.False(t, run.sub.LoomStatusComplete(), "one relay's EOSE cannot prove another relay's absence")
	select {
	case <-run.sub.LoomStatusReadySignal():
		t.Fatal("incomplete relay set signalled Loom status readiness")
	default:
	}

	down.down.Store(false)
	run.waitCaughtUp(t, ctx, down.url, 1)
	run.waitLive(t, ctx, down.url, 1)
	select {
	case <-run.sub.LoomStatusReadySignal():
	case <-ctx.Done():
		t.Fatal("all relays reached live EOSE without Loom status readiness")
	}
	require.True(t, run.sub.LoomStatusComplete())
	stored, err := NewLocalEventRepository(store, nil).GetByID(ctx, status.ID.Hex())
	require.NoError(t, err)
	require.NotNil(t, stored, "the relay-only worker status must be durable before readiness")

	pool.ReconfigureRelayURLs([]string{up.url})
	require.False(t, run.sub.LoomStatusComplete(), "removing a Loom publication relay revokes proof")
	pool.ReconfigureRelayURLs([]string{up.url, down.url})
	require.False(t, run.sub.LoomStatusComplete(), "a re-added URL must not inherit its old EOSE")
	run.waitCaughtUp(t, ctx, down.url, 2)
	run.waitLive(t, ctx, down.url, 2)
	require.True(t, run.sub.LoomStatusComplete(), "the new relay incarnation can prove a fresh EOSE")

	down.down.Store(true)
	mr, err := pool.managedRelayFor(down.url)
	require.NoError(t, err)
	mr.mu.Lock()
	connection := mr.relay
	mr.mu.Unlock()
	require.NoError(t, connection.Close())
	for run.sub.LoomStatusComplete() {
		select {
		case <-run.changed:
		case <-ctx.Done():
			t.Fatal("relay disconnect did not revoke Loom status proof")
		}
	}
	down.down.Store(false)
	run.waitCaughtUp(t, ctx, down.url, 3)
	run.waitLive(t, ctx, down.url, 3)
	require.True(t, run.sub.LoomStatusComplete(), "reconnect requires fresh durable replay and live EOSE")

	run.stop()
	require.False(t, run.sub.LoomStatusComplete(), "stopped subscriptions cannot retain proof")
	restarted := NewSubscriber(pool, nil, zap.NewNop(), WithLocalStore(store),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{up.url, down.url}),
		WithInboundSync(syncTestConfig()))
	require.False(t, restarted.LoomStatusComplete(), "durable events alone do not replace a fresh relay EOSE")
	restartCtx, stopRestart := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- restarted.Run(restartCtx) }()
	select {
	case <-restarted.LoomStatusReadySignal():
	case <-ctx.Done():
		stopRestart()
		t.Fatal("restarted subscriber did not establish a fresh Loom EOSE proof")
	}
	require.True(t, restarted.LoomStatusComplete())
	stopRestart()
	require.NoError(t, <-done)
	require.False(t, restarted.LoomStatusComplete())
}

func TestLoomStatusProofNeedsExplicitWorkerRelayBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	run := startSyncRun(t, newSyncTestPool(relay), openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}))
	run.waitCaughtUp(t, ctx, relay.url, 1)
	run.waitLive(t, ctx, relay.url, 1)
	require.False(t, run.sub.LoomStatusComplete(), "ambient interop relays are not a Loom publication policy")

	misconfigured := startSyncRun(t, newSyncTestPool(relay), openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{"wss://missing-worker-relay.example"}))
	misconfigured.waitCaughtUp(t, ctx, relay.url, 1)
	misconfigured.waitLive(t, ctx, relay.url, 1)
	require.False(t, misconfigured.sub.LoomStatusComplete(), "a missing worker relay cannot be replaced by an ambient relay")
}

func TestLoomStatusProofRejectsRelayRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	up := startSyncTestRelay(t, syncTestRelayOptions{})
	refused := startSyncTestRelay(t, syncTestRelayOptions{})
	refused.relay.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
		return true, "blocked: worker status subscription refused"
	}
	run := startSyncRun(t, newSyncTestPool(up, refused), openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{up.url, refused.url}))
	run.waitCaughtUp(t, ctx, up.url, 1)
	run.waitLive(t, ctx, up.url, 1)
	run.waitCount(t, ctx, "terminal Loom REQ refusal", 1, func(item inboundItem) bool {
		return item.op == opRelayGaveUp && item.relay == refused.url
	})
	require.False(t, run.sub.LoomStatusComplete())
	select {
	case <-run.sub.LoomStatusReadySignal():
		t.Fatal("terminal CLOSED must not count as EOSE")
	default:
	}
}

func TestLoomStatusProofSurvivesUnrelatedFilterRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	relay.relay.OnRequest = func(_ context.Context, filter gonostr.Filter) (bool, string) {
		if slices.Contains(filter.Kinds, gonostr.Kind(KindCASAudit)) {
			return true, "blocked: unrelated audit subscription refused"
		}
		return false, ""
	}
	run := startSyncRun(t, newSyncTestPool(relay), openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate, KindCASAudit}), WithLoomStatusRelays([]string{relay.url}))
	select {
	case <-run.sub.LoomStatusReadySignal():
	case <-ctx.Done():
		t.Fatal("unrelated audit CLOSED permanently blocked complete Loom status REQ")
	}
	require.True(t, run.sub.LoomStatusComplete())
}

func TestLoomStatusProofLeasePinsAdmissionAgainstInvalidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	pool := newSyncTestPool(relay)
	run := startSyncRun(t, pool, openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{relay.url}))
	run.waitCaughtUp(t, ctx, relay.url, 1)
	run.waitLive(t, ctx, relay.url, 1)
	require.True(t, run.sub.LoomStatusComplete())
	archive := &blockingArchiveRepo{called: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(archive.release) })
	outbox := openDeliveryTestOutbox(t)
	publisher := NewPublisher(config.NostrConfig{PrivateKey: gonostr.Generate().Hex(), PublishEnabled: true},
		pool, archive, zap.NewNop(), WithLocalOutbox(outbox, nil))
	event := &gonostr.Event{Kind: gonostr.Kind(KindNIP38Status), CreatedAt: gonostr.Now(),
		Tags: gonostr.Tags{{"d", "run-1"}, {"t", "deployment.run.health"}}, Content: `{"state":"stale"}`}

	entered, release := make(chan struct{}), make(chan struct{})
	admitted := make(chan error, 1)
	go func() {
		admitted <- run.sub.WithLoomStatusProof(func() error {
			close(entered)
			<-release // local outbox admission has not linearized yet
			return publisher.EnqueueSignedEvent(ctx, event)
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("proof lease was not acquired")
	}
	require.False(t, pool.topologyProofMu.TryLock(), "topology invalidation cannot pass an in-flight admission")
	require.False(t, run.sub.loomProofMu.TryLock(), "EOSE invalidation cannot pass an in-flight admission")

	reconfigured := make(chan RelayPoolReconfigureResult, 1)
	go func() { reconfigured <- pool.ReconfigureRelayURLs([]string{}) }()
	invalidated := make(chan struct{})
	go func() {
		run.sub.updateLoomProof(relay.url, relayProgress{failed: true})
		close(invalidated)
	}()
	select {
	case <-reconfigured:
		t.Fatal("relay-set change crossed a held proof lease")
	default:
	}
	select {
	case <-invalidated:
		t.Fatal("relay failure crossed a held proof lease")
	default:
	}
	close(release)
	select {
	case err := <-admitted:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("admission did not finish")
	}
	entry, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.True(t, found, "signed event must be durable before proof lease releases")
	require.Equal(t, localstore.OutboxPending, entry.State)
	select {
	case <-archive.called:
		t.Fatal("proof-sensitive admission called the blocking PostgreSQL archive")
	default:
	}
	select {
	case <-reconfigured:
	case <-ctx.Done():
		t.Fatal("reconfiguration did not finish after admission")
	}
	select {
	case <-invalidated:
	case <-ctx.Done():
		t.Fatal("relay failure did not invalidate proof after admission")
	}
	require.False(t, run.sub.LoomStatusComplete())
	require.ErrorIs(t, run.sub.WithLoomStatusProof(func() error { t.Fatal("unproved admission"); return nil }), ErrLoomStatusProofIncomplete)
}

func TestLoomStatusProofRejectsUnpageableSameSecondHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	worker := gonostr.Generate()
	at := gonostr.Now() - 60
	for i := range 6 {
		job := "job-" + strconv.Itoa(i)
		relay.add(t, syncTestEvent(t, worker, gonostr.Kind(KindLoomJobStatusUpdate), at,
			gonostr.Tags{{"d", job}, {"e", job}, {"p", "client"}, {"status", "running"}}, ""))
	}
	run := startSyncRun(t, newSyncTestPool(relay), openTestLocalStore(t, ""), nil, syncTestConfig(),
		WithKinds([]int{KindLoomJobStatusUpdate}), WithLoomStatusRelays([]string{relay.url}))
	run.waitCount(t, ctx, "unpageable Loom status history refusal", 1, func(item inboundItem) bool {
		return item.op == opRelayFailed && item.relay == relay.url
	})
	require.False(t, run.sub.LoomStatusComplete(), "skipping same-second events cannot count as complete history")
}

// the daemon's own events, stored or live and however new, never
// advance an inbound cursor; they reach observers but not handlers.
func TestInboundSyncSelfPublishedEventsDoNotAdvanceCursors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	self := gonostr.Generate()
	foreign := gonostr.Generate()
	now := gonostr.Now()
	foreignStored := syncTestEvent(t, foreign, syncTestRegularKind, now-1800, nil, "foreign")
	selfStored := syncTestEvent(t, self, syncTestRegularKind, now-60, nil, "self")
	relay.add(t, foreignStored, selfStored)

	store := openTestLocalStore(t, "")
	run := startSyncRun(t, newSyncTestPool(relay), store, nil, syncTestConfig(), WithSelfAuthors(self.Public().Hex()))
	hash := syncTestFilterHash(t, run.sub, false)
	cursor := func() gonostr.Timestamp {
		value, err := store.Cursor(relay.url, hash)
		require.NoError(t, err)
		return value
	}
	run.waitLive(t, ctx, relay.url, 2)
	require.Equal(t, foreignStored.CreatedAt, cursor())
	require.Equal(t, []gonostr.ID{foreignStored.ID}, drainIDs(run.handled), "the daemon's own events never reach handlers")

	selfLive := syncTestEvent(t, self, syncTestRegularKind, gonostr.Now(), nil, "self live")
	relay.publishLive(t, selfLive)
	run.waitApplied(t, ctx, selfLive.ID)
	waitEvent(t, ctx, run.observed, selfLive.ID, "the live self-published event reaching observers")
	require.Equal(t, foreignStored.CreatedAt, cursor(), "a live self-published event does not advance the cursor")

	foreignLive := syncTestEvent(t, foreign, syncTestRegularKind, gonostr.Now()-5, nil, "foreign live")
	relay.publishLive(t, foreignLive)
	run.waitApplied(t, ctx, foreignLive.ID)
	waitEvent(t, ctx, run.handled, foreignLive.ID, "the live foreign event")
	require.Equal(t, foreignLive.CreatedAt, cursor(), "a live foreign event after EOSE advances the cursor")
}

// A relay that CLOSEs the REQs "auth-required:" is caught up once the pool's
// connection AuthHandler has answered its NIP-42 challenge: the subscriber
// resyncs the relay like after any CLOSED, with no AUTH logic of its own.
func TestInboundSyncCatchesUpAnAuthRequiredRelayThroughThePoolAuthHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	relay.relay.OnRequest = func(ctx context.Context, _ gonostr.Filter) (bool, string) {
		if _, ok := khatru.GetAuthed(ctx); !ok {
			return true, "auth-required: authenticated clients only"
		}
		return false, ""
	}
	ev := syncTestEvent(t, gonostr.Generate(), syncTestRegularKind, gonostr.Now()-60, nil, "behind NIP-42")
	relay.add(t, ev)
	pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithPrivateKey(gonostr.Generate().Hex()))
	pool.newReconnectBackoff = fastTestBackoff
	defer pool.Close()

	run := startSyncRun(t, pool, openTestLocalStore(t, ""), nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, relay.url, 1)
	require.Equal(t, eventIDs(ev), drainIDs(run.handled))
}
