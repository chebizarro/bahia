package controlplane

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip59"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"go.uber.org/zap"
)

const contextVMTestMethod = "test/mutate"

// contextVMTestBound bounds every wait in these tests; none of them sleeps.
const contextVMTestBound = 20 * time.Second

func contextVMServicePubkey(t *testing.T) nostr.PubKey {
	t.Helper()
	return testNostrPubKeyFromPrivateKey(t, testServiceKey)
}

// startContextVMTestRelay runs an in-process khatru relay that stores regular
// events (1059) and only forwards ephemeral ones (25910, 21059), as relays do.
func startContextVMTestRelay(t *testing.T) string {
	t.Helper()
	relay := khatru.NewRelay()
	events := &slicestore.SliceStore{}
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(events.Close)
	relay.UseEventstore(events, 500)
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func publishToContextVMTestRelay(t *testing.T, ctx context.Context, url string, ev nostr.Event) {
	t.Helper()
	relay, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	if err := relay.Publish(ctx, ev); err != nil {
		t.Fatalf("publish %d to %s: %v", ev.Kind, url, err)
	}
}

// contextVMRumor is an unsigned NIP-59 rumor carrying a JSON-RPC request.
func contextVMRumor(t *testing.T, content string) nostr.Event {
	t.Helper()
	rumor := nostr.Event{
		Kind:      KindContextVMMessage,
		PubKey:    testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", contextVMServicePubkey(t).Hex()}},
		Content:   content,
	}
	rumor.ID = rumor.GetID()
	return rumor
}

// giftWrapContextVMRumor wraps rumor per NIP-59 with an outer created_at the
// caller picks, standing in for the sender's random backdating.
func giftWrapContextVMRumor(t *testing.T, rumor nostr.Event, kind nostr.Kind, outerCreatedAt nostr.Timestamp) nostr.Event {
	t.Helper()
	signer, err := NewPrivateKeySigner(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	recipient := contextVMServicePubkey(t)
	ctx := context.Background()
	wrap, err := nip59.GiftWrap(rumor, recipient,
		func(plaintext string) (string, error) { return signer.Encrypt(ctx, plaintext, recipient) },
		func(seal *nostr.Event) error { return signer.SignEvent(ctx, seal) },
		func(wrap *nostr.Event) { wrap.Kind, wrap.CreatedAt = kind, outerCreatedAt },
	)
	if err != nil {
		t.Fatal(err)
	}
	return wrap
}

type contextVMChannelPublisher struct{ events chan nostr.Event }

func (p *contextVMChannelPublisher) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	select {
	case p.events <- ev:
		return 1, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// contextVMTestDaemon is one daemon process: its own pool, store handle and
// transport over a store path that outlives it.
type contextVMTestDaemon struct {
	t         *testing.T
	store     *localstore.Store
	pool      *nostrpool.RelayPool
	published chan nostr.Event
	caughtUp  chan string
	cancel    context.CancelFunc
	done      chan error
}

func startContextVMTestDaemon(t *testing.T, path string, relays []string, calls *atomic.Int32) *contextVMTestDaemon {
	t.Helper()
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := &contextVMTestDaemon{
		t:         t,
		store:     store,
		pool:      nostrpool.NewRelayPool(relays, zap.NewNop()),
		published: make(chan nostr.Event, 64),
		caughtUp:  make(chan string, 64),
		done:      make(chan error, 1),
	}
	transport := NewEncryptedRequestTransport(d.pool, newResponder(t, &contextVMChannelPublisher{events: d.published}), contextVMTestAuthorizedPubkeys(t), zap.NewNop(), WithContextVMLocalStore(store))
	transport.contextVMLocal.onCaughtUp = func(relayURL string, _ nostr.Timestamp) { d.caughtUp <- relayURL }
	transport.RegisterContextVMHandler(contextVMTestMethod, func(context.Context, ContextVMRequest) (any, error) {
		return map[string]int32{"execution": calls.Add(1)}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	d.cancel = cancel
	go func() { d.done <- transport.Run(ctx) }()
	return d
}

func (d *contextVMTestDaemon) awaitCaughtUp(relays ...string) {
	d.t.Helper()
	pending := slices.Clone(relays)
	timeout := time.After(contextVMTestBound)
	for len(pending) > 0 {
		select {
		case relayURL := <-d.caughtUp:
			pending = slices.DeleteFunc(pending, func(url string) bool { return url == relayURL })
		case err := <-d.done:
			d.t.Fatalf("transport stopped before EOSE on %v: %v", pending, err)
		case <-timeout:
			d.t.Fatalf("no EOSE commit on %v", pending)
		}
	}
}

func (d *contextVMTestDaemon) nextTerminal() ContextVMJSONRPCResponse {
	d.t.Helper()
	timeout := time.After(contextVMTestBound)
	for {
		select {
		case ev := <-d.published:
			inner := ev
			if ev.Kind != KindContextVMMessage {
				inner = unwrapContextVMResponseEvent(d.t, ev, testRequesterKey)
			}
			if strings.Contains(inner.Content, ContextVMProgressNotificationMethod) {
				continue
			}
			return contextVMResponse(d.t, inner)
		case <-timeout:
			d.t.Fatal("no terminal ContextVM response")
			return ContextVMJSONRPCResponse{}
		}
	}
}

// assertNothingPublished is exact after awaitCaughtUp: a relay's stored events
// are processed, and any response to them published, before its EOSE commit.
func (d *contextVMTestDaemon) assertNothingPublished() {
	d.t.Helper()
	select {
	case ev := <-d.published:
		d.t.Fatalf("unexpected ContextVM publication of kind %d", ev.Kind)
	default:
	}
}

func (d *contextVMTestDaemon) stop() {
	d.t.Helper()
	d.cancel()
	if err := <-d.done; err != nil && err != context.Canceled {
		d.t.Fatalf("transport stopped with %v", err)
	}
	d.pool.Close()
	if err := d.store.Close(); err != nil {
		d.t.Fatal(err)
	}
}

func contextVMStoredCursor(t *testing.T, path, relayURL string) nostr.Timestamp {
	t.Helper()
	store, err := localstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cursor, err := store.ContextVMCursor(relayURL, contextVMServicePubkey(t).Hex())
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

func assertContextVMExecution(t *testing.T, response ContextVMJSONRPCResponse, wantID string, wantExecution int32) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("response error: %+v", response.Error)
	}
	if string(response.ID) != `"`+wantID+`"` {
		t.Fatalf("response id = %s, want %q", response.ID, wantID)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Execution int32 `json:"execution"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Execution != wantExecution {
		t.Fatalf("response result = %s, want execution %d", raw, wantExecution)
	}
}

func TestContextVMStoredWrapPublishedDuringDowntimeRunsOnceAcrossRestarts(t *testing.T) {
	ctx := t.Context()
	relayURL := startContextVMTestRelay(t)
	path := filepath.Join(t.TempDir(), "contextvm.bolt")
	var calls atomic.Int32

	first := startContextVMTestDaemon(t, path, []string{relayURL}, &calls)
	first.awaitCaughtUp(relayURL)
	first.stop()
	cursor := contextVMStoredCursor(t, path, relayURL)
	if cursor == 0 {
		t.Fatal("no cursor committed at EOSE")
	}

	// While the daemon is down: a stored request whose NIP-59 outer
	// created_at is backdated 47h, far behind the committed cursor (and the
	// old fixed 12h window), and an ephemeral plaintext request.
	rumor := contextVMRumor(t, `{"jsonrpc":"2.0","id":"offline","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"offline-1"}}}`)
	backdated := giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, cursor-nostr.Timestamp((47*time.Hour)/time.Second))
	publishToContextVMTestRelay(t, ctx, relayURL, backdated)
	// Relays may refuse it outright (khatru: "mute: no one was listening");
	// either way it is not stored, so it cannot be recovered.
	ephemeral := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"lost","method":"`+contextVMTestMethod+`","params":{}}`)
	if writer, err := nostr.RelayConnect(ctx, relayURL, nostr.RelayOptions{}); err == nil {
		_ = writer.Publish(ctx, *ephemeral)
		writer.Close()
	}

	second := startContextVMTestDaemon(t, path, []string{relayURL}, &calls)
	original := second.nextTerminal()
	assertContextVMExecution(t, original, "offline", 1)
	second.awaitCaughtUp(relayURL)
	second.assertNothingPublished() // the ephemeral request was never stored
	second.stop()
	if calls.Load() != 1 {
		t.Fatalf("handler calls after recovery = %d, want 1", calls.Load())
	}

	// The relay replays the stored wrap to the next process: not executed,
	// and not answered a second time.
	third := startContextVMTestDaemon(t, path, []string{relayURL}, &calls)
	defer third.stop()
	third.awaitCaughtUp(relayURL)
	third.assertNothingPublished()

	// A client retry re-wraps the same request: the stored response is
	// replayed without executing it again.
	rewrapped := giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, nostr.Now()-nostr.Timestamp((30*time.Hour)/time.Second))
	publishToContextVMTestRelay(t, ctx, relayURL, rewrapped)
	assertContextVMExecution(t, third.nextTerminal(), "offline", 1)

	// A new request reusing the idempotency key gets the same outcome under
	// its own JSON-RPC id.
	retry := contextVMRumor(t, `{"jsonrpc":"2.0","id":"retry","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"offline-1"}}}`)
	publishToContextVMTestRelay(t, ctx, relayURL, giftWrapContextVMRumor(t, retry, KindContextVMGiftWrap, nostr.Now()))
	assertContextVMExecution(t, third.nextTerminal(), "retry", 1)
	if calls.Load() != 1 {
		t.Fatalf("handler calls after duplicates = %d, want 1", calls.Load())
	}
}

func TestContextVMRelayCursorsAreIndependent(t *testing.T) {
	ctx := t.Context()
	relayA := startContextVMTestRelay(t)
	relayB := startContextVMTestRelay(t)
	path := filepath.Join(t.TempDir(), "contextvm.bolt")
	var calls atomic.Int32

	first := startContextVMTestDaemon(t, path, []string{relayA}, &calls)
	first.awaitCaughtUp(relayA)
	first.stop()
	cursorA := contextVMStoredCursor(t, path, relayA)
	if cursorA == 0 || contextVMStoredCursor(t, path, relayB) != 0 {
		t.Fatalf("cursors after first run: a=%d b=%d", cursorA, contextVMStoredCursor(t, path, relayB))
	}

	// A request that only relay B holds, backdated past relay A's cursor.
	rumor := contextVMRumor(t, `{"jsonrpc":"2.0","id":"b-only","method":"`+contextVMTestMethod+`","params":{}}`)
	publishToContextVMTestRelay(t, ctx, relayB, giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, cursorA-nostr.Timestamp((40*time.Hour)/time.Second)))

	second := startContextVMTestDaemon(t, path, []string{relayA, relayB}, &calls)
	assertContextVMExecution(t, second.nextTerminal(), "b-only", 1)
	second.awaitCaughtUp(relayA, relayB)
	second.assertNothingPublished()
	second.stop()
	if got := contextVMStoredCursor(t, path, relayA); got < cursorA {
		t.Fatalf("relay A cursor moved back: %d < %d", got, cursorA)
	}
	if contextVMStoredCursor(t, path, relayB) == 0 {
		t.Fatal("relay B cursor not committed")
	}
	if calls.Load() != 1 {
		t.Fatalf("handler calls = %d, want 1", calls.Load())
	}
}

// contextVMScriptedSub is one REQ handed out by contextVMScriptedPool.
type contextVMScriptedSub struct {
	relayURL string
	filters  []nostr.Filter
	opts     nostrpool.SubscribeOptions
	events   chan *nostr.Event
	eose     chan nostrpool.RelayEOSE
}

type contextVMScriptedPool struct {
	urls []string
	subs chan *contextVMScriptedSub
}

func newContextVMScriptedPool(urls ...string) *contextVMScriptedPool {
	return &contextVMScriptedPool{urls: urls, subs: make(chan *contextVMScriptedSub, 16)}
}

func (p *contextVMScriptedPool) URLs() []string { return p.urls }

func (p *contextVMScriptedPool) WaitForTopologyChange(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (p *contextVMScriptedPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	panic("the local-store transport subscribes per relay")
}

func (p *contextVMScriptedPool) SubscribeWithOptions(_ context.Context, filters []nostr.Filter, opts nostrpool.SubscribeOptions) (*nostrpool.MergedSubscription, error) {
	sub := &contextVMScriptedSub{relayURL: opts.Relays[0], filters: filters, opts: opts, events: make(chan *nostr.Event), eose: make(chan nostrpool.RelayEOSE)}
	p.subs <- sub
	return &nostrpool.MergedSubscription{Events: sub.events, RelayEOSE: sub.eose}, nil
}

func (p *contextVMScriptedPool) nextSub(t *testing.T) *contextVMScriptedSub {
	t.Helper()
	select {
	case sub := <-p.subs:
		return sub
	case <-time.After(contextVMTestBound):
		t.Fatal("no subscription opened")
		return nil
	}
}

func contextVMSend[T any](t *testing.T, ch chan<- T, value T) {
	t.Helper()
	select {
	case ch <- value:
	case <-time.After(contextVMTestBound):
		t.Fatal("transport did not receive")
	}
}

func contextVMReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(contextVMTestBound):
		t.Fatal("nothing received")
		var zero T
		return zero
	}
}

// startScriptedContextVMTransport runs a transport over pool whose response
// handler reports each delivered response's id: a stand-in for any event
// the subscription carries.
func startScriptedContextVMTransport(t *testing.T, store *localstore.Store, pool *contextVMScriptedPool) (<-chan string, <-chan nostr.Timestamp) {
	t.Helper()
	transport := NewEncryptedRequestTransport(pool, newResponder(t, &mockEncryptedPublisher{}), nil, zap.NewNop(), WithContextVMLocalStore(store))
	processed := make(chan string, 16)
	commits := make(chan nostr.Timestamp, 16)
	transport.RegisterContextVMResponseHandler(func(_ context.Context, envelope ContextVMResponseEnvelope) { processed <- envelope.Event.ID.Hex() })
	transport.contextVMLocal.onCaughtUp = func(_ string, cursor nostr.Timestamp) { commits <- cursor }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- transport.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return processed, commits
}

func contextVMTestResponseEvent(t *testing.T, id string) *nostr.Event {
	t.Helper()
	return makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"`+id+`","result":{}}`)
}

func TestContextVMResumeSinceUsesEachRelaysCursorBoundedByTheAgeFloor(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "contextvm.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	// A ledger in use for a month, so the age floor (now - 7d) applies.
	if _, err := store.ContextVMLedgerEpoch(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	service := contextVMServicePubkey(t).Hex()
	ago := func(d time.Duration) nostr.Timestamp { return nostr.Timestamp(now.Add(-d).Unix()) }
	cursors := map[string]nostr.Timestamp{
		"wss://recent.example": ago(time.Hour),
		"wss://older.example":  ago(72 * time.Hour),
		"wss://stale.example":  ago(20 * 24 * time.Hour),
	}
	for relayURL, cursor := range cursors {
		if err := store.AdvanceContextVMCursor(relayURL, service, cursor); err != nil {
			t.Fatal(err)
		}
	}
	overlap := 49 * time.Hour
	floor := 7 * 24 * time.Hour
	want := map[string]nostr.Timestamp{
		"wss://recent.example": ago(time.Hour + overlap),
		"wss://older.example":  ago(72*time.Hour + overlap),
		"wss://stale.example":  ago(floor + overlap), // the floor bounds the replay
		"wss://cold.example":   ago(floor + overlap), // no cursor yet
	}
	pool := newContextVMScriptedPool("wss://recent.example", "wss://older.example", "wss://stale.example", "wss://cold.example")
	startScriptedContextVMTransport(t, store, pool)
	for range want {
		sub := pool.nextSub(t)
		if len(sub.filters) != 1 || len(sub.opts.Relays) != 1 || !sub.opts.AwaitUnavailableRelays || sub.opts.ResumeOverlap != overlap {
			t.Fatalf("%s: subscription = %+v / %+v", sub.relayURL, sub.filters, sub.opts)
		}
		filter := sub.filters[0]
		if !slices.Equal(filter.Kinds, []nostr.Kind{KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap}) || !slices.Equal(filter.Tags["p"], []string{service}) {
			t.Fatalf("%s: filter = %+v", sub.relayURL, filter)
		}
		if diff := filter.Since - want[sub.relayURL]; diff < -2 || diff > 2 {
			t.Fatalf("%s: since = %v, want %v", sub.relayURL, filter.Since.Time(), want[sub.relayURL].Time())
		}
	}
}

func TestContextVMCursorCommitsOnlyAtItsOwnREQsEOSE(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "contextvm.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := contextVMServicePubkey(t).Hex()
	pool := newContextVMScriptedPool("wss://relay.example")
	before := nostr.Now()
	processed, commits := startScriptedContextVMTransport(t, store, pool)
	sub := pool.nextSub(t)

	stored := contextVMTestResponseEvent(t, "stored")
	contextVMSend(t, sub.events, stored)
	if got := contextVMReceive(t, processed); got != stored.ID.Hex() {
		t.Fatalf("processed %s", got)
	}
	if cursor, _ := store.ContextVMCursor("wss://relay.example", service); cursor != 0 {
		t.Fatalf("cursor before EOSE = %d", cursor)
	}

	contextVMSend(t, sub.eose, nostrpool.RelayEOSE{RelayURL: "wss://relay.example"})
	anchor := contextVMReceive(t, commits)
	if anchor < before || anchor > nostr.Now() {
		t.Fatalf("committed anchor %d outside [%d, now]", anchor, before)
	}
	if cursor, _ := store.ContextVMCursor("wss://relay.example", service); cursor != anchor {
		t.Fatalf("stored cursor = %d, want %d", cursor, anchor)
	}

	// An EOSE from a REQ the pool reissued commits nothing. The next event
	// is received only after that EOSE was handled (unbuffered channels,
	// one follower goroutine), so no commit can still be in flight.
	contextVMSend(t, sub.eose, nostrpool.RelayEOSE{RelayURL: "wss://relay.example"})
	live := contextVMTestResponseEvent(t, "live")
	contextVMSend(t, sub.events, live)
	contextVMReceive(t, processed)
	select {
	case cursor := <-commits:
		t.Fatalf("reissued EOSE committed %d", cursor)
	default:
	}
	// A replay of an already processed event is skipped.
	contextVMSend(t, sub.events, stored)
	marker := contextVMTestResponseEvent(t, "marker")
	contextVMSend(t, sub.events, marker)
	if got := contextVMReceive(t, processed); got != marker.ID.Hex() {
		t.Fatalf("replayed delivery re-dispatched: %s", got)
	}
}

func TestContextVMReissuedEOSECommitsCursor(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "contextvm.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pool := newContextVMScriptedPool("wss://relay.example")
	processed, commits := startScriptedContextVMTransport(t, store, pool)
	sub := pool.nextSub(t)

	// Initial EOSE commits the original anchor.
	contextVMSend(t, sub.eose, nostrpool.RelayEOSE{RelayURL: "wss://relay.example"})
	initialAnchor := contextVMReceive(t, commits)

	// A live event is delivered normally.
	contextVMSend(t, sub.events, contextVMTestResponseEvent(t, "live-1"))
	contextVMReceive(t, processed)

	// A reissued EOSE (pool reconnected and replayed) advances the cursor.
	// Use initialAnchor+10 to guarantee it is strictly after the original,
	// even when both would round to the same second.
	reissueTime := initialAnchor + 10
	contextVMSend(t, sub.eose, nostrpool.RelayEOSE{
		RelayURL:   "wss://relay.example",
		Reissued:   true,
		ReissuedAt: reissueTime,
	})
	reissuedAnchor := contextVMReceive(t, commits)
	if reissuedAnchor != reissueTime {
		t.Fatalf("reissued cursor = %d, want reissueTime %d", reissuedAnchor, reissueTime)
	}
	if reissuedAnchor <= initialAnchor {
		t.Fatalf("reissued cursor %d <= initial %d", reissuedAnchor, initialAnchor)
	}

	// No second subscription should have been opened (no re-anchor loop).
	select {
	case <-pool.subs:
		t.Fatal("unexpected second subscription: re-anchor loop should be gone")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestContextVMPruneLockedEvictsOldGiftWraps(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "contextvm.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now()
	if _, err := store.ContextVMLedgerEpoch(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Store two gift-wrap events: one old (beyond the cutoff) and one recent.
	oldTime := now.Add(-10 * 24 * time.Hour) // 10 days ago
	recentTime := now.Add(-1 * time.Hour)    // 1 hour ago

	makeWrap := func(t *testing.T, createdAt time.Time) nostr.Event {
		t.Helper()
		rumor := contextVMRumor(t, `{"jsonrpc":"2.0","id":"prune-`+createdAt.String()+`","result":{}}`)
		wrap := giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, nostr.Timestamp(createdAt.Unix()))
		return wrap
	}

	oldWrap := makeWrap(t, oldTime)
	recentWrap := makeWrap(t, recentTime)
	if _, err := store.SaveEvent(oldWrap); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveEvent(recentWrap); err != nil {
		t.Fatal(err)
	}

	// Verify both are stored.
	countEvents := func() int {
		n := 0
		for range store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{KindContextVMGiftWrap}}) {
			n++
		}
		return n
	}
	if n := countEvents(); n != 2 {
		t.Fatalf("before prune: expected 2 events, got %d", n)
	}

	local := &contextVMLocalState{store: store}
	local.pruneLocked(now, zap.NewNop())

	// The old event should be pruned; the recent one stays.
	if n := countEvents(); n != 1 {
		t.Fatalf("after prune: expected 1 event, got %d", n)
	}
}

func TestContextVMLedgerWithoutRelays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contextvm.bolt")
	authorized := contextVMTestAuthorizedPubkeys(t)
	var calls atomic.Int32
	newTransport := func(store *localstore.Store, publisher *mockEncryptedPublisher, opts ...EncryptedRequestTransportOption) *EncryptedRequestTransport {
		transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), authorized, zap.NewNop(), append(opts, WithContextVMLocalStore(store))...)
		transport.RegisterContextVMHandler(contextVMTestMethod, func(context.Context, ContextVMRequest) (any, error) {
			return map[string]int32{"execution": calls.Add(1)}, nil
		})
		return transport
	}
	open := func() *localstore.Store {
		store, err := localstore.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	terminals := func(publisher *mockEncryptedPublisher) []ContextVMJSONRPCResponse {
		publisher.mu.Lock()
		defer publisher.mu.Unlock()
		var out []ContextVMJSONRPCResponse
		for _, ev := range publisher.events {
			inner := ev
			if ev.Kind != KindContextVMMessage {
				inner = unwrapContextVMResponseEvent(t, ev, testRequesterKey)
			}
			if !strings.Contains(inner.Content, ContextVMProgressNotificationMethod) {
				out = append(out, contextVMResponse(t, inner))
			}
		}
		return out
	}

	t.Run("unkeyed request is not re-executed after restart", func(t *testing.T) {
		calls.Store(0)
		rumor := contextVMRumor(t, `{"jsonrpc":"2.0","id":"no-key","method":"`+contextVMTestMethod+`","params":{}}`)
		store := open()
		first := &mockEncryptedPublisher{}
		newTransport(store, first).HandleEvent(t.Context(), contextVMPtr(giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, nostr.Now())))
		_ = store.Close()

		store = open()
		defer store.Close()
		second := &mockEncryptedPublisher{}
		newTransport(store, second).HandleEvent(t.Context(), contextVMPtr(giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, nostr.Now())))
		if calls.Load() != 1 || len(terminals(first)) != 1 {
			t.Fatalf("calls = %d, first terminals = %d", calls.Load(), len(terminals(first)))
		}
		got := terminals(second)
		if len(got) != 1 || got[0].Error == nil || got[0].Error.Code != ContextVMDuplicateRequestErrorCode {
			t.Fatalf("re-wrapped unkeyed duplicate = %+v", got)
		}
	})

	t.Run("interrupted request is answered as unknown, not re-run", func(t *testing.T) {
		calls.Store(0)
		rumor := contextVMRumor(t, `{"jsonrpc":"2.0","id":"crashed","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"crash-1"}}}`)
		store := open()
		defer store.Close()
		// A previous process claimed it and died before completing.
		if _, err := store.ClaimContextVMRequest(localstore.ContextVMRequest{DeliveryID: "earlier-wrap", RequestID: rumor.ID.Hex(), CreatedAt: rumor.CreatedAt}, time.Now()); err != nil {
			t.Fatal(err)
		}
		publisher := &mockEncryptedPublisher{}
		newTransport(store, publisher).HandleEvent(t.Context(), contextVMPtr(giftWrapContextVMRumor(t, rumor, KindContextVMGiftWrap, nostr.Now())))
		got := terminals(publisher)
		if calls.Load() != 0 || len(got) != 1 || got[0].Error == nil || got[0].Error.Code != ContextVMDuplicateRequestErrorCode {
			t.Fatalf("calls = %d, responses = %+v", calls.Load(), got)
		}
	})

	t.Run("Postgres response from before the ledger is adopted", func(t *testing.T) {
		calls.Store(0)
		responses := newMemoryContextVMResponseStore()
		earlier := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"pre","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"pg-1"}}}`)
		legacy := NewEncryptedRequestTransport(nil, newResponder(t, &mockEncryptedPublisher{}), authorized, zap.NewNop(), WithContextVMResponseStore(responses, time.Hour))
		legacy.RegisterContextVMHandler(contextVMTestMethod, func(context.Context, ContextVMRequest) (any, error) {
			return map[string]int32{"execution": calls.Add(1)}, nil
		})
		legacy.HandleEvent(t.Context(), earlier)

		store := open()
		defer store.Close()
		publisher := &mockEncryptedPublisher{}
		retry := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"post","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"pg-1"}}}`)
		newTransport(store, publisher, WithContextVMResponseStore(responses, time.Hour)).HandleEvent(t.Context(), retry)
		// Without Postgres, the ledger alone now replays it.
		again := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"again","method":"`+contextVMTestMethod+`","params":{"_meta":{"progressToken":"pg-1"}}}`)
		newTransport(store, publisher).HandleEvent(t.Context(), again)
		got := terminals(publisher)
		if calls.Load() != 1 || len(got) != 2 {
			t.Fatalf("calls = %d, responses = %+v", calls.Load(), got)
		}
		assertContextVMExecution(t, got[0], "post", 1)
		assertContextVMExecution(t, got[1], "again", 1)
	})
}

func contextVMPtr[T any](value T) *T { return &value }

// storeFilteredSubscriber stands in for a subscriber with its own persisted
// delivery dedup, such as the DNS agent's StoreBackedSubscriber.
type storeFilteredSubscriber struct{}

func (storeFilteredSubscriber) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	panic("must not subscribe")
}

func (storeFilteredSubscriber) WaitForTopologyChange(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestContextVMLedgerRefusesASecondStoreLayer(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "contextvm.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	transport := NewEncryptedRequestTransport(storeFilteredSubscriber{}, newResponder(t, &mockEncryptedPublisher{}), nil, zap.NewNop(), WithContextVMLocalStore(store))
	if err := transport.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "one store layer") {
		t.Fatalf("Run = %v, want refusal of a second dedup layer", err)
	}
}
