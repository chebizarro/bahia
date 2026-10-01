package nostr

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Relay-stack polish (bahia-irsry.49): terminal CLOSEDs recorded in stored
// outcomes, the CLOSED retry budget across consumers, and max_limit paging.
// Every wait is on a protocol signal under a deadline; none sleeps.

// TestRelayPoolRecordsTerminalBeforeEndOfStoredEvents: a terminal CLOSED is
// recorded before EndOfStoredEvents closes on it, so a consumer woken by
// EndOfStoredEvents, with the CLOSED still queued on Closed, already sees
// that the pool gave up and must not resubscribe.
func TestRelayPoolRecordsTerminalBeforeEndOfStoredEvents(t *testing.T) {
	var reqs atomic.Int32
	relay := newPoolKhatruRelay(t, func(rl *khatru.Relay) {
		rl.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
			reqs.Add(1)
			return true, "blocked: not on the allowlist"
		}
	})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
	require.NoError(t, err)
	defer merged.Close()
	select {
	case <-merged.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("EndOfStoredEvents never closed")
	}
	// Closed has not been read yet.
	outcomes := merged.StoredOutcomes()
	require.Len(t, outcomes, 1)
	require.Equal(t, RelayStoredClosed, outcomes[0].Status)
	require.True(t, outcomes[0].Terminal, "the terminal CLOSED is recorded before EndOfStoredEvents closes")
	require.False(t, merged.HasRealEOSE())
	gaveUp := merged.GaveUp()
	require.ErrorIs(t, gaveUp, ErrSubscriptionGaveUp)
	require.ErrorContains(t, gaveUp, "blocked: not on the allowlist")
	require.ErrorContains(t, merged.StoredEventsIncomplete(nil), "closed, terminal (blocked: not on the allowlist)")

	closed := <-merged.Closed
	require.True(t, closed.Terminal)
	for range merged.Events {
	}
	require.Equal(t, int32(1), reqs.Load())
}

// TestRelayPoolGaveUpAfterEOSEWhenTheBudgetRunsOut: a relay that answered
// with EOSE and then keeps closing the live REQ with a retryable reason is
// given up on once the budget runs out. Its stored outcome stays EOSE but is
// marked Terminal, and GaveUp reports it once Events closes.
func TestRelayPoolGaveUpAfterEOSEWhenTheBudgetRunsOut(t *testing.T) {
	const relayURL = "wss://gave-up-after-eose.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	WithRetryableClosedBudget(1)(pool)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	fastResubscribeBackoff(pool)
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
	require.NoError(t, err)
	defer merged.Close()
	first := reqs.next(t).sub
	close(first.EndOfStoredEvents)
	<-merged.EndOfStoredEvents
	require.NoError(t, merged.GaveUp())

	closeScripted(first, "error: overloaded")
	require.False(t, (<-merged.Closed).Terminal)
	require.NoError(t, merged.GaveUp(), "a retryable CLOSED within the budget is not a give-up")
	closeScripted(reqs.next(t).sub, "error: overloaded")
	require.True(t, (<-merged.Closed).Terminal)
	for range merged.Events {
	}
	require.ErrorIs(t, merged.GaveUp(), ErrSubscriptionGaveUp)
	require.Equal(t, []RelayStoredOutcome{{RelayURL: relayURL, Status: RelayStoredEOSE, Terminal: true}}, merged.StoredOutcomes())
	require.NoError(t, merged.StoredEventsIncomplete(nil), "the stored answer itself was complete")
	reqs.none(t)
}

// maxLimitRelay is an in-process khatru relay that serves at most maxLimit
// events per REQ and says so in its NIP-11 document, like a production relay
// with a query cap. It records every REQ filter.
type maxLimitRelay struct {
	url   string
	relay *khatru.Relay
	mu    sync.Mutex
	reqs  []gonostr.Filter
}

func newMaxLimitRelay(t *testing.T, maxLimit int) *maxLimitRelay {
	t.Helper()
	r := &maxLimitRelay{relay: khatru.NewRelay()}
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	r.relay.UseEventstore(store, maxLimit)
	r.relay.Info.Limitation = &nip11.RelayLimitationDocument{MaxLimit: maxLimit}
	r.relay.OnRequest = func(_ context.Context, filter gonostr.Filter) (bool, string) {
		r.mu.Lock()
		r.reqs = append(r.reqs, filter)
		r.mu.Unlock()
		return false, ""
	}
	server := httptest.NewServer(r.relay)
	t.Cleanup(server.Close)
	r.url = gonostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
	return r
}

func (r *maxLimitRelay) requests() []gonostr.Filter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gonostr.Filter(nil), r.reqs...)
}

func (r *maxLimitRelay) resetRequests() {
	r.mu.Lock()
	r.reqs = nil
	r.mu.Unlock()
}

