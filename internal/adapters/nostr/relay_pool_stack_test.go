package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/eventstore/wrappers"
	"fiatjaf.com/nostr/khatru"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// These tests cover the single relay stack: NIP-42 through
// the pool's AuthHandler, CLOSED classification, per-relay re-REQ and NIP-11
// limit enforcement.

// collectStored reads merged's events until every relay settled its initial
// REQs.
func collectStored(t *testing.T, ctx context.Context, merged *MergedSubscription) []*gonostr.Event {
	t.Helper()
	var events []*gonostr.Event
	for {
		select {
		case <-ctx.Done():
			t.Fatal("stored events never completed")
		case <-merged.EndOfStoredEvents:
			for {
				select {
				case ev := <-merged.Events:
					events = append(events, ev)
				default:
					return events
				}
			}
		case ev := <-merged.Events:
			events = append(events, ev)
		}
	}
}

func fastResubscribeBackoff(p *RelayPool) {
	p.newResubscribeBackoff = func() *Backoff {
		return &Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond, Multiplier: 2}
	}
}

type khatruTestServer struct {
	url    string
	server *httptest.Server
	relay  *khatru.Relay
}

func newPoolKhatruRelay(t *testing.T, configure func(*khatru.Relay)) *khatruTestServer {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	if configure != nil {
		configure(relay)
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return &khatruTestServer{url: "ws" + strings.TrimPrefix(server.URL, "http"), server: server, relay: relay}
}

// requireNIP42 makes relay reject unauthenticated REQs and EVENTs with
// "auth-required:" (khatru re-sends its challenge with each rejection).
func requireNIP42(challengeOnConnect bool) func(*khatru.Relay) {
	return func(relay *khatru.Relay) {
		if challengeOnConnect {
			relay.OnConnect = khatru.RequestAuth
		}
		relay.OnRequest = func(ctx context.Context, _ gonostr.Filter) (bool, string) {
			if _, ok := khatru.GetAuthed(ctx); !ok {
				return true, "auth-required: authenticated clients only"
			}
			return false, ""
		}
		relay.OnEvent = func(ctx context.Context, _ gonostr.Event) (bool, string) {
			if _, ok := khatru.GetAuthed(ctx); !ok {
				return true, "auth-required: authenticated writers only"
			}
			return false, ""
		}
	}
}

func signedStackEvent(t *testing.T, secret gonostr.SecretKey, kind gonostr.Kind, content string) gonostr.Event {
	t.Helper()
	event := gonostr.Event{Kind: kind, CreatedAt: gonostr.Now(), Content: content}
	require.NoError(t, event.Sign(secret))
	return event
}

// TestRelayPoolNIP42ThroughAuthHandlerIsRaceFree drives concurrent REQs,
// publishes and AuthenticateRelays against an in-process khatru relay that
// requires NIP-42. The pool answers challenges through the library
// AuthHandler and reissues only the REQ that got "auth-required:". Run with
// -race (the pristine library races here; see BAHIA_PATCHES.md).
func TestRelayPoolNIP42ThroughAuthHandlerIsRaceFree(t *testing.T) {
	for _, tc := range []struct {
		name               string
		challengeOnConnect bool
		authenticateFirst  bool
	}{
		{name: "challenge on connect, AuthenticateRelays", challengeOnConnect: true, authenticateFirst: true},
		{name: "challenge on connect", challengeOnConnect: true},
		{name: "challenge only with rejections", challengeOnConnect: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := gonostr.Generate()
			relay := newPoolKhatruRelay(t, requireNIP42(tc.challengeOnConnect))
			var authResults atomic.Int32
			pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithAuthSignFunc(func(_ context.Context, event *gonostr.Event) error {
				authResults.Add(1)
				return event.Sign(secret)
			}))
			fastResubscribeBackoff(pool)
			defer pool.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool.Connect(ctx)
			if tc.authenticateFirst {
				require.NoError(t, pool.AuthenticateRelays(ctx, nil))
			}

			var wg sync.WaitGroup
			published := make([]gonostr.Event, 8)
			for i := range published {
				published[i] = signedStackEvent(t, secret, 1, fmt.Sprintf("nip-42 %d", i))
				wg.Add(1)
				go func() {
					defer wg.Done()
					results, err := pool.PublishWithResults(ctx, published[i])
					if err != nil || len(results) != 1 || !results[0].Accepted {
						t.Errorf("publish %d: results=%+v err=%v", i, results, err)
					}
				}()
			}
			// khatru's test slicestore is not safe for a save concurrent
			// with a query, so the REQs start once the writes are in.
			wg.Wait()
			subs := make([]*MergedSubscription, 4)
			for i := range subs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					merged, err := pool.SubscribeWithOptions(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{gonostr.Kind(100 + i)}}, {Kinds: []gonostr.Kind{gonostr.Kind(200 + i)}}}, SubscribeOptions{})
					if err != nil {
						t.Errorf("subscribe %d: %v", i, err)
						return
					}
					subs[i] = merged
				}()
			}
			wg.Wait()
			for i, merged := range subs {
				require.NotNil(t, merged)
				collectStored(t, ctx, merged)
				require.NoError(t, merged.StoredEventsIncomplete(nil), "subscription %d", i)
				require.True(t, merged.AllRelaysReachedEOSE())
				merged.Close()
			}
			require.Equal(t, int32(1), authResults.Load(), "one AUTH per connection, however many challenges")

			ids := make([]gonostr.ID, len(published))
			for i := range published {
				ids[i] = published[i].ID
			}
			merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{IDs: ids}})
			require.NoError(t, err)
			defer merged.Close()
			events := collectStored(t, ctx, merged)
			require.NoError(t, merged.StoredEventsIncomplete(nil))
			require.Len(t, events, len(published))
		})
	}
}

