package service_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const lifecycleTestKey = "3333333333333333333333333333333333333333333333333333333333333333"

const lifecycleServiceID = "6f1c2f55-2d59-4d8c-9a55-4a7f0f2a8f10"

var lifecycleClock = time.Unix(1_800_000_000, 0).UTC()

type recordedProjection struct {
	sourceID  string
	name      string
	tombstone bool
}

// newLifecycleCache returns a cache whose service applier records what it is
// asked to apply.
func newLifecycleCache(t *testing.T) (*service.RelayProjectionCache, chan recordedProjection) {
	t.Helper()
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	service.SetRelayProjectionCacheClock(cache, func() time.Time { return lifecycleClock })
	applied := make(chan recordedProjection, 16)
	cache.RegisterApplier(nostr.FamilyService, func(_ context.Context, event any) error {
		decoded := event.(*nostr.DecodedProjectionEvent)
		applied <- recordedProjection{sourceID: decoded.SourceID, name: decoded.Service.Name, tombstone: decoded.Tombstone}
		return nil
	})
	return cache, applied
}

func signedLifecycleEvent(t *testing.T, kind int, createdAt time.Time, content string, tags ...gonostr.Tag) *gonostr.Event {
	t.Helper()
	ev := &gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Timestamp(createdAt.Unix()), Content: content, Tags: gonostr.Tags(tags)}
	require.NoError(t, nostrutil.SignEventWithHexKey(ev, lifecycleTestKey))
	return ev
}

func serviceRegistryEvent(t *testing.T, createdAt time.Time, name string, extra ...gonostr.Tag) *gonostr.Event {
	t.Helper()
	tags := append([]gonostr.Tag{{"d", lifecycleServiceID}}, extra...)
	return signedLifecycleEvent(t, nostr.KindServiceRegistry, createdAt, `{"id":"`+lifecycleServiceID+`","name":"`+name+`"}`, tags...)
}

func decodeForCache(t *testing.T, ev *gonostr.Event) *nostr.DecodedProjectionEvent {
	t.Helper()
	decoder, ok := nostr.NewKindCatalog().Decoder(int(ev.Kind))
	require.True(t, ok, "kind %d has a decoder", ev.Kind)
	decoded, err := decoder(ev)
	require.NoError(t, err)
	require.Same(t, ev, decoded.SourceEvent())
	return decoded
}

func drainApplied(applied chan recordedProjection) []recordedProjection {
	var out []recordedProjection
	for {
		select {
		case record := <-applied:
			out = append(out, record)
		default:
			return out
		}
	}
}

func TestRelayProjectionCacheLowestIDWinsEqualCreatedAtInEitherOrder(t *testing.T) {
	at := lifecycleClock.Add(-time.Minute)
	a := serviceRegistryEvent(t, at, "a")
	b := serviceRegistryEvent(t, at, "b")
	low, high := a, b
	if low.ID.Hex() > high.ID.Hex() {
		low, high = high, low
	}

	for _, order := range [][]*gonostr.Event{{low, high}, {high, low}} {
		cache, applied := newLifecycleCache(t)
		for _, ev := range order {
			require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, ev)))
		}
		records := drainApplied(applied)
		require.NotEmpty(t, records)
		require.Equal(t, low.ID.Hex(), records[len(records)-1].sourceID, "the lowest id must be the applied state")
	}
}

func TestRelayProjectionCacheOlderVersionDoesNotOverwriteNewer(t *testing.T) {
	cache, applied := newLifecycleCache(t)
	newer := serviceRegistryEvent(t, lifecycleClock.Add(-time.Minute), "new")
	older := serviceRegistryEvent(t, lifecycleClock.Add(-time.Hour), "old")
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, newer)))
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, older)))
	require.Equal(t, []recordedProjection{{sourceID: newer.ID.Hex(), name: "new"}}, drainApplied(applied))
}

func TestRelayProjectionCacheHonoursEDeletionWithoutResurrection(t *testing.T) {
	cache, applied := newLifecycleCache(t)
	target := serviceRegistryEvent(t, lifecycleClock.Add(-time.Hour), "svc")
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, target)))
	deletion := signedLifecycleEvent(t, int(gonostr.KindDeletion), lifecycleClock.Add(-time.Minute), "", gonostr.Tag{"e", target.ID.Hex()})
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, deletion)))
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, target)))

	require.Equal(t, []recordedProjection{
		{sourceID: target.ID.Hex(), name: "svc"},
		{sourceID: target.ID.Hex(), name: "svc", tombstone: true},
	}, drainApplied(applied), "the deletion tombstones the entity and a redelivery must not resurrect it")
}

func TestRelayProjectionCacheHonoursADeletionWithoutResurrection(t *testing.T) {
	cache, applied := newLifecycleCache(t)
	older := serviceRegistryEvent(t, lifecycleClock.Add(-2*time.Hour), "v1")
	current := serviceRegistryEvent(t, lifecycleClock.Add(-time.Hour), "v2")
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, current)))
	address := strconv.Itoa(nostr.KindServiceRegistry) + ":" + current.PubKey.Hex() + ":" + lifecycleServiceID
	deletion := signedLifecycleEvent(t, int(gonostr.KindDeletion), lifecycleClock.Add(-time.Minute), "", gonostr.Tag{"a", address})
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, deletion)))
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, older)))
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, current)))

	require.Equal(t, []recordedProjection{
		{sourceID: current.ID.Hex(), name: "v2"},
		{sourceID: current.ID.Hex(), name: "v2", tombstone: true},
	}, drainApplied(applied))

	newer := serviceRegistryEvent(t, lifecycleClock, "v3")
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, newer)))
	require.Equal(t, []recordedProjection{{sourceID: newer.ID.Hex(), name: "v3"}}, drainApplied(applied), "a version after the deletion is new state")
}

func TestRelayProjectionCacheHonoursExpiration(t *testing.T) {
	cache, applied := newLifecycleCache(t)
	expired := serviceRegistryEvent(t, lifecycleClock.Add(-time.Hour), "expired", gonostr.Tag{"expiration", strconv.FormatInt(lifecycleClock.Unix(), 10)})
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, expired)))
	require.Empty(t, drainApplied(applied), "an expired event must be ignored")

	expiring := serviceRegistryEvent(t, lifecycleClock.Add(-time.Minute), "live", gonostr.Tag{"expiration", strconv.FormatInt(lifecycleClock.Add(time.Minute).Unix(), 10)})
	require.NoError(t, cache.Apply(context.Background(), decodeForCache(t, expiring)))
	require.Equal(t, recordedProjection{sourceID: expiring.ID.Hex(), name: "live"}, <-applied)

	// Past the expiration, Run's timer fires at once and tombstones it.
	service.SetRelayProjectionCacheClock(cache, func() time.Time { return lifecycleClock.Add(time.Minute) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cache.Run(ctx) }()
	require.Equal(t, recordedProjection{sourceID: expiring.ID.Hex(), name: "live", tombstone: true}, <-applied)
	cancel()
	require.NoError(t, <-done)
}