// storeHistory adds n kind-1 events, one second apart, ending a minute ago.
// It returns them newest first.
func (r *maxLimitRelay) storeHistory(t *testing.T, n int) []gonostr.Event {
	t.Helper()
	secret := gonostr.Generate()
	newest := gonostr.Now() - 60
	events := make([]gonostr.Event, n)
	for i := range events {
		ev := gonostr.Event{Kind: 1, CreatedAt: newest - gonostr.Timestamp(i), Content: fmt.Sprintf("history %d", i)}
		require.NoError(t, ev.Sign(secret))
		_, err := r.relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
		events[i] = ev
	}
	return events
}

func idSet(events []*gonostr.Event) map[gonostr.ID]struct{} {
	ids := make(map[gonostr.ID]struct{}, len(events))
	for _, ev := range events {
		ids[ev.ID] = struct{}{}
	}
	return ids
}

// TestRelayPoolPagesPastNIP11MaxLimit: a caller that asks for more events
// than the relay's max_limit, and does not page, still gets every event it
// asked for before EndOfStoredEvents. The pool sends REQs capped at
// max_limit and pages with `until`, then follows the relay live.
func TestRelayPoolPagesPastNIP11MaxLimit(t *testing.T) {
	relay := newMaxLimitRelay(t, 5)
	history := relay.storeHistory(t, 23)
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool.Connect(ctx)

	t.Run("limit below the history", func(t *testing.T) {
		relay.resetRequests()
		merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}, Limit: 12}})
		require.NoError(t, err)
		defer merged.Close()
		got := collectStored(t, ctx, merged)
		require.NoError(t, merged.StoredEventsIncomplete(nil))
		require.Len(t, got, 12, "exactly the caller's limit")
		want := make([]*gonostr.Event, 12)
		for i := range want {
			want[i] = &history[i]
		}
		require.Equal(t, idSet(want), idSet(got), "the newest 12 events")
		for _, filter := range relay.requests() {
			require.LessOrEqual(t, filter.Limit, 5, "no REQ asks for more than max_limit")
		}
	})

	t.Run("limit beyond the history", func(t *testing.T) {
		relay.resetRequests()
		merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}, Limit: 1000}})
		require.NoError(t, err)
		defer merged.Close()
		got := collectStored(t, ctx, merged)
		require.NoError(t, merged.StoredEventsIncomplete(nil))
		require.Len(t, got, len(history), "every stored event, not the newest max_limit")
		// The previous subscription's live REQ may be recorded here too, so
		// look for this answer's pages by their until.
		paged := false
		for _, filter := range relay.requests() {
			paged = paged || (filter.Until == history[4].CreatedAt && filter.Limit == 5)
		}
		require.True(t, paged, "the page after the first full one ends at its oldest event")

		// After paging, the relay is followed live.
		secret := gonostr.Generate()
		live := gonostr.Event{Kind: 1, CreatedAt: gonostr.Now(), Content: "live after paging"}
		require.NoError(t, live.Sign(secret))
		results, err := pool.PublishWithResults(ctx, live)
		require.NoError(t, err)
		require.True(t, results[0].Accepted, "%+v", results)
		for {
			select {
			case ev := <-merged.Events:
				if ev.ID == live.ID {
					return
				}
			case <-ctx.Done():
				t.Fatal("event published after paging never arrived")
			}
		}
	})
}

// TestRelayPoolReportsAnAnswerItCannotPageAsTruncated: NIP-01 cannot page
// within one second, so when more events share one created_at than max_limit
// lets a page hold, the answer is marked Truncated and StoredEventsIncomplete
// reports it instead of passing a partial answer off as complete.
func TestRelayPoolReportsAnAnswerItCannotPageAsTruncated(t *testing.T) {
	relay := newMaxLimitRelay(t, 5)
	secret := gonostr.Generate()
	createdAt := gonostr.Now() - 60
	for i := range 12 {
		ev := gonostr.Event{Kind: 1, CreatedAt: createdAt, Content: fmt.Sprintf("same second %d", i)}
		require.NoError(t, ev.Sign(secret))
		_, err := relay.relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
	}
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool.Connect(ctx)

	merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}, Limit: 100}})
	require.NoError(t, err)
	defer merged.Close()
	got := collectStored(t, ctx, merged)
	require.Less(t, len(got), 12)
	require.True(t, merged.AllRelaysReachedEOSE())
	outcomes := merged.StoredOutcomes()
	require.Len(t, outcomes, 1)
	require.True(t, outcomes[0].Truncated)
	err = merged.StoredEventsIncomplete(nil)
	require.ErrorIs(t, err, ErrStoredEventsIncomplete)
	require.ErrorContains(t, err, "eose, truncated")
}

