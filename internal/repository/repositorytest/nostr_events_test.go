package repositorytest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/repository"

	"github.com/stretchr/testify/require"
)

func TestInMemoryNostrEventRepositoryRecordGetByIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	rec := &repository.NostrEventRecord{
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

	got, err := repo.GetByID(ctx, rec.ID)
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
	rec := &repository.NostrEventRecord{ID: "event-1", Kind: 5101, CreatedAt: time.Unix(100, 0).UTC()}

	inserted, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	require.True(t, inserted)

	inserted, err = repo.Record(ctx, rec)
	require.NoError(t, err)
	require.False(t, inserted)

	records, err := repo.ListByKind(ctx, 5101, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
}

// Like PostgreSQL, archive rows of the local outbox are neither drained nor
// counted as outbox depth or failures.
func TestInMemoryNostrEventRepositoryIgnoresLocalOutboxArchiveRows(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	archive := repository.LocalOutboxArchiveTarget(repository.NostrPublishTargetControlPlane)
	recordEvents(t, ctx, repo,
		&repository.NostrEventRecord{ID: "drained", Kind: 4903, PublishState: repository.NostrPublishStatePending, PublishTarget: repository.NostrPublishTargetControlPlane, CreatedAt: time.Unix(101, 0).UTC()},
		&repository.NostrEventRecord{ID: "archived", Kind: 4903, PublishState: repository.NostrPublishStatePending, PublishTarget: archive, CreatedAt: time.Unix(102, 0).UTC()},
		&repository.NostrEventRecord{ID: "archived-failed", Kind: 4903, PublishState: repository.NostrPublishStateFailed, PublishTarget: archive, CreatedAt: time.Unix(103, 0).UTC()},
	)

	pending, err := repo.ListUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "drained", pending[0].ID)
	depth, err := repo.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), depth)
	failed, err := repo.CountPublishFailed(ctx)
	require.NoError(t, err)
	require.Zero(t, failed)
}

func TestInMemoryNostrEventRepositoryListByKindsAndFindByTag(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	recordEvents(t, ctx, repo,
		&repository.NostrEventRecord{ID: "b", Kind: 5961, Tags: []byte(`[["migrated-from","legacy-1"]]`), CreatedAt: time.Unix(102, 0).UTC()},
		&repository.NostrEventRecord{ID: "a", Kind: 5962, Tags: []byte(`[["migrated-from","legacy-2"]]`), CreatedAt: time.Unix(101, 0).UTC()},
		&repository.NostrEventRecord{ID: "c", Kind: 7000, Tags: []byte(`[["migrated-from","legacy-1"]]`), CreatedAt: time.Unix(103, 0).UTC()},
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
			inserted, err := repo.Record(ctx, &repository.NostrEventRecord{
				ID:        fmt.Sprintf("event-%d", i),
				Kind:      5101,
				CreatedAt: time.Unix(int64(i), 0).UTC(),
			})
			require.NoError(t, err)
			require.True(t, inserted)
		}()
	}
	wg.Wait()

	records, err := repo.ListByKind(ctx, 5101, count)
	require.NoError(t, err)
	require.Len(t, records, count)
}

func recordEvents(t *testing.T, ctx context.Context, repo *InMemoryNostrEventRepository, records ...*repository.NostrEventRecord) {
	t.Helper()
	for _, rec := range records {
		inserted, err := repo.Record(ctx, rec)
		require.NoError(t, err)
		require.True(t, inserted)
	}
}

func TestNostrArchiveLatestQueriesUseLowestIDOnTie(t *testing.T) {
	repo := NewInMemoryNostrEventRepository()
	for _, id := range []string{"ffff", "1111"} {
		_, err := repo.Record(t.Context(), &repository.NostrEventRecord{ID: id, Kind: 30900, PubKey: "author", Tags: []byte(`[["d","state"]]`), CreatedAt: time.Unix(100, 0)})
		require.NoError(t, err)
	}
	latest, err := repo.FindLatestByKindPubkeyDTag(t.Context(), 30900, "author", "state", "")
	require.NoError(t, err)
	require.Equal(t, "1111", latest.ID)
	listed, err := repo.ListByKind(t.Context(), 30900, 1)
	require.NoError(t, err)
	require.Equal(t, "1111", listed[0].ID)
	tagged, err := repo.FindByTag(t.Context(), "d", "state", []int{30900}, 1)
	require.NoError(t, err)
	require.Equal(t, "1111", tagged[0].ID)
}

