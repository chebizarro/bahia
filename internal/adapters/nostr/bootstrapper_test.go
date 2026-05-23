package nostr

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	gonostr "github.com/nbd-wtf/go-nostr"
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
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{
		RequestedTier:   1,
		SnapshotTimeout: 50 * time.Millisecond,
		CatchupTimeout:  50 * time.Millisecond,
	})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Equal(t, 1, bootstrapper.ReadyTier())
	require.Equal(t, 2, cache.count())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 1, progress.RequestedTier)
	require.Equal(t, 1, progress.ReadyTier)
	require.Equal(t, 3, progress.GroupsTotal)
	require.Equal(t, 3, progress.GroupsComplete)
	require.False(t, progress.StartedAt.IsZero())
}

func TestBootstrapperTimeoutFallsBackToLowerTier(t *testing.T) {
	catalog := testBootstrapCatalog()
	cache := &bootstrapApplyRecorder{}
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Snapshot, "snapshot-1")}, eose: true},
		testKindTier1Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier1Live, "live-1")}, eose: true},
		testKindTier2Snapshot: {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Snapshot, "snapshot-2")}, eose: false},
		testKindTier2Live:     {events: []*gonostr.Event{signedBootstrapEvent(t, testKindTier2Live, "live-2")}, eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, cache, zap.NewNop(), BootstrapConfig{
		RequestedTier:   2,
		SnapshotTimeout: time.Millisecond,
		CatchupTimeout:  50 * time.Millisecond,
	})

	err := bootstrapper.Run(context.Background())

	require.NoError(t, err)
	require.True(t, bootstrapper.Ready())
	require.Equal(t, 1, bootstrapper.ReadyTier())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseReady, progress.Phase)
	require.Equal(t, 5, progress.GroupsTotal)
	require.Equal(t, 4, progress.GroupsComplete)
	require.Equal(t, 4, cache.count())
}

func TestBootstrapperNoRelayDataFails(t *testing.T) {
	catalog := testBootstrapCatalog()
	setBootstrapSubscribeScript(t, map[int]scriptedBootstrapSubscription{
		testKindTier0Snapshot: {eose: true},
		testKindTier1Snapshot: {eose: true},
		testKindTier1Live:     {eose: true},
	})

	bootstrapper := NewBootstrapper(nil, catalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{
		RequestedTier:   1,
		SnapshotTimeout: 50 * time.Millisecond,
		CatchupTimeout:  50 * time.Millisecond,
	})

	err := bootstrapper.Run(context.Background())

	require.Error(t, err)
	require.False(t, bootstrapper.Ready())
	require.Equal(t, -1, bootstrapper.ReadyTier())
	progress := bootstrapper.Progress()
	require.Equal(t, BootstrapPhaseFailed, progress.Phase)
	require.Equal(t, 3, progress.GroupsComplete)
}

func TestBootstrapperProgressReturnsSnapshot(t *testing.T) {
	bootstrapper := NewBootstrapper(nil, testBootstrapCatalog(), nil, nil, zap.NewNop(), BootstrapConfig{RequestedTier: 2})
	startedAt := time.Unix(100, 0).UTC()
	bootstrapper.setProgress(func(progress *BootstrapProgress) {
		progress.Phase = BootstrapPhaseLiveCatchup
		progress.RequestedTier = 2
		progress.ReadyTier = 1
		progress.GroupsTotal = 5
		progress.GroupsComplete = 3
		progress.StartedAt = startedAt
	})

	progress := bootstrapper.Progress()

	require.Equal(t, BootstrapPhaseLiveCatchup, progress.Phase)
	require.Equal(t, 2, progress.RequestedTier)
	require.Equal(t, 1, progress.ReadyTier)
	require.Equal(t, 5, progress.GroupsTotal)
	require.Equal(t, 3, progress.GroupsComplete)
	require.Equal(t, startedAt, progress.StartedAt)
}

func TestBootstrapperReadyTierComputation(t *testing.T) {
	bootstrapper := NewBootstrapper(nil, testBootstrapCatalog(), nil, nil, zap.NewNop(), BootstrapConfig{RequestedTier: 2})

	readyTier2 := bootstrapper.computeReadyTier(map[string]bool{
		"tier0_snapshot": true,
		"tier1_snapshot": true,
		"tier1_live":     true,
		"tier2_snapshot": true,
		"tier2_live":     true,
	})
	require.Equal(t, 2, readyTier2)

	readyTier1 := bootstrapper.computeReadyTier(map[string]bool{
		"tier0_snapshot": true,
		"tier1_snapshot": true,
		"tier1_live":     true,
		"tier2_snapshot": false,
		"tier2_live":     true,
	})
	require.Equal(t, 1, readyTier1)

	failed := bootstrapper.computeReadyTier(map[string]bool{
		"tier0_snapshot": true,
		"tier1_snapshot": false,
		"tier1_live":     true,
	})
	require.Equal(t, 0, failed)
}

func testBootstrapCatalog() *KindCatalog {
	groups := []ReplayGroup{
		{Name: "tier0_snapshot", Kinds: []int{testKindTier0Snapshot}, Tier: 0, Snapshot: true, Required: true},
		{Name: "tier1_snapshot", Kinds: []int{testKindTier1Snapshot}, Tier: 1, Snapshot: true, Required: true},
		{Name: "tier1_live", Kinds: []int{testKindTier1Live}, Tier: 1, Snapshot: false, Required: true},
		{Name: "tier2_snapshot", Kinds: []int{testKindTier2Snapshot}, Tier: 2, Snapshot: true, Required: true},
		{Name: "tier2_live", Kinds: []int{testKindTier2Live}, Tier: 2, Snapshot: false, Required: true},
	}
	catalog := &KindCatalog{Version: "test", Groups: groups, decoders: make(map[int]DecodeFunc)}
	for _, group := range groups {
		for _, kind := range group.Kinds {
			kind := kind
			catalog.decoders[kind] = func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
				return &DecodedProjectionEvent{
					Kind:      ev.Kind,
					DTag:      tagValueLocal(ev.Tags, "d"),
					Timestamp: ev.CreatedAt.Time().UTC(),
					SourceID:  ev.ID,
					Family:    ProjectionFamily(fmt.Sprintf("test-%d", kind)),
				}, nil
			}
		}
	}
	return catalog
}

func setBootstrapSubscribeScript(t *testing.T, scripts map[int]scriptedBootstrapSubscription) {
	t.Helper()
	original := bootstrapSubscribeAllWithEOSE
	bootstrapSubscribeAllWithEOSE = func(_ *RelayPool, ctx context.Context, filters []gonostr.Filter) (*MergedSubscription, error) {
		require.Len(t, filters, 1)
		require.Len(t, filters[0].Kinds, 1)
		script, ok := scripts[filters[0].Kinds[0]]
		if !ok {
			return nil, fmt.Errorf("unexpected subscription for kind %d", filters[0].Kinds[0])
		}
		return scriptedMergedSubscription(ctx, script), nil
	}
	t.Cleanup(func() { bootstrapSubscribeAllWithEOSE = original })
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
		Kind:      kind,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", dTag}},
		Content:   `{"ok":true}`,
	}
	require.NoError(t, event.Sign(gonostr.GeneratePrivateKey()))
	return event
}
