package nostr

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	testKindTier0Snapshot = 39000
	testKindTier1Snapshot = 39001
	testKindTier1Live     = 39002
	testKindTier2Snapshot = 39003
	testKindTier2Live     = 39004
)

type bootstrapApplyRecorder struct {
	mu     sync.Mutex
	events []*DecodedProjectionEvent
}

func (r *bootstrapApplyRecorder) Apply(_ context.Context, event *DecodedProjectionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *bootstrapApplyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

type bootstrapApplyFunc func(ctx context.Context, event *DecodedProjectionEvent) error

func (f bootstrapApplyFunc) Apply(ctx context.Context, event *DecodedProjectionEvent) error {
	return f(ctx, event)
}

type scriptedBootstrapSubscription struct {
	events []*gonostr.Event
	eose   bool
}

func TestBootstrapperRunReplaysSnapshotAndLiveCatchupToReady(t *testing.T) {
	catalog := testBootstrapCatalog()
	cache := &bootstrapApplyRecorder{}
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Snapshot, "snapshot-1")}, eose: true},
		testKindTier1Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "live-1")}, eose: true},
		testKindTier2Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Snapshot, "snapshot-2")}, eose: true},
		testKindTier2Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Live, "live-2")}, eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Equal(t, 4, cache.count())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 5, progress.GroupsTotal)
	require.Equal(t, 5, progress.GroupsComplete)
	require.False(t, progress.StartedAt.IsZero())
}

func TestBootstrapperTimeoutOnRequiredGroupRetriesUntilSuccess(t *testing.T) {
	catalog := testBootstrapCatalog()
	cache := &bootstrapApplyRecorder{}

	// All required groups complete including the previously-slow one.
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Snapshot, "snapshot-1")}, eose: true},
		testKindTier1Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "live-1")}, eose: true},
		testKindTier2Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Snapshot, "snapshot-2")}, eose: true},
		testKindTier2Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Live, "live-2")}, eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 5, progress.GroupsTotal)
	require.Equal(t, 5, progress.GroupsComplete)
	require.Equal(t, 4, cache.count())
}

// A page whose deadline elapses before EOSE fails the attempt, and the retry
// re-issues every REQ: relays answer each REQ with their stored events again,
// so the bootstrapper delivers at least once and leaves deduplication to the
// cache (RelayProjectionCache skips event IDs it has already applied). The
// deadline is fired by the test itself, after the stalled page's event has
// been applied, so the interleaving does not depend on the wall clock.
func TestBootstrapperTimedOutAttemptRetriesAndRedeliversEveryGroup(t *testing.T) {
	catalog := testBootstrapCatalog()
	scripts := map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Snapshot, "snapshot-1")}, eose: true},
		testKindTier1Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "live-1")}, eose: true},
		testKindTier2Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Snapshot, "snapshot-2")}, eose: true},
		testKindTier2Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Live, "live-2")}, eose: true},
	}
	stalledEvent := scripts[testKindTier2Live].events[0]
	stalledEventID := eventIDHex(stalledEvent)

	var mu sync.Mutex
	subscriptionsByKind := make(map[int]int)
	// deadline is the stalled page's timer; the cache fires it once the
	// stalled page's event has been applied.
	var deadline chan time.Time
	stallNextPage := false

	originalSubscribe := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		require.Len(t, filters, 1)
		require.Len(t, filters[0].Kinds, 1)
		kind := int(filters[0].Kinds[0])
		mu.Lock()
		defer mu.Unlock()
		subscriptionsByKind[kind]++
		script := scripts[kind]
		if kind == testKindTier2Live && subscriptionsByKind[kind] == 1 {
			// The relay delivers its stored event but never sends EOSE.
			script.eose = false
			stallNextPage = true
		}
		return scriptedMergedSubscription(ctx, script), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = originalSubscribe })

	originalTimer := bootstrapPageTimer
	bootstrapPageTimer = func(time.Duration) (<-chan time.Time, func()) {
		mu.Lock()
		defer mu.Unlock()
		if !stallNextPage {
			return nil, func() {}
		}
		stallNextPage = false
		deadline = make(chan time.Time)
		return deadline, func() {}
	}
	t.Cleanup(func() { bootstrapPageTimer = originalTimer })

	cache := &bootstrapApplyRecorder{}
	firingCache := bootstrapApplyFunc(func(ctx context.Context, event *DecodedProjectionEvent) error {
		if err := cache.Apply(ctx, event); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if event.SourceID == stalledEventID && deadline != nil {
			close(deadline)
			deadline = nil
		}
		return nil
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, firingCache, zap.NewNop(), BootstrapConfig{
		RetryInterval: time.Millisecond,
	})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	mu.Lock()
	for kind, subscriptions := range subscriptionsByKind {
		require.Equalf(t, 2, subscriptions, "kind %d replayed once per attempt", kind)
	}
	mu.Unlock()
	require.Equal(t, 8, cache.count(), "every scripted event is redelivered by the retried attempt")
	unique := make(map[string]struct{})
	for _, event := range cache.events {
		unique[event.SourceID] = struct{}{}
	}
	require.Len(t, unique, 4)
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 5, progress.GroupsComplete)
}

