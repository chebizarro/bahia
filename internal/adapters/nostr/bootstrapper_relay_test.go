package nostr

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// These tests drive the bootstrapper through a real RelayPool merged
// subscription. Each fake relay hands the test its raw subscription channels,
// so completion is driven purely by EVENT/EOSE/CLOSED frames.

const (
	bootstrapTestOperatorKey = "2222222222222222222222222222222222222222222222222222222222222222"
	bootstrapTestStrangerKey = "3333333333333333333333333333333333333333333333333333333333333333"
	// strfry's default maxFilterLimit; the relay sidecar caps at 2000.
	bootstrapTestRelayMaxLimit = 500
)

type bootstrapFakeRelaySub struct {
	filter gonostr.Filter
	sub    *gonostr.Subscription
}

type bootstrapFakeRelay struct {
	url string
	// store, when non-nil, answers every REQ synchronously like a relay with
	// a bootstrapTestRelayMaxLimit query cap, then sends EOSE.
	store []gonostr.Event
	// ignoreAuthors makes the relay return events regardless of the
	// filter's authors, like a misbehaving or compatibility relay.
	ignoreAuthors bool
	// closeWith, when set, makes a store relay answer every REQ with this
	// CLOSED reason instead of events and EOSE.
	closeWith string
	// subs receives manually driven subscriptions when store is nil.
	subs chan bootstrapFakeRelaySub

	mu      sync.Mutex
	filters []gonostr.Filter
}

func newManualBootstrapRelay(url string) *bootstrapFakeRelay {
	return &bootstrapFakeRelay{url: url, subs: make(chan bootstrapFakeRelaySub, 16)}
}

func (r *bootstrapFakeRelay) recordedFilters() []gonostr.Filter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gonostr.Filter(nil), r.filters...)
}

func (r *bootstrapFakeRelay) query(filter gonostr.Filter) []gonostr.Event {
	kinds := make(map[gonostr.Kind]struct{}, len(filter.Kinds))
	for _, kind := range filter.Kinds {
		kinds[kind] = struct{}{}
	}
	authors := make(map[gonostr.PubKey]struct{}, len(filter.Authors))
	for _, author := range filter.Authors {
		authors[author] = struct{}{}
	}
	matches := make([]gonostr.Event, 0, len(r.store))
	for _, event := range r.store {
		if _, ok := kinds[event.Kind]; len(kinds) > 0 && !ok {
			continue
		}
		if _, ok := authors[event.PubKey]; len(authors) > 0 && !ok && !r.ignoreAuthors {
			continue
		}
		if filter.Until != 0 && event.CreatedAt > filter.Until {
			continue
		}
		if filter.Since != 0 && event.CreatedAt < filter.Since {
			continue
		}
		matches = append(matches, event)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].CreatedAt > matches[j].CreatedAt })
	limit := bootstrapTestRelayMaxLimit
	if filter.Limit > 0 && filter.Limit < limit {
		limit = filter.Limit
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

func newBootstrapFakeRelayPool(t *testing.T, relays ...*bootstrapFakeRelay) *RelayPool {
	t.Helper()
	urls := make([]string, 0, len(relays))
	byURL := make(map[string]*bootstrapFakeRelay, len(relays))
	for _, relay := range relays {
		urls = append(urls, relay.url)
		byURL[gonostr.NormalizeURL(relay.url)] = relay
	}
	pool := newRelayPoolWithManagedRelays(urls...)
	for _, url := range pool.URLs() {
		markRelayConnectedForSubscribeTest(pool, url)
	}
	setSubscribeOnRelayForTest(t, func(relay *gonostr.Relay, _ context.Context, filter gonostr.Filter) (*gonostr.Subscription, error) {
		fake := byURL[gonostr.NormalizeURL(relay.URL)]
		require.NotNil(t, fake, "unexpected relay %s", relay.URL)
		fake.mu.Lock()
		fake.filters = append(fake.filters, filter)
		fake.mu.Unlock()
		if fake.store != nil && fake.closeWith != "" {
			sub := &gonostr.Subscription{
				Events:            make(chan gonostr.Event),
				EndOfStoredEvents: make(chan gonostr.EndOfStoredEvent),
				ClosedReason:      make(chan string, 1),
			}
			sub.ClosedReason <- fake.closeWith
			close(sub.Events)
			return sub, nil
		}
		if fake.store != nil {
			events := fake.query(filter)
			sub := &gonostr.Subscription{
				Events:            make(chan gonostr.Event, len(events)),
				EndOfStoredEvents: make(chan gonostr.EndOfStoredEvent),
				ClosedReason:      make(chan string, 1),
			}
			for _, event := range events {
				sub.Events <- event
			}
			close(sub.EndOfStoredEvents)
			return sub, nil
		}
		sub := newTestSubscription()
		fake.subs <- bootstrapFakeRelaySub{filter: filter, sub: sub}
		return sub, nil
	})
	return pool
}

// observeConsumedRelayEOSE wraps each bootstrap subscription so the test
// learns exactly when the bootstrapper has consumed a relay's EOSE.
func observeConsumedRelayEOSE(t *testing.T) <-chan string {
	t.Helper()
	consumed := make(chan string, 16)
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(pool *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		merged, err := original(pool, ctx, filters)
		if err != nil {
			return nil, err
		}
		proxy := make(chan RelayEOSE)
		done := make(chan struct{})
		var closeOnce sync.Once
		go func() {
			defer close(proxy)
			for {
				select {
				case eose, ok := <-merged.RelayEOSE:
					if !ok {
						return
					}
					select {
					case proxy <- eose:
						consumed <- eose.RelayURL
					case <-done:
						return
					}
				case <-done:
					return
				}
			}
		}()
		return &MergedSubscription{
			Events:            merged.Events,
			EndOfStoredEvents: merged.EndOfStoredEvents,
			RelayEOSE:         proxy,
			Closed:            merged.Closed,
			closeFn: func() {
				closeOnce.Do(func() { close(done) })
				merged.Close()
			},
			eventSources: merged.eventSources,
			active:       merged.active,
		}, nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })
	return consumed
}

