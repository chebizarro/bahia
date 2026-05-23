package repository

import (
	"context"
	"sync"
	"testing"
	"time"
)

type InMemoryRelayProjectionMetaRepository struct {
	mu    sync.Mutex
	metas map[string]RelayProjectionMeta
}

func NewInMemoryRelayProjectionMetaRepository() *InMemoryRelayProjectionMetaRepository {
	return &InMemoryRelayProjectionMetaRepository{metas: make(map[string]RelayProjectionMeta)}
}

func (r *InMemoryRelayProjectionMetaRepository) Get(_ context.Context, stream, entityKey string) (*RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, ok := r.metas[metaKey(stream, entityKey)]
	if !ok {
		return nil, nil
	}
	return &meta, nil
}

func (r *InMemoryRelayProjectionMetaRepository) Upsert(_ context.Context, meta RelayProjectionMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := metaKey(meta.Stream, meta.EntityKey)
	existing, ok := r.metas[key]
	if ok && !meta.UpdatedAt.After(existing.UpdatedAt) {
		return nil
	}
	r.metas[key] = meta
	return nil
}

func (r *InMemoryRelayProjectionMetaRepository) ListByStream(_ context.Context, stream string) ([]RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	metas := make([]RelayProjectionMeta, 0)
	for _, meta := range r.metas {
		if meta.Stream == stream {
			metas = append(metas, meta)
		}
	}
	return metas, nil
}

func metaKey(stream, entityKey string) string {
	return stream + "\x00" + entityKey
}

func TestInMemoryRelayProjectionMetaRepositoryUpsertOrdersByUpdatedAt(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryRelayProjectionMetaRepository()
	base := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	if err := repo.Upsert(ctx, RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: base, SourceEventID: "evt-1"}); err != nil {
		t.Fatalf("upsert initial: %v", err)
	}
	if err := repo.Upsert(ctx, RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: base.Add(-time.Minute), SourceEventID: "evt-old"}); err != nil {
		t.Fatalf("upsert older: %v", err)
	}
	meta, err := repo.Get(ctx, "service", "svc-a")
	if err != nil {
		t.Fatalf("get after older upsert: %v", err)
	}
	if meta.SourceEventID != "evt-1" {
		t.Fatalf("older upsert replaced meta: got %q", meta.SourceEventID)
	}

	if err := repo.Upsert(ctx, RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: base.Add(time.Minute), SourceEventID: "evt-2", Tombstoned: true}); err != nil {
		t.Fatalf("upsert newer: %v", err)
	}
	meta, err = repo.Get(ctx, "service", "svc-a")
	if err != nil {
		t.Fatalf("get after newer upsert: %v", err)
	}
	if meta.SourceEventID != "evt-2" || !meta.Tombstoned {
		t.Fatalf("newer upsert did not replace meta: %+v", meta)
	}
}

func TestInMemoryRelayProjectionMetaRepositoryListByStream(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryRelayProjectionMetaRepository()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	entries := []RelayProjectionMeta{
		{Stream: "service", EntityKey: "svc-a", UpdatedAt: now, SourceEventID: "evt-1"},
		{Stream: "service", EntityKey: "svc-b", UpdatedAt: now, SourceEventID: "evt-2"},
		{Stream: "environment", EntityKey: "env-a", UpdatedAt: now, SourceEventID: "evt-3"},
	}
	for _, entry := range entries {
		if err := repo.Upsert(ctx, entry); err != nil {
			t.Fatalf("upsert %s/%s: %v", entry.Stream, entry.EntityKey, err)
		}
	}

	metas, err := repo.ListByStream(ctx, "service")
	if err != nil {
		t.Fatalf("list by stream: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("expected 2 service metas, got %d", len(metas))
	}
}