func TestInMemoryNostrEventRepositoryListUnpublishedAfterPagesByKeyset(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	base := time.Unix(1000, 0).UTC()
	for i, id := range []string{"b", "a", "c"} {
		_, err := repo.Record(ctx, &repository.NostrEventRecord{ID: id, ReceivedAt: base.Add(time.Duration(i/2) * time.Second), PublishState: repository.NostrPublishStatePending})
		require.NoError(t, err)
	}
	_, err := repo.Record(ctx, &repository.NostrEventRecord{ID: "done", ReceivedAt: base, PublishState: repository.NostrPublishStatePublished})
	require.NoError(t, err)

	first, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetDefault, nil, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, nostrRecordIDs(first))

	last := first[len(first)-1]
	rest, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetDefault, &repository.NostrOutboxCursor{ReceivedAt: last.ReceivedAt, ID: last.ID}, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, nostrRecordIDs(rest))
}

func TestInMemoryNostrEventRepositoryListUnpublishedAfterIsPartitionedByTarget(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	base := time.Unix(1000, 0).UTC()
	rows := []repository.NostrEventRecord{
		{ID: "interop-1", ReceivedAt: base, PublishState: repository.NostrPublishStatePending},
		{ID: "cp-1", ReceivedAt: base.Add(time.Second), PublishState: repository.NostrPublishStatePending, PublishTarget: repository.NostrPublishTargetControlPlane},
		{ID: "interop-2", ReceivedAt: base.Add(2 * time.Second), PublishState: repository.NostrPublishStatePending},
		{ID: "cp-done", ReceivedAt: base, PublishState: repository.NostrPublishStatePublished, PublishTarget: repository.NostrPublishTargetControlPlane},
	}
	for i := range rows {
		_, err := repo.Record(ctx, &rows[i])
		require.NoError(t, err)
	}

	interop, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetDefault, nil, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"interop-1", "interop-2"}, nostrRecordIDs(interop))

	controlPlane, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetControlPlane, nil, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"cp-1"}, nostrRecordIDs(controlPlane))
	require.Equal(t, repository.NostrPublishTargetControlPlane, controlPlane[0].PublishTarget)

	all, err := repo.ListUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"interop-1", "cp-1", "interop-2"}, nostrRecordIDs(all), "ListUnpublished spans every target")
	depth, err := repo.CountUnpublished(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, depth)
}

func TestInMemoryNostrEventRepositoryAbandonPublishOnlyAffectsPendingRows(t *testing.T) {
	ctx := context.Background()
	repo := NewInMemoryNostrEventRepository()
	_, err := repo.Record(ctx, &repository.NostrEventRecord{ID: "pending", PublishState: repository.NostrPublishStatePending})
	require.NoError(t, err)
	_, err = repo.Record(ctx, &repository.NostrEventRecord{ID: "published", PublishState: repository.NostrPublishStatePending})
	require.NoError(t, err)
	require.NoError(t, repo.MarkPublished(ctx, "published", time.Now()))

	require.NoError(t, repo.AbandonPublish(ctx, "pending", "abandoned: blocked: no"))
	require.NoError(t, repo.AbandonPublish(ctx, "published", "abandoned: late"))

	pending, err := repo.GetByID(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStateFailed, pending.PublishState)
	require.Equal(t, "abandoned: blocked: no", pending.LastPublishError)
	require.Equal(t, 1, pending.PublishAttempts)

	published, err := repo.GetByID(ctx, "published")
	require.NoError(t, err)
	require.Equal(t, repository.NostrPublishStatePublished, published.PublishState)
	require.Empty(t, published.LastPublishError)

	depth, err := repo.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Zero(t, depth)
}

func nostrRecordIDs(records []repository.NostrEventRecord) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.ID)
	}
	return ids
}