type bootstrapSourceRecorder struct {
	mu      sync.Mutex
	sources map[string]struct{}
}

func (r *bootstrapSourceRecorder) Apply(_ context.Context, event *DecodedProjectionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sources == nil {
		r.sources = make(map[string]struct{})
	}
	r.sources[event.SourceID] = struct{}{}
	return nil
}

func (r *bootstrapSourceRecorder) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sources[id]
	return ok
}

func (r *bootstrapSourceRecorder) ids() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]struct{}, len(r.sources))
	for id := range r.sources {
		out[id] = struct{}{}
	}
	return out
}

func signedBootstrapEventBy(t *testing.T, privateKeyHex string, kind int, dTag string, createdAt gonostr.Timestamp) gonostr.Event {
	t.Helper()
	event := gonostr.Event{
		Kind:      canonicalKind(kind),
		CreatedAt: createdAt,
		Tags:      gonostr.Tags{{"d", dTag}},
		Content:   `{"ok":true}`,
	}
	require.NoError(t, signEventWithPrivateKeyHex(&event, privateKeyHex))
	return event
}

func bootstrapTestPubkey(t *testing.T, privateKeyHex string) string {
	t.Helper()
	pubkey, err := publicKeyHexFromPrivateKeyHex(privateKeyHex)
	require.NoError(t, err)
	return pubkey
}