func TestClassifyClosedReason(t *testing.T) {
	for reason, want := range map[string]ClosedAction{
		"auth-required: sign in":        ClosedAuthenticate,
		"AUTH-REQUIRED: sign in":        ClosedAuthenticate,
		"blocked: not on the allowlist": ClosedTerminal,
		"restricted: members only":      ClosedTerminal,
		"invalid: bad filter":           ClosedTerminal,
		"unsupported: search":           ClosedTerminal,
		"pow: difficulty 20":            ClosedTerminal,
		"mute: no":                      ClosedTerminal,
		"error: subscriber overflow":    ClosedRetry,
		"rate-limited: slow down":       ClosedRetry,
		"shutting down":                 ClosedRetry,
		"":                              ClosedRetry,
	} {
		require.Equal(t, want, ClassifyClosedReason(reason), reason)
	}
}

// scriptedSubscribes records every REQ the pool sends to fake relays and
// hands the test each subscription to drive.
type scriptedSubscribes struct {
	mu      sync.Mutex
	filters map[string][]gonostr.Filter
	subs    chan scriptedREQ
}

type scriptedREQ struct {
	relay  string
	filter gonostr.Filter
	sub    *gonostr.Subscription
}

func newScriptedSubscribes(t *testing.T) *scriptedSubscribes {
	t.Helper()
	s := &scriptedSubscribes{filters: map[string][]gonostr.Filter{}, subs: make(chan scriptedREQ, 64)}
	setSubscribeOnRelayForTest(t, func(relay *gonostr.Relay, _ context.Context, filter gonostr.Filter) (*gonostr.Subscription, error) {
		sub := newTestSubscription()
		s.mu.Lock()
		s.filters[relay.URL] = append(s.filters[relay.URL], filter)
		s.mu.Unlock()
		s.subs <- scriptedREQ{relay: relay.URL, filter: filter, sub: sub}
		return sub, nil
	})
	return s
}

func (s *scriptedSubscribes) count(relay string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.filters[gonostr.NormalizeURL(relay)])
}

func (s *scriptedSubscribes) next(t *testing.T) scriptedREQ {
	t.Helper()
	select {
	case req := <-s.subs:
		return req
	case <-time.After(10 * time.Second):
		t.Fatal("no REQ was sent")
		return scriptedREQ{}
	}
}

func (s *scriptedSubscribes) none(t *testing.T) {
	t.Helper()
	select {
	case req := <-s.subs:
		t.Fatalf("unexpected REQ to %s: %+v", req.relay, req.filter)
	default:
	}
}

func closeScripted(sub *gonostr.Subscription, reason string) {
	sub.ClosedReason <- reason
	close(sub.Events)
}