// TestInboundSyncRetryBudgetGivesUpARefusedFilterAndKeepsTheRest: the
// inbound sync applies the pool's CLOSED retry budget per (relay, filter)
// instead of resyncing forever. A filter the relay keeps closing with a
// retryable reason is given up on after the budget, the relay's other filter
// keeps syncing from its cursor, and the give-up is counted.
func TestInboundSyncRetryBudgetGivesUpARefusedFilterAndKeepsTheRest(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	var refused atomic.Int32
	relay.relay.OnRequest = func(ctx context.Context, filter gonostr.Filter) (bool, string) {
		if !khatru.IsNegentropySession(ctx) && len(filter.Kinds) == 1 && filter.Kinds[0] == syncTestRegularKind {
			refused.Add(1)
			return true, "error: overloaded"
		}
		return false, ""
	}
	state := syncTestEvent(t, gonostr.Generate(), syncTestStateKind, gonostr.Now()-60, gonostr.Tags{{"d", "kept"}}, "state")
	relay.add(t, state)
	pool := newSyncTestPool(relay)
	WithRetryableClosedBudget(1)(pool)
	defer pool.Close()

	run := startSyncRun(t, pool, openTestLocalStore(t, ""), nil, syncTestConfig())
	run.waitCaughtUp(t, ctx, relay.url, 1)
	waitEvent(t, ctx, run.handled, state.ID, "the state event from the filter the relay serves")
	require.Equal(t, int32(2), refused.Load(), "the first REQ and one reissue (budget 1), then no more")
	require.Equal(t, int64(1), pool.HealthSnapshot().Relays[0].ClosedRetryExhausted)
	require.True(t, run.sub.IsCaughtUp())
}

// TestInboundSyncStopsARelayThatRefusesEveryFilter: policy refusals are
// terminal for the inbound sync too. Once the relay has refused every filter
// its worker stops (opRelayGaveUp) rather than resyncing with backoff
// forever, and ProcessSync, which has no other relay, returns a
// SubscriptionGaveUpError instead of idling.
func TestInboundSyncStopsARelayThatRefusesEveryFilter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	var reqs atomic.Int32
	relay.relay.OnRequest = func(ctx context.Context, _ gonostr.Filter) (bool, string) {
		if !khatru.IsNegentropySession(ctx) {
			reqs.Add(1)
		}
		return true, "restricted: members only"
	}

	t.Run("subscriber", func(t *testing.T) {
		reqs.Store(0)
		pool := newSyncTestPool(relay)
		defer pool.Close()
		run := startSyncRun(t, pool, openTestLocalStore(t, ""), nil, syncTestConfig())
		run.waitCount(t, ctx, "relay given up", 1, func(item inboundItem) bool {
			return item.op == opRelayGaveUp && item.relay == relay.url
		})
		require.Equal(t, int32(2), reqs.Load(), "each filter refused once, never retried")
		require.False(t, run.sub.IsCaughtUp())
	})

	t.Run("process sync", func(t *testing.T) {
		reqs.Store(0)
		pool := newSyncTestPool(relay)
		defer pool.Close()
		syncer := &ProcessSync{
			Pool: pool, Store: openTestLocalStore(t, ""), Config: syncTestConfig(),
			RelayBackoff: fastTestBackoff,
			Apply:        func(context.Context, *gonostr.Event) {},
		}
		err := syncer.Run(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{syncTestRegularKind}}})
		require.ErrorIs(t, err, ErrSubscriptionGaveUp)
		require.ErrorContains(t, err, "restricted: members only")
		require.NoError(t, ctx.Err())
		require.Equal(t, int32(1), reqs.Load())
	})
}

// TestFIPSSubscriberStopsWhenThePoolGivesUp: once the pool has given up on
// every relay, the FIPS subscriber stops with the give-up instead of
// resubscribing, which would only restart the refusals with a fresh budget.
func TestFIPSSubscriberStopsWhenThePoolGivesUp(t *testing.T) {
	var reqs atomic.Int32
	relay := newPoolKhatruRelay(t, func(rl *khatru.Relay) {
		rl.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
			reqs.Add(1)
			return true, "error: overloaded"
		}
	})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithRetryableClosedBudget(1))
	fastResubscribeBackoff(pool)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	err := NewFIPSSubscriber(pool, newFIPSTestWorkerRepo(), zap.NewNop()).Run(ctx)
	require.ErrorIs(t, err, ErrSubscriptionGaveUp)
	require.ErrorContains(t, err, "error: overloaded")
	require.NoError(t, ctx.Err(), "Run returned on the give-up, not the deadline")
	require.Equal(t, int32(4), reqs.Load(), "two filters, each sent once and reissued once")
	require.Equal(t, int64(2), pool.HealthSnapshot().Relays[0].ClosedRetryExhausted)
}

// TestStoreBackedSubscriptionReportsTheGiveUp: the store-backed
// subscription's Events closes when its sync gives up, and GaveUp says why.
func TestStoreBackedSubscriptionReportsTheGiveUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	relay := startSyncTestRelay(t, syncTestRelayOptions{})
	relay.relay.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
		return true, "blocked: no"
	}
	pool := newSyncTestPool(relay)
	defer pool.Close()
	sub := &StoreBackedSubscriber{Pool: pool, Store: openTestLocalStore(t, ""), Logger: zap.NewNop(), Config: syncTestConfig()}
	merged, err := sub.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{syncTestRegularKind}}})
	require.NoError(t, err)
	defer merged.Close()
	for {
		select {
		case _, ok := <-merged.Events:
			if !ok {
				require.ErrorIs(t, merged.GaveUp(), ErrSubscriptionGaveUp)
				return
			}
		case <-ctx.Done():
			t.Fatal("the store-backed subscription never ended")
		}
	}
}
