package nostr

import (
	"context"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func pagingTestBootstrapper(pool *RelayPool, pageLimit int, cache BootstrapCacheApplier) *Bootstrapper {
	return NewBootstrapper(pool, testBootstrapCatalog(), nil, cache, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout: time.Minute,
		CatchupTimeout:  time.Minute,
		PageLimit:       pageLimit,
	})
}

func requireAllApplied(t *testing.T, cache *bootstrapSourceRecorder, events ...[]gonostr.Event) {
	t.Helper()
	applied := cache.ids()
	for _, set := range events {
		for _, event := range set {
			_, ok := applied[event.ID.Hex()]
			require.True(t, ok, "event created_at=%d was not replayed", event.CreatedAt)
		}
	}
}

// The merged page mixes relays. Paging must follow the relay that filled its
// page even when another relay contributes newer events to the same page.
func TestBootstrapperPagingFollowsEveryFullRelay(t *testing.T) {
	now := gonostr.Now()
	var deep, shallow []gonostr.Event
	for i := 0; i < 7; i++ {
		deep = append(deep, signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier0Snapshot, fmt.Sprintf("deep-%d", i), now-200+gonostr.Timestamp(i)))
	}
	shallow = append(shallow, signedBootstrapEventBy(t, bootstrapTestOperatorKey, testKindTier0Snapshot, "shallow", now-10))
	deepRelay := &bootstrapFakeRelay{url: "wss://deep.example", store: deep}
	shallowRelay := &bootstrapFakeRelay{url: "wss://shallow.example", store: shallow}
	pool := newBootstrapFakeRelayPool(t, deepRelay, shallowRelay)
	cache := &bootstrapSourceRecorder{}

	require.NoError(t, pagingTestBootstrapper(pool, 2, cache).attemptBootstrap(context.Background()))

	requireAllApplied(t, cache, deep, shallow)
	for _, filter := range deepRelay.recordedFilters() {
		require.Equal(t, 2, filter.Limit)
	}
}

func TestBootstrapperPagingDeduplicatesOverlapAcrossPages(t *testing.T) {
	now := gonostr.Now()
	var store []gonostr.Event
	for i := 0; i < 5; i++ {
		// Pairs share a second so inclusive `until` re-delivers boundary events.
		store = append(store, signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier0Snapshot, fmt.Sprintf("pair-%d", i), now-100+gonostr.Timestamp(i/2)))
	}
	relay := &bootstrapFakeRelay{url: "wss://overlap.example", store: store}
	pool := newBootstrapFakeRelayPool(t, relay)
	recorder := &bootstrapApplyRecorder{}

	require.NoError(t, pagingTestBootstrapper(pool, 3, recorder).attemptBootstrap(context.Background()))

	require.Equal(t, len(store), recorder.count(), "boundary events re-delivered by an inclusive until must be applied once")
	require.GreaterOrEqual(t, len(relay.recordedFilters()), 2)
}

func TestBootstrapperPagingFailsLoudlyWhenAPageSharesOneSecond(t *testing.T) {
	now := gonostr.Now()
	var store []gonostr.Event
	for i := 0; i < 3; i++ {
		store = append(store, signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier0Snapshot, fmt.Sprintf("same-%d", i), now-50))
	}
	relay := &bootstrapFakeRelay{url: "wss://dense.example", store: store}
	pool := newBootstrapFakeRelayPool(t, relay)

	// Use a single-group catalog so that subsequent groups don't clear
	// LastError when they start (runGroup resets LastError on entry).
	singleGroupCatalog := &KindCatalog{
		Version: "test-paging",
		Groups: []ReplayGroup{
			{Name: "tier0_snapshot", Kinds: []int{testKindTier0Snapshot}, Snapshot: true, Required: true, Authors: ReplayAuthorsAny},
		},
		decoders: make(map[int]DecodeFunc),
	}
	singleGroupCatalog.decoders[testKindTier0Snapshot] = func(ev *gonostr.Event) (*DecodedProjectionEvent, error) {
		return &DecodedProjectionEvent{
			Kind:      eventKindInt(ev),
			DTag:      tagValueLocal(ev.Tags, "d"),
			Timestamp: ev.CreatedAt.Time().UTC(),
			SourceID:  eventIDHex(ev),
			Family:    ProjectionFamily(fmt.Sprintf("test-%d", testKindTier0Snapshot)),
		}, nil
	}
	bootstrapper := NewBootstrapper(pool, singleGroupCatalog, nil, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{
		SnapshotTimeout: time.Minute,
		CatchupTimeout:  time.Minute,
		PageLimit:       2,
	})

	err := bootstrapper.attemptBootstrap(context.Background())

	require.Error(t, err)
	require.False(t, bootstrapper.Ready())
	require.Contains(t, bootstrapper.Progress().LastError, "share created_at")
}

func TestBootstrapperPagesLiveCatchup(t *testing.T) {
	now := gonostr.Now()
	var live []gonostr.Event
	for i := 0; i < 5; i++ {
		// Newer than the attempt start and within the inbound future-skew
		// allowance.
		live = append(live, signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier1Live, fmt.Sprintf("live-%d", i), now+1+gonostr.Timestamp(i)))
	}
	snapshots := []gonostr.Event{
		signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier0Snapshot, "t0", now-10),
		signedBootstrapEventBy(t, testNostrPrivateKey, testKindTier1Snapshot, "t1", now-10),
	}
	relay := &bootstrapFakeRelay{url: "wss://live.example", store: append(append([]gonostr.Event(nil), snapshots...), live...)}
	pool := newBootstrapFakeRelayPool(t, relay)
	cache := &bootstrapSourceRecorder{}

	require.NoError(t, pagingTestBootstrapper(pool, 2, cache).attemptBootstrap(context.Background()))

	requireAllApplied(t, cache, snapshots, live)
	var livePages int
	for _, filter := range relay.recordedFilters() {
		if len(filter.Kinds) == 1 && int(filter.Kinds[0]) == testKindTier1Live {
			livePages++
			require.Equal(t, 2, filter.Limit)
			require.Zero(t, filter.Since, "an addressable live group is replayed in full, never from the attempt start")
		}
	}
	require.GreaterOrEqual(t, livePages, 3)
}

func TestKindCatalogEveryReplayGroupHasAnAuthorScope(t *testing.T) {
	for _, group := range NewKindCatalog().Groups {
		require.Truef(t, group.Authors.Valid(), "replay group %q has no valid author scope", group.Name)
		if group.Required {
			require.NotEqualf(t, ReplayAuthorsAny, group.Authors, "required replay group %q gates readiness and must be author-scoped", group.Name)
		}
	}
}