func TestRelayPoolClosedClassificationStopsRefusalsAndRetriesTransients(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		terminal bool
	}{
		{reason: "blocked: not allowed", terminal: true},
		{reason: "restricted: members only", terminal: true},
		{reason: "invalid: filter too broad", terminal: true},
		{reason: "error: subscriber queue overflow", terminal: false},
		{reason: "rate-limited: slow down", terminal: false},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			const relayURL = "wss://classify.example"
			pool := newRelayPoolWithManagedRelays(relayURL)
			markRelayConnectedForSubscribeTest(pool, relayURL)
			fastResubscribeBackoff(pool)
			reqs := newScriptedSubscribes(t)

			merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
			require.NoError(t, err)
			defer merged.Close()
			first := reqs.next(t)
			closeScripted(first.sub, tc.reason)

			closed := <-merged.Closed
			require.Equal(t, tc.reason, closed.Reason)
			require.Equal(t, tc.terminal, closed.Terminal)
			<-merged.EndOfStoredEvents
			require.Equal(t, []RelayStoredOutcome{{RelayURL: relayURL, Status: RelayStoredClosed, Reason: tc.reason, Terminal: tc.terminal}}, merged.StoredOutcomes())

			if tc.terminal {
				// Every REQ refused for good: the merged stream ends and the
				// relay is never asked again.
				for range merged.Events {
				}
				reqs.none(t)
				require.Equal(t, 1, reqs.count(relayURL))
				return
			}
			reissued := reqs.next(t)
			require.Equal(t, first.filter, reissued.filter)
			event := signedStackEvent(t, gonostr.Generate(), 1, "after reissue")
			reissued.sub.Events <- event
			got := <-merged.Events
			require.Equal(t, event.ID, got.ID)
		})
	}
}

// TestRelayPoolNegentropyAuthenticates: a relay that refuses NEG-OPEN with
// "auth-required:" is reconciled through the pool's signer. The session
// answers the challenge, re-opens once, and downloads the protected event
// (see third_party/nostr/BAHIA_PATCHES.md).
func TestRelayPoolNegentropyAuthenticates(t *testing.T) {
	secret := gonostr.Generate()
	var negOpens atomic.Int32
	relay := newPoolKhatruRelay(t, func(rl *khatru.Relay) {
		rl.Negentropy = true
		rl.OnRequest = func(ctx context.Context, _ gonostr.Filter) (bool, string) {
			if !khatru.IsNegentropySession(ctx) {
				return false, ""
			}
			negOpens.Add(1)
			if authed, ok := khatru.GetAuthed(ctx); !ok || authed != secret.Public() {
				return true, "auth-required: protected sync"
			}
			return false, ""
		}
	})
	ev := signedStackEvent(t, secret, 1, "protected")
	_, err := relay.relay.AddEvent(t.Context(), ev)
	require.NoError(t, err)
	local := wrappers.StorePublisher{Store: &slicestore.SliceStore{}, MaxLimit: 100}
	require.NoError(t, local.Init())
	var authOK atomic.Int32
	pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithAuthSignFunc(func(_ context.Context, event *gonostr.Event) error {
		authOK.Add(1)
		return event.Sign(secret)
	}))
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	require.NoError(t, pool.negentropySyncRelay(ctx, relay.url, gonostr.Filter{Kinds: []gonostr.Kind{1}}, local, false, 5*time.Second))
	require.Equal(t, int32(2), negOpens.Load(), "the refused NEG-OPEN and one re-open after AUTH")
	require.Equal(t, int32(1), authOK.Load(), "one AUTH for the session's connection")
	found := false
	for range local.QueryEvents(gonostr.Filter{IDs: []gonostr.ID{ev.ID}}) {
		found = true
	}
	require.True(t, found, "the protected event was not reconciled")
}

// TestRelayPoolRetryableClosedBudgetGivesUp: a relay that answers every REQ
// with a retryable CLOSED gets the REQ reissued budget times, then the pool
// gives up on it: the CLOSED is surfaced as Terminal, the relay is not asked
// again, and ClosedRetryExhausted counts the give-up.
func TestRelayPoolRetryableClosedBudgetGivesUp(t *testing.T) {
	var reqs atomic.Int32
	relay := newPoolKhatruRelay(t, func(rl *khatru.Relay) {
		rl.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
			reqs.Add(1)
			return true, "error: overloaded"
		}
	})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop(), WithRetryableClosedBudget(2))
	fastResubscribeBackoff(pool)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
	require.NoError(t, err)
	defer merged.Close()
	var closed []RelayClosed
	for len(closed) == 0 || !closed[len(closed)-1].Terminal {
		select {
		case info := <-merged.Closed:
			closed = append(closed, info)
		case <-ctx.Done():
			t.Fatalf("no terminal CLOSED; got %+v", closed)
		}
	}
	// The relay's only worker stopped, so the merged stream ends.
	for range merged.Events {
	}
	last := closed[len(closed)-1]
	require.Equal(t, gonostr.NormalizeURL(relay.url), last.RelayURL)
	require.Equal(t, "error: overloaded", last.Reason)
	require.Equal(t, int32(3), reqs.Load(), "the first REQ and two reissues")
	status := pool.HealthSnapshot().Relays[0]
	require.Equal(t, int64(1), status.ClosedRetryExhausted)
	require.Equal(t, int64(2), status.ReREQAttempts)
}