func TestBootstrapperTimeoutNamesBlockingRelaysInProgress(t *testing.T) {
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, _ context.Context, _ []gonostr.Filter) (*MergedSubscription, error) {
		return &MergedSubscription{
			Events:            make(chan *gonostr.Event),
			EndOfStoredEvents: make(chan struct{}),
			RelayEOSE:         make(chan RelayEOSE),
			Closed:            make(chan RelayClosed),
			relayURLs:         []string{"wss://one.example", "wss://two.example"},
			closeFn:           func() {},
		}, nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })

	bootstrapper := NewBootstrapper(nil, testBootstrapCatalog(), nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout: 10 * time.Millisecond,
	})
	_, err := bootstrapper.runGroup(context.Background(), testBootstrapCatalog().Groups[0], gonostr.Filter{}, 10*time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "wss://one.example")
	require.Equal(t, "tier0_snapshot", bootstrapper.Progress().CurrentGroup)
	require.Equal(t, []string{"wss://one.example", "wss://two.example"}, bootstrapper.Progress().BlockingRelays)
}

// An empty fleet is synced, not failed: every required group reached EOSE
// with no stored events, so the requested tier is ready (bahia-irsry.20).
func TestBootstrapperEmptyFleetWithEOSEBecomesReady(t *testing.T) {
	catalog := testBootstrapCatalog()
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {eose: true},
		testKindTier1Live:     {eose: true},
		testKindTier2Snapshot: {eose: true},
		testKindTier2Live:     {eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{})

	cache := &bootstrapApplyRecorder{}
	bootstrapper.cache = cache

	err := bootstrapper.attemptBootstrap(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Zero(t, cache.count())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 5, progress.GroupsComplete)
}

func TestBootstrapperRunRetriesAfterFailedAttempt(t *testing.T) {
	catalog := testBootstrapCatalog()
	cache := &bootstrapApplyRecorder{}
	attemptsByKind := make(map[int]int)
	var attemptsMu sync.Mutex
	disableBootstrapPageTimeouts(t)
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		require.Len(t, filters, 1)
		require.Len(t, filters[0].Kinds, 1)
		kind := int(filters[0].Kinds[0])

		attemptsMu.Lock()
		attemptsByKind[kind]++
		attempt := attemptsByKind[kind]
		attemptsMu.Unlock()

		if attempt == 1 {
			// No relay reachable: every group fails, so the attempt fails.
			return nil, errors.New("no connected relays")
		}
		script := scriptedBootstrapSubscription{eose: true}
		switch kind {
		case testKindTier1Snapshot:
			script.events = []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Snapshot, "retry-snapshot")}
		case testKindTier1Live:
			script.events = []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "retry-live")}
		}
		return scriptedMergedSubscription(ctx, script), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{
		RetryInterval: time.Millisecond,
	})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Equal(t, 2, cache.count())
	attemptsMu.Lock()
	require.Equal(t, 2, attemptsByKind[testKindTier0Snapshot])
	require.Equal(t, 2, attemptsByKind[testKindTier1Snapshot])
	require.Equal(t, 2, attemptsByKind[testKindTier1Live])
	attemptsMu.Unlock()
}