// discoveryOnlyBootstrapper returns a bootstrapper with a single required
// group (discovery_snapshot) so relay-level EOSE semantics can be tested
// without multi-group interference.
func discoveryOnlyBootstrapper(t *testing.T, pool *RelayPool, cache BootstrapCacheApplier) *Bootstrapper {
	t.Helper()
	servicePubkey := bootstrapTestPubkey(t, testNostrPrivateKey)
	discoveryKinds := []int{KindBahiaIdentityDefinition, KindBahiaReplayCheckpoint, KindBahiaReadinessStatus}
	catalog := &KindCatalog{
		Version: "test-discovery-only",
		Groups: []ReplayGroup{
			{Name: "discovery_snapshot", Kinds: discoveryKinds, Snapshot: true, Required: true, Authors: ReplayAuthorsProjection},
		},
		decoders: make(map[int]DecodeFunc),
	}
	for _, kind := range discoveryKinds {
		kind := kind
		catalog.decoders[kind] = func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
			return &DecodedProjectionEvent{
				Kind:      eventKindInt(ev),
				DTag:      tagValueLocal(ev.Tags, "d"),
				Timestamp: ev.CreatedAt.Time().UTC(),
				SourceID:  eventIDHex(ev),
				Family:    ProjectionFamily(fmt.Sprintf("test-%d", kind)),
			}, nil
		}
	}
	return NewBootstrapper(pool, catalog, nil, cache, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout:     time.Minute,
		CatchupTimeout:      time.Minute,
		ProjectionAuthors:   []string{servicePubkey},
		ControlPlaneAuthors: []string{servicePubkey},
	})
}

func runBootstrapAttempt(ctx context.Context, bootstrapper *Bootstrapper) <-chan error {
	done := make(chan error, 1)
	go func() { done <- bootstrapper.attemptBootstrap(ctx) }()
	return done
}

func TestBootstrapperSnapshotWaitsForEOSEFromEveryRelay(t *testing.T) {
	fast := newManualBootstrapRelay("wss://fast.example")
	slow := newManualBootstrapRelay("wss://slow.example")
	pool := newBootstrapFakeRelayPool(t, fast, slow)
	consumed := observeConsumedRelayEOSE(t)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := discoveryOnlyBootstrapper(t, pool, cache)

	done := runBootstrapAttempt(context.Background(), bootstrapper)
	fastSub := <-fast.subs
	slowSub := <-slow.subs

	now := gonostr.Now()
	stale := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaIdentityDefinition, "identity", now-10)
	fastSub.sub.Events <- stale
	close(fastSub.sub.EndOfStoredEvents)
	require.Equal(t, gonostr.NormalizeURL(fast.url), <-consumed)

	// The slower relay holds the newer version. It must still be merged.
	fresh := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaIdentityDefinition, "identity", now-5)
	slowSub.sub.Events <- fresh
	close(slowSub.sub.EndOfStoredEvents)

	require.NoError(t, <-done)
	require.True(t, cache.has(stale.ID.Hex()))
	require.True(t, cache.has(fresh.ID.Hex()), "events from the slower relay were dropped after the first relay's EOSE")
	require.Equal(t, BootstrapPhaseReady, bootstrapper.Progress().Phase)
}

func TestBootstrapperClosedRelayIsTerminalButNotSuccessForOtherRelays(t *testing.T) {
	closing := newManualBootstrapRelay("wss://closing.example")
	fast := newManualBootstrapRelay("wss://fast.example")
	slow := newManualBootstrapRelay("wss://slow.example")
	pool := newBootstrapFakeRelayPool(t, closing, fast, slow)
	consumed := observeConsumedRelayEOSE(t)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := discoveryOnlyBootstrapper(t, pool, cache)

	done := runBootstrapAttempt(context.Background(), bootstrapper)
	closingSub := <-closing.subs
	fastSub := <-fast.subs
	slowSub := <-slow.subs

	closingSub.sub.ClosedReason <- "error: shutting down"
	close(closingSub.sub.Events)

	now := gonostr.Now()
	fastEvent := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaReadinessStatus, "readiness", now-10)
	fastSub.sub.Events <- fastEvent
	close(fastSub.sub.EndOfStoredEvents)
	require.Equal(t, gonostr.NormalizeURL(fast.url), <-consumed)

	slowEvent := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaReplayCheckpoint, "checkpoint", now-5)
	slowSub.sub.Events <- slowEvent
	close(slowSub.sub.EndOfStoredEvents)

	require.NoError(t, <-done)
	require.True(t, cache.has(fastEvent.ID.Hex()))
	require.True(t, cache.has(slowEvent.ID.Hex()), "a CLOSED relay plus one EOSE must not complete the group for the still-pending relay")
}

