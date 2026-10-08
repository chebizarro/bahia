package relaysidecar

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestSidecarREQGapEventSavedDuringStoredQueryIsDelivered covers khatru's
// REQ gap: khatru runs the stored query before it registers
// the live listener, so an event saved in between used to be neither replayed
// nor delivered live. The test holds the REQ between the two steps, publishes
// into the gap, and requires the event to arrive exactly once. It also
// requires an event the stored query already returned, but whose live dispatch
// lands in the gap, not to be delivered twice.
func TestSidecarREQGapEventSavedDuringStoredQueryIsDelivered(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	server, relayURL := startSidecarForFanoutTest(t)
	relay := server.Relay()

	queried := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	storedQuery := relay.QueryStored
	relay.QueryStored = func(qctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		events := storedQuery(qctx, filter)
		return func(yield func(nostr.Event) bool) {
			gap := khatru.GetSubscriptionID(qctx) == "gap"
			inside := false
			for event := range events {
				if gap && !inside {
					inside = true
					// Hold inside the stored query, after its first event: the
					// listener is registered (listener-before-query) and the
					// fanout is buffering live matches, while the query's
					// replay and its deduplicating drain are still ahead.
					close(queried)
					<-release
				}
				if !yield(event) {
					return
				}
			}
		}
	}

	sk := nostr.Generate()
	filter := nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}}
	before := signedFanoutEvent(t, sk, 1, 0)
	_, err := relay.AddEvent(ctx, before)
	require.NoError(t, err)
	// saved but not yet dispatched when the REQ arrives
	lateDispatch := signedFanoutEvent(t, sk, 1, 1)
	require.NoError(t, server.store.Save(ctx, lateDispatch))

	client := dialRawRelay(t, ctx, relayURL)
	client.send("REQ", "gap", filter)
	select {
	case <-queried:
	case <-ctx.Done():
		t.Fatal("stored query never ran")
	}
	server.fanout.dispatch(lateDispatch)
	inGap := signedFanoutEvent(t, sk, 1, 2)
	_, err = relay.AddEvent(ctx, inGap)
	require.NoError(t, err)
	releaseOnce.Do(func() { close(release) })

	// A sentinel published after EOSE bounds the read: the writer is FIFO, so
	// anything owed to the subscription arrives before it.
	counts := map[nostr.ID]int{}
	var sentinel nostr.Event
	for sentinel.ID == (nostr.ID{}) || counts[sentinel.ID] == 0 {
		frame := client.next()
		require.Equal(t, "gap", frame.subID, "unexpected %s frame", frame.label)
		switch frame.label {
		case "EVENT":
			counts[frame.event.ID]++
		case "EOSE":
			sentinel = signedFanoutEvent(t, sk, 1, 3)
			_, err := relay.AddEvent(ctx, sentinel)
			require.NoError(t, err)
		default:
			t.Fatalf("unexpected %s %q", frame.label, frame.reason)
		}
	}
	require.Equal(t, map[nostr.ID]int{
		before.ID: 1, lateDispatch.ID: 1, inGap.ID: 1, sentinel.ID: 1,
	}, counts)
}

// TestSidecarOverflowCloseRemovesListenerAndCounts: after an
// overflow CLOSED the khatru listener is removed server-side instead of
// lingering until the client disconnects, the overflow counter is exported on
// /metrics, and re-REQing the same id afterwards works.
func TestSidecarOverflowCloseRemovesListenerAndCounts(t *testing.T) {
	const queueSize = 2
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.SubscriberQueueSize = queueSize
	server, err := New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	relay := server.Relay()
	require.Equal(t, queueSize, server.fanout.subscriberQueueSize())

	removed := make(chan string, 8)
	onRemoved := relay.OnListenerRemoved
	relay.OnListenerRemoved = func(ws *khatru.WebSocket, ssid int, id string, filter nostr.Filter) {
		onRemoved(ws, ssid, id, filter)
		removed <- id
	}
	stalled := make(chan struct{})
	release := make(chan struct{})
	var stallOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	setFanoutForTest(server.fanout, 0, func(ws *khatru.WebSocket, v any) error {
		if env, ok := v.(nostr.EventEnvelope); ok && *env.SubscriptionID == "slow" {
			stallOnce.Do(func() {
				close(stalled)
				<-release
			})
		}
		return ws.WriteJSON(v)
	})

	sk := nostr.Generate()
	filter := nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}}
	slow := dialRawRelay(t, ctx, httpServer.URL)
	slow.subscribe("slow", filter)
	require.Len(t, relay.GetListeningFilters(), 1)

	_, err = relay.AddEvent(ctx, signedFanoutEvent(t, sk, 1, 0))
	require.NoError(t, err)
	select {
	case <-stalled:
	case <-ctx.Done():
		t.Fatal("writer never picked up the first delivery")
	}
	for i := 1; i <= queueSize+1; i++ { // fill the queue, then overflow it
		_, err := relay.AddEvent(ctx, signedFanoutEvent(t, sk, 1, i))
		require.NoError(t, err)
	}
	releaseOnce.Do(func() { close(release) })
	for {
		frame := slow.next()
		require.Equal(t, "slow", frame.subID)
		if frame.label == "CLOSED" {
			require.Equal(t, subscriberOverflowReason, frame.reason)
			break
		}
	}

	select {
	case id := <-removed:
		require.Equal(t, "slow", id)
	case <-ctx.Done():
		t.Fatal("khatru listener was not removed after the overflow CLOSED")
	}
	require.Empty(t, relay.GetListeningFilters())
	require.Equal(t, uint64(1), server.fanout.OverflowCloses())

	resp, err := http.Get(httpServer.URL + metricsPath)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "bahia_relay_sidecar_subscription_overflow_closes_total 1\n")
	require.Contains(t, string(body), "bahia_relay_sidecar_subscriber_queue_size 2\n")

	// The client re-subscribes under the same id and receives live events.
	slow.subscribe("slow", nostr.Filter{Kinds: []nostr.Kind{1}, Authors: []nostr.PubKey{sk.Public()}, Since: nostr.Timestamp(1790000100)})
	require.Len(t, relay.GetListeningFilters(), 1)
	next := signedFanoutEvent(t, sk, 1, 200)
	_, err = relay.AddEvent(ctx, next)
	require.NoError(t, err)
	frame := slow.next()
	require.Equal(t, "EVENT", frame.label)
	require.Equal(t, "slow", frame.subID)
	require.Equal(t, next.ID, frame.event.ID)
}