// TestRelayPoolRetryableClosedBudgetResetsOnEOSE: the budget counts
// consecutive retryable CLOSEDs. A REQ that reached EOSE before its CLOSED
// proves the relay serves the filter, so the count starts over.
func TestRelayPoolRetryableClosedBudgetResetsOnEOSE(t *testing.T) {
	const relayURL = "wss://retry-budget.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	WithRetryableClosedBudget(1)(pool)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	fastResubscribeBackoff(pool)
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
	require.NoError(t, err)
	defer merged.Close()

	// 1st CLOSED: within the budget of one reissue.
	closeScripted(reqs.next(t).sub, "error: temporary overload")
	require.False(t, (<-merged.Closed).Terminal)
	// The reissue reaches EOSE, then is CLOSED: the count restarts at one.
	second := reqs.next(t).sub
	second.EndOfStoredEvents <- gonostr.EndOfStoredEvent{}
	closeScripted(second, "rate-limited: slow down")
	require.False(t, (<-merged.Closed).Terminal)
	// A second CLOSED in a row exceeds the budget.
	closeScripted(reqs.next(t).sub, "error: temporary overload")
	require.True(t, (<-merged.Closed).Terminal)
	for range merged.Events {
	}
	reqs.none(t)
	require.Equal(t, 3, reqs.count(relayURL))
	require.Equal(t, int64(1), pool.HealthSnapshot().Relays[0].ClosedRetryExhausted)
}

// TestRelayPoolReissuesOnlyTheDroppedRelay: one relay's REQ dropping must be
// reissued on that relay alone, without waiting for every relay to fail
// , while the other relay keeps streaming on its original REQ.
func TestRelayPoolReissuesOnlyTheDroppedRelay(t *testing.T) {
	const stable, flaky = "wss://stable.example", "wss://flaky.example"
	pool := newRelayPoolWithManagedRelays(stable, flaky)
	markRelayConnectedForSubscribeTest(pool, stable)
	markRelayConnectedForSubscribeTest(pool, flaky)
	fastResubscribeBackoff(pool)
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}})
	require.NoError(t, err)
	defer merged.Close()
	byRelay := map[string]*gonostr.Subscription{}
	for range 2 {
		req := reqs.next(t)
		byRelay[req.relay] = req.sub
	}
	close(byRelay[stable].EndOfStoredEvents)
	close(byRelay[flaky].EndOfStoredEvents)
	<-merged.EndOfStoredEvents
	require.True(t, merged.AllRelaysReachedEOSE())

	close(byRelay[flaky].Events) // connection dropped, no CLOSED
	reissued := reqs.next(t)
	require.Equal(t, gonostr.NormalizeURL(flaky), reissued.relay)
	reqs.none(t)
	require.Equal(t, 1, reqs.count(stable))

	secret := gonostr.Generate()
	fromStable := signedStackEvent(t, secret, 1, "stable")
	byRelay[stable].Events <- fromStable
	require.Equal(t, fromStable.ID, (<-merged.Events).ID)
	fromFlaky := signedStackEvent(t, secret, 1, "flaky after reissue")
	reissued.sub.Events <- fromFlaky
	require.Equal(t, fromFlaky.ID, (<-merged.Events).ID)
	require.Positive(t, pool.HealthSnapshot().Relays[1].ReREQAttempts)
}

// TestRelayPoolResumeCursorNarrowsReissuedREQ: with ResumeOverlap the REQ
// reissued after a drop starts at the newest event the relay delivered, less
// the overlap, instead of replaying the backfill.
func TestRelayPoolResumeCursorNarrowsReissuedREQ(t *testing.T) {
	const relayURL = "wss://resume.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	fastResubscribeBackoff(pool)
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeWithOptions(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}, Limit: 50}}, SubscribeOptions{ResumeOverlap: time.Minute})
	require.NoError(t, err)
	defer merged.Close()
	first := reqs.next(t)
	require.Zero(t, first.filter.Since)

	secret := gonostr.Generate()
	stored := gonostr.Event{Kind: 1, CreatedAt: gonostr.Now() - 3600, Content: "stored"}
	require.NoError(t, stored.Sign(secret))
	first.sub.Events <- stored
	<-merged.Events
	close(first.sub.EndOfStoredEvents)
	<-merged.EndOfStoredEvents
	close(first.sub.Events)

	reissued := reqs.next(t)
	require.Equal(t, stored.CreatedAt-60, reissued.filter.Since)
	require.Equal(t, 50, reissued.filter.Limit)
}

