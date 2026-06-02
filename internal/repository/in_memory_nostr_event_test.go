package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInMemoryNostrEventRepositoryRecordFindByIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	rec := &NostrEventRecord{
		ID:        "event-1",
		Kind:      5101,
		PubKey:    "pubkey",
		Content:   "{}",
		Sig:       "sig",
		CreatedAt: time.Unix(100, 0).UTC(),
	}

	inserted, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	require.True(t, inserted)

	got, err := repo.FindByID(ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, rec.ID, got.ID)
	require.Equal(t, rec.Kind, got.Kind)
	require.Equal(t, rec.PubKey, got.PubKey)
	require.Equal(t, rec.Content, got.Content)
	require.Equal(t, rec.Sig, got.Sig)
	require.True(t, rec.CreatedAt.Equal(got.CreatedAt))
}

func TestInMemoryNostrEventRepositoryRecordIdempotency(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	rec := &NostrEventRecord{ID: "event-1", Kind: 5101, CreatedAt: time.Unix(100, 0).UTC()}

	inserted, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	require.True(t, inserted)

	inserted, err = repo.Record(ctx, rec)
	require.NoError(t, err)
	require.False(t, inserted)

	records, err := repo.FindSince(ctx, time.Unix(0, 0).UTC(), nil)
	require.NoError(t, err)
	require.Len(t, records, 1)
}

func TestInMemoryNostrEventRepositoryFindSinceTimestampFiltering(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	recordEvents(t, ctx, repo,
		&NostrEventRecord{ID: "before", Kind: 5101, CreatedAt: time.Unix(99, 0).UTC()},
		&NostrEventRecord{ID: "at", Kind: 5101, CreatedAt: time.Unix(100, 0).UTC()},
		&NostrEventRecord{ID: "after", Kind: 5101, CreatedAt: time.Unix(101, 0).UTC()},
	)

	records, err := repo.FindSince(ctx, time.Unix(100, 0).UTC(), nil)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "after", records[0].ID)
}

func TestInMemoryNostrEventRepositoryFindSinceKindFiltering(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	recordEvents(t, ctx, repo,
		&NostrEventRecord{ID: "kind-1", Kind: 5101, CreatedAt: time.Unix(101, 0).UTC()},
		&NostrEventRecord{ID: "kind-2", Kind: 5961, CreatedAt: time.Unix(102, 0).UTC()},
		&NostrEventRecord{ID: "kind-3", Kind: 5962, CreatedAt: time.Unix(103, 0).UTC()},
	)

	records, err := repo.FindSince(ctx, time.Unix(100, 0).UTC(), []int{5961, 5962})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "kind-2", records[0].ID)
	require.Equal(t, "kind-3", records[1].ID)
}

func TestInMemoryNostrEventRepositoryListByKindsAndFindByTag(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	recordEvents(t, ctx, repo,
		&NostrEventRecord{ID: "b", Kind: 5961, Tags: []byte(`[["migrated-from","legacy-1"]]`), CreatedAt: time.Unix(102, 0).UTC()},
		&NostrEventRecord{ID: "a", Kind: 5962, Tags: []byte(`[["migrated-from","legacy-2"]]`), CreatedAt: time.Unix(101, 0).UTC()},
		&NostrEventRecord{ID: "c", Kind: 7000, Tags: []byte(`[["migrated-from","legacy-1"]]`), CreatedAt: time.Unix(103, 0).UTC()},
	)

	listed, err := repo.ListByKinds(ctx, []int{5961, 5962}, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, []string{listed[0].ID, listed[1].ID})

	found, err := repo.FindByTag(ctx, "migrated-from", "legacy-1", []int{5961}, 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "b", found[0].ID)
}

func TestInMemoryNostrEventRepositoryConcurrentRecord(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()

	const count = 100
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			inserted, err := repo.Record(ctx, &NostrEventRecord{
				ID:        fmt.Sprintf("event-%d", i),
				Kind:      5101,
				CreatedAt: time.Unix(int64(i), 0).UTC(),
			})
			require.NoError(t, err)
			require.True(t, inserted)
		}()
	}
	wg.Wait()

	records, err := repo.FindSince(ctx, time.Unix(-1, 0).UTC(), []int{5101})
	require.NoError(t, err)
	require.Len(t, records, count)
}

func recordEvents(t *testing.T, ctx context.Context, repo *InMemoryNostrEventRepository, records ...*NostrEventRecord) {
	t.Helper()
	for _, rec := range records {
		inserted, err := repo.Record(ctx, rec)
		require.NoError(t, err)
		require.True(t, inserted)
	}
}
