package nostr

import (
	"context"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Readiness means "synced", not "non-empty" (bahia-irsry.20): every relay a
// replay REQ reached must be terminal (EOSE, CLOSED or dropped) for every
// required group, and at least one relay must have sent a real EOSE. These
// tests are driven purely by EVENT/EOSE/CLOSED frames through a real
// RelayPool merged subscription.

func productionCatalogBootstrapper(t *testing.T, pool *RelayPool, cache BootstrapCacheApplier) *Bootstrapper {
	t.Helper()
	servicePubkey := bootstrapTestPubkey(t, testNostrPrivateKey)
	return NewBootstrapper(pool, NewKindCatalog(), nil, cache, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout:     time.Minute,
		CatchupTimeout:      time.Minute,
		ProjectionAuthors:   []string{servicePubkey},
		ControlPlaneAuthors: []string{servicePubkey},
	})
}

func TestBootstrapperEmptyFleetBecomesReadyWhenEveryRelaySendsEOSE(t *testing.T) {
	// A non-nil empty store answers every REQ with zero events then EOSE.
	one := &bootstrapFakeRelay{url: "wss://one.example", store: []gonostr.Event{}}
	two := &bootstrapFakeRelay{url: "wss://two.example", store: []gonostr.Event{}}
	pool := newBootstrapFakeRelayPool(t, one, two)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := productionCatalogBootstrapper(t, pool, cache)

	require.NoError(t, bootstrapper.attemptBootstrap(context.Background()))

	require.True(t, bootstrapper.Ready())
	require.True(t, bootstrapper.Ready())
	require.Empty(t, cache.ids())
	required := len(NewKindCatalog().RequiredGroups())
	require.Equal(t, required, bootstrapper.Progress().GroupsComplete)
	require.Len(t, one.recordedFilters(), required, "every required group must be replayed from every relay")
	require.Len(t, two.recordedFilters(), required, "every required group must be replayed from every relay")
}

func TestBootstrapperEmptyFleetWaitsForEveryRelayBeforeReady(t *testing.T) {
	fast := newManualBootstrapRelay("wss://fast.example")
	slow := newManualBootstrapRelay("wss://slow.example")
	pool := newBootstrapFakeRelayPool(t, fast, slow)
	consumed := observeConsumedRelayEOSE(t)
	cache := &bootstrapSourceRecorder{}
	bootstrapper := discoveryOnlyBootstrapper(t, pool, cache)

	done := runBootstrapAttempt(context.Background(), bootstrapper)
	fastSub := <-fast.subs
	slowSub := <-slow.subs

	close(fastSub.sub.EndOfStoredEvents)
	require.Equal(t, gonostr.NormalizeURL(fast.url), <-consumed)
	select {
	case err := <-done:
		t.Fatalf("attempt finished (err=%v) on the first relay's EOSE while another relay was pending", err)
	default:
	}

	// The slow relay still holds history; it must be merged before ready.
	late := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaIdentityDefinition, "identity", gonostr.Now()-5)
	slowSub.sub.Events <- late
	close(slowSub.sub.EndOfStoredEvents)

	require.NoError(t, <-done)
	require.True(t, bootstrapper.Ready())
	require.True(t, cache.has(late.ID.Hex()))
}

func TestBootstrapperEmptyFleetWithOneEOSEAndOneDroppedRelayIsReady(t *testing.T) {
	synced := newManualBootstrapRelay("wss://synced.example")
	dropped := newManualBootstrapRelay("wss://dropped.example")
	pool := newBootstrapFakeRelayPool(t, synced, dropped)
	bootstrapper := discoveryOnlyBootstrapper(t, pool, &bootstrapSourceRecorder{})

	done := runBootstrapAttempt(context.Background(), bootstrapper)
	syncedSub := <-synced.subs
	droppedSub := <-dropped.subs

	close(droppedSub.sub.Events)
	close(syncedSub.sub.EndOfStoredEvents)

	require.NoError(t, <-done)
	require.True(t, bootstrapper.Ready())
}

func TestBootstrapperNotReadyWhenNoRelaySendsEOSE(t *testing.T) {
	closeRelay := func(sub *gonostr.Subscription) {
		sub.ClosedReason <- "error: shutting down"
		close(sub.Events)
	}
	dropRelay := func(sub *gonostr.Subscription) {
		close(sub.Events)
	}
	tests := []struct {
		name string
		one  func(*gonostr.Subscription)
		two  func(*gonostr.Subscription)
	}{
		{name: "all relays CLOSED", one: closeRelay, two: closeRelay},
		{name: "all relays dropped", one: dropRelay, two: dropRelay},
		{name: "one CLOSED one dropped", one: closeRelay, two: dropRelay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			one := newManualBootstrapRelay("wss://one.example")
			two := newManualBootstrapRelay("wss://two.example")
			pool := newBootstrapFakeRelayPool(t, one, two)
			cache := &bootstrapSourceRecorder{}
			bootstrapper := discoveryOnlyBootstrapper(t, pool, cache)

			done := runBootstrapAttempt(context.Background(), bootstrapper)
			oneSub := <-one.subs
			twoSub := <-two.subs

			// Events sent before a relay closes are still not sync evidence.
			event := signedBootstrapEventBy(t, testNostrPrivateKey, KindBahiaIdentityDefinition, "identity", gonostr.Now()-5)
			oneSub.sub.Events <- event
			tt.one(oneSub.sub)
			tt.two(twoSub.sub)

			err := <-done
			require.Error(t, err)
			require.False(t, bootstrapper.Ready())
			require.False(t, bootstrapper.Ready())
			progress := bootstrapper.Progress()
			require.Equal(t, BootstrapPhaseFailed, progress.Phase)
			require.Zero(t, progress.GroupsComplete)
			require.Contains(t, progress.LastError, "before any relay EOSE")
		})
	}
}

func TestBootstrapperCatalogWithNoRequiredGroupsIsNeverReady(t *testing.T) {
	// A catalog with no required groups proves nothing about relays.
	empty := NewBootstrapper(nil, &KindCatalog{Version: "empty"}, nil, nil, zap.NewNop(), BootstrapConfig{})
	require.Error(t, empty.attemptBootstrap(context.Background()), "a catalog with no required groups proves nothing about relays")
	require.False(t, empty.Ready())
}