func TestBootstrapperPagesSnapshotPastRelayQueryLimit(t *testing.T) {
	now := gonostr.Now()
	const total = bootstrapTestRelayMaxLimit + 100
	store := make([]gonostr.Event, 0, total)
	for i := 0; i < total; i++ {
		store = append(store, signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaIdentityDefinition, "identity-"+time.Duration(i).String(), now-gonostr.Timestamp(total+10-i)))
	}
	relay := &bootstrapFakeRelay{url: "wss://capped.example", store: store}
	pool := newBootstrapFakeRelayPool(t, relay)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := discoveryOnlyBootstrapper(t, pool, cache)

	require.NoError(t, bootstrapper.attemptBootstrap(context.Background()))

	applied := cache.ids()
	for _, event := range store {
		_, ok := applied[event.ID.Hex()]
		require.True(t, ok, "snapshot event created_at=%d was silently dropped by the relay query cap", event.CreatedAt)
	}
	filters := relay.recordedFilters()
	require.GreaterOrEqual(t, len(filters), 2, "a full page must be followed by an until-bounded page")
	for i, filter := range filters {
		require.Positive(t, filter.Limit, "page %d REQ has no explicit limit", i)
		require.LessOrEqual(t, filter.Limit, bootstrapTestRelayMaxLimit)
	}
	for i := 1; i < len(filters); i++ {
		require.Less(t, filters[i].Until, filters[i-1].Until, "page %d did not move until backwards", i)
	}
}

func TestBootstrapperRejectsOutOfScopeAuthorsForEveryRequiredGroup(t *testing.T) {
	servicePubkey := bootstrapTestPubkey(t, testNostrPrivateKey)
	operatorPubkey := bootstrapTestPubkey(t, bootstrapTestOperatorKey)
	strangerPubkey := bootstrapTestPubkey(t, bootstrapTestStrangerKey)

	catalog := NewKindCatalog()
	groups := catalog.RequiredGroups()
	require.NotEmpty(t, groups)
	now := gonostr.Now()
	var store []gonostr.Event
	authorOf := make(map[string]string)
	for _, group := range groups {
		for _, key := range []string{testNostrPrivateKey, bootstrapTestOperatorKey, bootstrapTestStrangerKey} {
			createdAt := now - 30
			if !group.Snapshot {
				createdAt = now + 1
			}
			event := signedBootstrapEventBy(t, key, group.Kinds[0], group.Name+"-"+key[:4], createdAt)
			store = append(store, event)
			authorOf[event.ID.Hex()] = event.PubKey.Hex()
		}
	}
	relay := &bootstrapFakeRelay{url: "wss://noisy.example", store: store, ignoreAuthors: true}
	pool := newBootstrapFakeRelayPool(t, relay)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := NewBootstrapper(pool, catalog, nil, cache, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout:     time.Minute,
		CatchupTimeout:      time.Minute,
		ProjectionAuthors:   []string{servicePubkey},
		ControlPlaneAuthors: []string{servicePubkey, operatorPubkey},
	})

	require.NoError(t, bootstrapper.attemptBootstrap(context.Background()))

	filters := relay.recordedFilters()
	require.NotEmpty(t, filters)
	for _, filter := range filters {
		require.NotEmpty(t, filter.Authors, "required replay group REQ for kinds %v is not author-scoped", filter.Kinds)
		for _, author := range filter.Authors {
			require.NotEqual(t, strangerPubkey, author.Hex())
		}
	}
	applied := cache.ids()
	require.NotEmpty(t, applied)
	for id := range applied {
		require.NotEqual(t, strangerPubkey, authorOf[id], "event %s from an out-of-scope author was applied", id)
	}
}
