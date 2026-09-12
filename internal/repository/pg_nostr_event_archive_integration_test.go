package repository_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/nostrarchive"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNostrEventArchiveProductionShapedLifecycle(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL archive lifecycle test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	_, err = pool.Exec(ctx, `TRUNCATE nostr_events, nostr_event_archive_batches CASCADE`)
	require.NoError(t, err)
	old := time.Now().UTC().Add(-48 * time.Hour)
	newer := time.Now().UTC()
	for _, row := range []struct {
		id, state string
		at        time.Time
	}{
		{"old-a", repository.NostrPublishStateNotApplicable, old},
		{"old-b", repository.NostrPublishStatePublished, old.Add(time.Second)},
		{"old-c", repository.NostrPublishStateNotApplicable, old.Add(2 * time.Second)},
		{"pending", repository.NostrPublishStatePending, old.Add(3 * time.Second)},
		{"new", repository.NostrPublishStateNotApplicable, newer},
	} {
		_, err := pool.Exec(ctx, `INSERT INTO nostr_events (id,kind,pubkey,content,tags,sig,created_at,received_at,publish_state) VALUES ($1,25910,'pub','{}','[]','sig',$2,$2,$3)`, row.id, row.at, row.state)
		require.NoError(t, err)
	}

	repo := repository.NewPgNostrEventArchiveRepository(pool)
	require.NoError(t, repo.EnsureOnlineIndexes(ctx))
	batch, err := repo.ClaimArchiveBatch(ctx, time.Now().UTC().Add(-time.Hour), 2, []int{25910})
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, int64(2), batch.RowCount)
	_, _, err = repo.PruneArchiveBatch(ctx, batch.ID, 1)
	require.ErrorIs(t, err, repository.ErrNostrArchiveNotProtected)

	// Reconnect between claim and export to prove a process crash cannot lose
	// batch ownership or make rows prunable.
	restartedPool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer restartedPool.Close()
	restartedRepo := repository.NewPgNostrEventArchiveRepository(restartedPool)
	manager, err := nostrarchive.NewArtifactManager(restartedRepo, filepath.Join(t.TempDir(), "archive"))
	require.NoError(t, err)
	batch, err = manager.Export(ctx, batch.ID)
	require.NoError(t, err)
	require.Equal(t, repository.NostrArchiveStatusExported, batch.Status)
	require.Error(t, manager.ConfirmProtected(ctx, batch.ID, "file:///not-protected", "v1"))
	require.NoError(t, manager.ConfirmProtected(ctx, batch.ID, "kopia://bahia/nostr-events", "snapshot-1"))

	deleted, done, err := restartedRepo.PruneArchiveBatch(ctx, batch.ID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.False(t, done)
	deleted, done, err = restartedRepo.PruneArchiveBatch(ctx, batch.ID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	require.True(t, done)

	var pending, unclaimedOld, newRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM nostr_events WHERE id='pending' AND publish_state='pending'`).Scan(&pending))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM nostr_events WHERE id='old-c' AND archive_batch_id IS NULL`).Scan(&unclaimedOld))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM nostr_events WHERE id='new'`).Scan(&newRows))
	require.Equal(t, 1, pending)
	require.Equal(t, 1, unclaimedOld)
	require.Equal(t, 1, newRows)

	inserted, err := manager.Restore(ctx, batch.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), inserted)
	inserted, err = manager.Restore(ctx, batch.ID)
	require.NoError(t, err)
	require.Zero(t, inserted)

	outbox := repository.NewPgNostrEventRepository(pool)
	depth, err := outbox.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), depth)
}