// A relay that cannot be subscribed when the subscription opens stays
// pending under AwaitUnavailableRelays and is subscribed once it recovers.
func TestRelayPoolAwaitUnavailableRelaysWaitsForRecovery(t *testing.T) {
	const up, down = "wss://up.example", "wss://down.example"
	pool := newRelayPoolWithManagedRelays(up, down)
	markRelayConnectedForSubscribeTest(pool, up)
	fastResubscribeBackoff(pool)
	var dials atomic.Int32
	setConnectRelayForTest(t, pool, func(ctx context.Context, url string, opts gonostr.RelayOptions) (*gonostr.Relay, error) {
		if dials.Add(1) < 3 {
			return nil, fmt.Errorf("connection refused")
		}
		return gonostr.NewRelay(context.Background(), url, opts), nil
	})
	pool.newReconnectBackoff = func() *Backoff { return &Backoff{Initial: time.Millisecond, Max: 2 * time.Millisecond} }
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeWithOptions(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}}, SubscribeOptions{AwaitUnavailableRelays: true})
	require.NoError(t, err)
	defer merged.Close()
	upReq := reqs.next(t)
	close(upReq.sub.EndOfStoredEvents)
	downReq := reqs.next(t)
	require.Equal(t, gonostr.NormalizeURL(down), downReq.relay)
	select {
	case <-merged.EndOfStoredEvents:
		t.Fatal("EndOfStoredEvents closed while an awaited relay had not answered")
	default:
	}
	close(downReq.sub.EndOfStoredEvents)
	<-merged.EndOfStoredEvents
	require.NoError(t, merged.StoredEventsIncomplete(nil))
}

// nip11LimitRelay is a minimal relay that serves a NIP-11 document with the
// given limitations and answers a REQ with EOSE, or CLOSED "invalid:" when it
// breaks max_filters. It records the filters of every REQ.
type nip11LimitRelay struct {
	url    string
	mu     sync.Mutex
	reqs   [][]gonostr.Filter
	closed []string
}

func newNIP11LimitRelay(t *testing.T, limitation map[string]int) *nip11LimitRelay {
	t.Helper()
	relay := &nip11LimitRelay{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/nostr+json" {
			w.Header().Set("Content-Type", "application/nostr+json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "limits", "limitation": limitation})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, msg, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(msg, &frame) != nil || len(frame) < 2 {
				continue
			}
			var verb, subID string
			_ = json.Unmarshal(frame[0], &verb)
			_ = json.Unmarshal(frame[1], &subID)
			if verb != "REQ" {
				continue
			}
			filters := make([]gonostr.Filter, 0, len(frame)-2)
			for _, raw := range frame[2:] {
				var filter gonostr.Filter
				_ = json.Unmarshal(raw, &filter)
				filters = append(filters, filter)
			}
			relay.mu.Lock()
			relay.reqs = append(relay.reqs, filters)
			relay.mu.Unlock()
			answer := fmt.Sprintf(`["EOSE",%q]`, subID)
			if max := limitation["max_filters"]; max > 0 && len(filters) > max {
				answer = fmt.Sprintf(`["CLOSED",%q,"invalid: too many filters"]`, subID)
			}
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(answer)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	relay.url = "ws" + strings.TrimPrefix(server.URL, "http")
	return relay
}

func (r *nip11LimitRelay) requests() [][]gonostr.Filter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]gonostr.Filter(nil), r.reqs...)
}