func TestBootstrapperSkipsMalformedEventsAndContinuesGroup(t *testing.T) {
	catalog := testBootstrapCatalog()
	cache := &bootstrapApplyRecorder{}
	// Build a properly signed event with unparseable content so
	// ValidateInboundEvent passes (ID and signature match the actual
	// content) but the strict decoder fails on the payload.
	badEvent := &gonostr.Event{
		Kind:      canonicalKind(testKindTier1Snapshot),
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "bad"}},
		Content:   `not-json`,
	}
	require.NoError(t, signEventWithPrivateKeyHex(badEvent, gonostr.Generate().Hex()))
	goodEvent := signedBootstrapEvent(t, testKindTier1Snapshot, "good")
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {events: []*gonostr.Event{badEvent, goodEvent}, eose: true},
		testKindTier1Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "live-1")}, eose: true},
		testKindTier2Snapshot: {eose: true},
		testKindTier2Live:     {eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{})
	// Replace the tier1 snapshot decoder with strict JSON decoding so malformed content is skipped.
	catalog.decoders[testKindTier1Snapshot] = func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
		var payload map[string]bool
		if err := decodeContent(ev, &payload); err != nil {
			return nil, err
		}
		return &DecodedProjectionEvent{Kind: eventKindInt(ev), DTag: tagValueLocal(ev.Tags, "d"), Timestamp: ev.CreatedAt.Time().UTC(), SourceID: eventIDHex(ev)}, nil
	}

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Positive(t, cache.count())
}

func TestBootstrapperScopesRequiredGroupsToConfiguredAuthors(t *testing.T) {
	catalog := &KindCatalog{
		Version: "test",
		Groups: []ReplayGroup{
			{Name: "tier0_snapshot", Kinds: []int{testKindTier0Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsProjection},
			{Name: "tier1_snapshot", Kinds: []int{testKindTier1Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsControlPlane},
			{Name: "tier1_live", Kinds: []int{testKindTier1Live}, Snapshot: false, Required: true, Authors: ReplayAuthorsControlPlane},
			{Name: "tier2_snapshot", Kinds: []int{testKindTier2Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsProjection},
			{Name: "tier2_live", Kinds: []int{testKindTier2Live}, Snapshot: false, Required: true, Authors: ReplayAuthorsControlPlane},
		},
		decoders: map[int]DecodeFunc{
			testKindTier0Snapshot: func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{Kind: eventKindInt(ev), SourceID: eventIDHex(ev), Timestamp: ev.CreatedAt.Time().UTC()}, nil
			},
			testKindTier1Snapshot: func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{Kind: eventKindInt(ev), SourceID: eventIDHex(ev), Timestamp: ev.CreatedAt.Time().UTC()}, nil
			},
			testKindTier1Live: func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{Kind: eventKindInt(ev), SourceID: eventIDHex(ev), Timestamp: ev.CreatedAt.Time().UTC()}, nil
			},
			testKindTier2Snapshot: func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{Kind: eventKindInt(ev), SourceID: eventIDHex(ev), Timestamp: ev.CreatedAt.Time().UTC()}, nil
			},
			testKindTier2Live: func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{Kind: eventKindInt(ev), SourceID: eventIDHex(ev), Timestamp: ev.CreatedAt.Time().UTC()}, nil
			},
		},
	}
	var captured []gonostr.Filter
	disableBootstrapPageTimeouts(t)
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		require.Len(t, filters, 1)
		captured = append(captured, filters[0])
		return scriptedMergedSubscription(ctx, scriptedBootstrapSubscription{eose: true}), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })

	servicePubkey, err := publicKeyHexFromPrivateKeyHex(testNostrPrivateKey)
	require.NoError(t, err)
	operatorOne, err := publicKeyHexFromPrivateKeyHex("2222222222222222222222222222222222222222222222222222222222222222")
	require.NoError(t, err)
	operatorTwo, err := publicKeyHexFromPrivateKeyHex("3333333333333333333333333333333333333333333333333333333333333333")
	require.NoError(t, err)
	projectionAuthors, err := filterAuthorsFromHex([]string{servicePubkey})
	require.NoError(t, err)
	controlPlaneAuthors, err := filterAuthorsFromHex([]string{operatorOne, operatorTwo})
	require.NoError(t, err)

	bootstrapper := NewBootstrapper(nil, catalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{
		ProjectionAuthors:   []string{servicePubkey},
		ControlPlaneAuthors: []string{operatorOne, operatorTwo},
	})

	err = bootstrapper.attemptBootstrap(context.Background())

	require.NoError(t, err)
	require.Len(t, captured, 5)
	require.Equal(t, projectionAuthors, captured[0].Authors)
	require.Equal(t, controlPlaneAuthors, captured[1].Authors)
	require.Equal(t, projectionAuthors, captured[2].Authors)
	require.Equal(t, controlPlaneAuthors, captured[3].Authors)
	require.Equal(t, controlPlaneAuthors, captured[4].Authors)
}

