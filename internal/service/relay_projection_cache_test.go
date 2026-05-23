package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type relayProjectionMetaMemoryRepo struct {
	mu    sync.Mutex
	metas map[string]repository.RelayProjectionMeta
}

func newRelayProjectionMetaMemoryRepo() *relayProjectionMetaMemoryRepo {
	return &relayProjectionMetaMemoryRepo{metas: make(map[string]repository.RelayProjectionMeta)}
}

func (r *relayProjectionMetaMemoryRepo) Get(_ context.Context, stream, entityKey string) (*repository.RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, ok := r.metas[stream+"\x00"+entityKey]
	if !ok {
		return nil, nil
	}
	return &meta, nil
}

func (r *relayProjectionMetaMemoryRepo) Upsert(_ context.Context, meta repository.RelayProjectionMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := meta.Stream + "\x00" + meta.EntityKey
	existing, ok := r.metas[key]
	if ok && !meta.UpdatedAt.After(existing.UpdatedAt) {
		return nil
	}
	r.metas[key] = meta
	return nil
}

func (r *relayProjectionMetaMemoryRepo) ListByStream(_ context.Context, stream string) ([]repository.RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	metas := make([]repository.RelayProjectionMeta, 0)
	for _, meta := range r.metas {
		if meta.Stream == stream {
			metas = append(metas, meta)
		}
	}
	return metas, nil
}

func TestRelayProjectionCacheApplyNoPriorMetaAppliesAndStoresMeta(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-1")
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected applier to run once, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-1", false)
}

func TestRelayProjectionCacheApplyOlderThanExistingSkips(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	newer := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, repository.RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: newer, SourceEventID: "evt-newer"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, testProjectionEvent("svc-a", newer.Add(-time.Minute), "evt-old")); err != nil {
		t.Fatalf("apply older: %v", err)
	}
	if applied != 0 {
		t.Fatalf("expected stale event to skip applier, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-newer", false)
}

func TestRelayProjectionCacheApplyNewerThanExistingAppliesAndUpdatesMeta(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	older := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, repository.RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: older, SourceEventID: "evt-old"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, testProjectionEvent("svc-a", older.Add(time.Minute), "evt-new")); err != nil {
		t.Fatalf("apply newer: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected newer event to apply once, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-new", false)
}

func TestRelayProjectionCacheApplyTombstone(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-delete")
	event.Tombstone = true
	seenTombstone := false
	cache.RegisterApplier(nostr.FamilyService, func(_ context.Context, applied any) error {
		seenTombstone = applied.(*nostr.DecodedProjectionEvent).Tombstone
		return nil
	})

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply tombstone: %v", err)
	}
	if !seenTombstone {
		t.Fatal("expected applier to receive tombstone event")
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-delete", true)
}

func TestRelayProjectionCacheApplyNoRegisteredApplierStoresMetaOnly(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-1")

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply without applier: %v", err)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-1", false)
}

func testProjectionEvent(dTag string, timestamp time.Time, sourceID string) *nostr.DecodedProjectionEvent {
	return &nostr.DecodedProjectionEvent{
		Kind:      nostr.KindServiceRegistry,
		DTag:      dTag,
		Timestamp: timestamp,
		SourceID:  sourceID,
		Family:    nostr.FamilyService,
	}
}

func assertStoredMeta(t *testing.T, repo repository.RelayProjectionMetaRepository, stream, entityKey, sourceID string, tombstoned bool) {
	t.Helper()
	meta, err := repo.Get(context.Background(), stream, entityKey)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected meta for %s/%s", stream, entityKey)
	}
	if meta.SourceEventID != sourceID || meta.Tombstoned != tombstoned {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}
