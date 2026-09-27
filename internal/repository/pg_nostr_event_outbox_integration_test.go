//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNostrOutboxExpiryPostgres(t *testing.T) {
	pool, _ := vmPostgres(t)
	repo := NewPgNostrEventRepository(pool)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := func(id string, enqueued time.Time) {
		_, err := repo.Record(ctx, &NostrEventRecord{ID: id, Kind: 1, PubKey: "pk", Sig: "sig", CreatedAt: enqueued.Add(-24 * time.Hour), ReceivedAt: enqueued, EntityType: "test", PublishState: NostrPublishStatePending})
		require.NoError(t, err)
	}
	record("stale", now.Add(-2*time.Hour))
	record("fresh", now.Add(-time.Minute))
	require.NoError(t, repo.RecordPublishFailure(ctx, "stale", "relay unavailable"))

	expired, err := repo.ExpireUnpublished(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), expired)
	again, err := repo.ExpireUnpublished(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Zero(t, again, "expiry happens once")

	stale, err := repo.GetByID(ctx, "stale")
	require.NoError(t, err)
	require.Equal(t, NostrPublishStateExpired, stale.PublishState)
	require.Equal(t, "relay unavailable; retry lifetime expired", stale.LastPublishError)

	// A late failure cannot resurrect it; a late success may still finalize it.
	require.NoError(t, repo.RecordPublishFailure(ctx, "stale", "late failure"))
	stale, err = repo.GetByID(ctx, "stale")
	require.NoError(t, err)
	require.Equal(t, NostrPublishStateExpired, stale.PublishState)
	require.NoError(t, repo.MarkPublished(ctx, "stale", now))
	stale, err = repo.GetByID(ctx, "stale")
	require.NoError(t, err)
	require.Equal(t, NostrPublishStatePublished, stale.PublishState)

	pending, err := repo.ListUnpublished(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "fresh", pending[0].ID)
}