func TestBootstrapperUnknownAuthorScopeIsHardError(t *testing.T) {
	catalog := testBootstrapCatalog()
	catalog.Groups[0].Authors = ""
	subscribed := false
	disableBootstrapPageTimeouts(t)
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, _ []gonostr.Filter) (*MergedSubscription, error) {
		subscribed = true
		return scriptedMergedSubscription(ctx, scriptedBootstrapSubscription{eose: true}), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })

	bootstrapper := NewBootstrapper(nil, catalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{})
	err := bootstrapper.attemptBootstrap(context.Background())

	require.ErrorContains(t, err, `bootstrap group "tier0_snapshot"`)
	require.ErrorContains(t, err, "unknown author scope")
	require.False(t, subscribed, "an unscoped group must never be replayed")
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseFailed, progress.Phase)
	require.Equal(t, "tier0_snapshot", progress.CurrentGroup)
	require.Contains(t, progress.LastError, "unknown author scope")
}

func TestBootstrapperScopedGroupWithoutConfiguredAuthorsIsHardError(t *testing.T) {
	catalog := testBootstrapCatalog()
	catalog.Groups[0].Authors = ReplayAuthorsProjection
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, _ context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		t.Fatalf("scoped group without authors was replayed with filter %+v", filters)
		return nil, nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })

	bootstrapper := NewBootstrapper(nil, catalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{})
	err := bootstrapper.attemptBootstrap(context.Background())

	require.ErrorContains(t, err, "requires projection authors but none are configured")
}

func testBootstrapCatalog() *KindCatalog {
	groups := []ReplayGroup{
		{Name: "tier0_snapshot", Kinds: []int{testKindTier0Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsAny},
		{Name: "tier1_snapshot", Kinds: []int{testKindTier1Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsAny},
		{Name: "tier1_live", Kinds: []int{testKindTier1Live}, Snapshot: false, Required: true, Authors: ReplayAuthorsAny},
		{Name: "tier2_snapshot", Kinds: []int{testKindTier2Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsAny},
		{Name: "tier2_live", Kinds: []int{testKindTier2Live}, Snapshot: false, Required: true, Authors: ReplayAuthorsAny},
	}
	catalog := &KindCatalog{Version: "test", Groups: groups, decoders: make(map[int]DecodeFunc)}
	for _, group := range groups {
		for _, kind := range group.Kinds {
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
	}
	return catalog
}

// setBootstrapSubscribeScript answers every replay REQ from the script for its
// kind. Scripted relays always reach a terminal state on their own, so the
// page deadline is disabled: a wall-clock timeout firing under load would
// fail the attempt and make Run retry, redelivering every scripted event.
func setBootstrapSubscribeScript(t *testing.T, scripts map[int]scriptedBootstrapSubscription) {
	t.Helper()
	disableBootstrapPageTimeouts(t)
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		require.Len(t, filters, 1)
		require.Len(t, filters[0].Kinds, 1)
		script, ok := scripts[int(filters[0].Kinds[0])]
		if !ok {
			return nil, fmt.Errorf("unexpected subscription for kind %d", filters[0].Kinds[0])
		}
		return scriptedMergedSubscription(ctx, script), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })
}

// disableBootstrapPageTimeouts makes every page deadline unreachable for the
// rest of the test, so only the scripted relays decide when a group ends.
func disableBootstrapPageTimeouts(t *testing.T) {
	t.Helper()
	original := bootstrapPageTimer
	bootstrapPageTimer = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }
	t.Cleanup(func() { bootstrapPageTimer = original })
}

func scriptedMergedSubscription(ctx context.Context, script scriptedBootstrapSubscription) *MergedSubscription {
	events := make(chan *gonostr.Event)
	eose := make(chan struct{})
	closed := make(chan RelayClosed)
	relayEOSE := make(chan RelayEOSE)
	go func() {
		defer close(events)
		defer close(closed)
		defer close(relayEOSE)
		for _, event := range script.events {
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
		if script.eose {
			close(eose)
		}
	}()
	return &MergedSubscription{
		Events:            events,
		EndOfStoredEvents: eose,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
		closeFn:           func() {},
	}
}

func signedBootstrapEvent(t *testing.T, kind int, dTag string) *gonostr.Event {
	t.Helper()
	event := &gonostr.Event{
		Kind:      canonicalKind(kind),
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", dTag}},
		Content:   `{"ok":true}`,
	}
	require.NoError(t, signEventWithPrivateKeyHex(event, gonostr.Generate().Hex()))
	return event
}