// TestLiveFanoutRemovesListenersOfClosedREQs: a listener khatru adds for a REQ
// that was already closed (its context cancelled before or while the stored
// query ran), or for a later filter of a subscription that already overflowed,
// is removed server-side and never delivers. A REQ whose buffer overflowed is
// CLOSED when its stored query ends.
func TestLiveFanoutRemovesListenersOfClosedREQs(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), fanoutTestTimeout)
	defer cancel()

	frames := make(chan any, 16)
	removed := make(chan []int, 8)
	f := newLiveFanout(zap.NewNop(), 2)
	f.write = func(_ *khatru.WebSocket, v any) error {
		frames <- v
		return nil
	}
	f.removeListeners = func(_ *khatru.WebSocket, ssids ...int) { removed <- ssids }
	nextRemoval := func() []int {
		t.Helper()
		select {
		case ssids := <-removed:
			return ssids
		case <-ctx.Done():
			t.Fatal("no listener removal")
			return nil
		}
	}
	ws := &khatru.WebSocket{Context: ctx}
	kind1 := nostr.Filter{Kinds: []nostr.Kind{1}}
	setPending := func(id string, p *pendingListener) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.connection(ws).pending[id] = p
	}

	// 1. the REQ context was cancelled before khatru added the listener
	reqCtx, cancelReq := context.WithCancel(ctx)
	setPending("closed", &pendingListener{ctx: reqCtx, filter: kind1})
	cancelReq()
	f.listenerAdded(ws, 7, "closed", kind1)
	require.Equal(t, []int{7}, nextRemoval())

	// 2. the gap buffer overflowed: the drain at the end of the stored query
	//    CLOSES the subscription, then its listener is removed
	full := &pendingListener{ctx: ctx, filter: kind1}
	setPending("gapfull", full)
	f.listenerAdded(ws, 8, "gapfull", kind1) // listener-before-query: live first
	for i := range 3 {
		event := fanoutTestEvent(1, i)
		f.dispatch(event) // buffered, not queued; the third overflows
	}
	f.finishPending(ws, "gapfull", full) // the stored query ends
	closed, ok := nextFrame(t, ctx, frames).(nostr.ClosedEnvelope)
	require.True(t, ok)
	require.Equal(t, "gapfull", closed.SubscriptionID)
	require.Equal(t, []int{8}, nextRemoval())
	require.Equal(t, uint64(1), f.OverflowCloses())

	// 3. a later filter of the already-closed subscription is not attached
	f.mu.Lock()
	f.conns[ws].subs["gapfull"] = &liveSubscription{id: "gapfull", filters: map[int]nostr.Filter{}}
	f.conns[ws].subs["gapfull"].state.Store(subscriptionOverflowed)
	f.mu.Unlock()
	f.listenerAdded(ws, 9, "gapfull", nostr.Filter{Kinds: []nostr.Kind{2}})
	require.Equal(t, []int{9}, nextRemoval())

	// none of them deliver; a live subscription on the same connection does
	f.listenerAdded(ws, 10, "live", kind1)
	sentinel := fanoutTestEvent(1, 100)
	f.dispatch(sentinel)
	requireEventFrame(t, nextFrame(t, ctx, frames), "live", sentinel)
	select {
	case frame := <-frames:
		t.Fatalf("unexpected extra frame %#v", frame)
	default:
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	require.NotContains(t, f.conns[ws].subs, "closed")
	require.Empty(t, f.conns[ws].pending)
	require.True(t, strings.HasPrefix(subscriberOverflowReason, "error:"))
}