func TestRelayPoolEnforcesNIP11MaxLimitAndMaxFilters(t *testing.T) {
	relay := newNIP11LimitRelay(t, map[string]int{"max_limit": 20, "max_filters": 1})
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool.Connect(ctx)

	merged, err := pool.SubscribeAllWithEOSE(ctx, []gonostr.Filter{
		{Kinds: []gonostr.Kind{1}, Limit: 500},
		{Kinds: []gonostr.Kind{2}, Limit: 5},
		{Kinds: []gonostr.Kind{3}},
	})
	require.NoError(t, err)
	defer merged.Close()
	collectStored(t, ctx, merged)
	require.NoError(t, merged.StoredEventsIncomplete(nil), "a max_filters=1 relay must get one filter per REQ")

	reqs := relay.requests()
	require.Len(t, reqs, 3)
	limits := map[gonostr.Kind]int{}
	for _, filters := range reqs {
		require.Len(t, filters, 1)
		limits[filters[0].Kinds[0]] = filters[0].Limit
	}
	require.Equal(t, map[gonostr.Kind]int{1: 20, 2: 5, 3: 0}, limits, "limit is capped at max_limit and otherwise untouched")

	info := pool.GetRelayInfo(relay.url)
	require.NotNil(t, info, "the connect-time NIP-11 document is cached")
	require.Equal(t, 20, pool.GetMaxLimit(relay.url))
}

func TestFetchRelayLimitsReadsMaxFilters(t *testing.T) {
	relay := newNIP11LimitRelay(t, map[string]int{"max_limit": 7, "max_subscriptions": 3, "max_filters": 2})
	limits, info, err := fetchRelayLimits(t.Context(), relay.url)
	require.NoError(t, err)
	require.Equal(t, relayLimits{MaxLimit: 7, MaxSubscriptions: 3, MaxFilters: 2}, limits)
	require.Equal(t, "limits", info.Name)
}

// TestRelayPoolHoldsREQsBeyondMaxSubscriptions: REQs beyond a relay's NIP-11
// max_subscriptions wait for a free slot (reported as the pending reason)
// instead of being sent and refused.
func TestRelayPoolHoldsREQsBeyondMaxSubscriptions(t *testing.T) {
	const relayURL = "wss://slots.example"
	pool := newRelayPoolWithManagedRelays(relayURL)
	markRelayConnectedForSubscribeTest(pool, relayURL)
	pool.relays[relayURL].limits = relayLimits{MaxSubscriptions: 1}
	fastResubscribeBackoff(pool)
	reqs := newScriptedSubscribes(t)

	merged, err := pool.SubscribeAllWithEOSE(t.Context(), []gonostr.Filter{{Kinds: []gonostr.Kind{1}}, {Kinds: []gonostr.Kind{2}}})
	require.NoError(t, err)
	defer merged.Close()
	first := reqs.next(t)
	reqs.none(t)
	outcomes := merged.StoredOutcomes()
	require.Len(t, outcomes, 1)
	require.Equal(t, RelayStoredPending, outcomes[0].Status)
	require.Contains(t, outcomes[0].Reason, "max_subscriptions 1")

	close(first.sub.EndOfStoredEvents)
	closeScripted(first.sub, "blocked: done with this one")
	second := reqs.next(t)
	require.NotEqual(t, first.filter.Kinds, second.filter.Kinds)
	close(second.sub.EndOfStoredEvents)
	<-merged.EndOfStoredEvents
}

// TestRelayPoolReconnectsAndReissuesAfterWebsocketDrop is the end-to-end
// shape of: the relay drops the websocket, the pool redials that relay
// and reissues the REQ, and events published afterwards still arrive.
func TestRelayPoolReconnectsAndReissuesAfterWebsocketDrop(t *testing.T) {
	relay := newPoolKhatruRelay(t, nil)
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	fastResubscribeBackoff(pool)
	pool.newReconnectBackoff = func() *Backoff { return &Backoff{Initial: time.Millisecond, Max: 5 * time.Millisecond} }
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool.Connect(ctx)

	merged, err := pool.SubscribeWithOptions(ctx, []gonostr.Filter{{Kinds: []gonostr.Kind{1}}}, SubscribeOptions{ResumeOverlap: time.Minute})
	require.NoError(t, err)
	defer merged.Close()
	<-merged.EndOfStoredEvents

	relay.server.CloseClientConnections()
	secret := gonostr.Generate()
	event := signedStackEvent(t, secret, 1, "published after the drop")
	// Publishing redials too; keep trying until the relay has the event.
	for {
		results, _ := pool.PublishWithResults(ctx, event)
		if len(results) == 1 && (results[0].Accepted || results[0].IsDuplicate()) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("could not publish after the drop")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case got := <-merged.Events:
		require.Equal(t, event.ID, got.ID)
	case <-ctx.Done():
		t.Fatal("event published after the drop never reached the reissued REQ")
	}
}
