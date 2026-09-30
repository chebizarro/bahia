package repository_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The PostgreSQL outbox partitions pending rows by publish target and moves
// abandoned rows to failed, matching the in-memory repository.
func TestPgNostrEventOutboxTargetsAndFailedState(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL outbox target test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	_, err = pool.Exec(ctx, `TRUNCATE nostr_events, nostr_event_archive_batches CASCADE`)
	require.NoError(t, err)

	repos := map[string]repository.NostrEventOutboxRepository{
		"postgres":  repository.NewPgNostrEventRepository(pool),
		"in-memory": repository.NewInMemoryNostrEventRepository(),
	}
	for name, repo := range repos {
		t.Run(name, func(t *testing.T) {
			base := time.Now().UTC().Truncate(time.Second)
			prefix := name + "-"
			for i, row := range []struct {
				id, target string
			}{
				{"interop-1", repository.NostrPublishTargetDefault},
				{"cp-1", repository.NostrPublishTargetControlPlane},
				{"interop-2", repository.NostrPublishTargetDefault},
				{"cp-2", repository.NostrPublishTargetControlPlane},
			} {
				_, err := repo.Record(ctx, &repository.NostrEventRecord{
					ID: prefix + row.id, Kind: 30078, PubKey: "pk", Content: "{}", Sig: "sig",
					CreatedAt: base, ReceivedAt: base.Add(time.Duration(i) * time.Second),
					PublishState: repository.NostrPublishStatePending, PublishTarget: row.target,
				})
				require.NoError(t, err)
			}

			cp, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetControlPlane, nil, 1)
			require.NoError(t, err)
			require.Len(t, cp, 1)
			require.Equal(t, prefix+"cp-1", cp[0].ID)
			require.Equal(t, repository.NostrPublishTargetControlPlane, cp[0].PublishTarget)
			cp, err = repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetControlPlane, &repository.NostrOutboxCursor{ReceivedAt: cp[0].ReceivedAt, ID: cp[0].ID}, 10)
			require.NoError(t, err)
			require.Len(t, cp, 1)
			require.Equal(t, prefix+"cp-2", cp[0].ID)

			interop, err := repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetDefault, nil, 10)
			require.NoError(t, err)
			ids := make([]string, 0, len(interop))
			for _, rec := range interop {
				ids = append(ids, rec.ID)
			}
			require.Equal(t, []string{prefix + "interop-1", prefix + "interop-2"}, ids)

			require.NoError(t, repo.AbandonPublish(ctx, prefix+"cp-1", "abandoned: blocked: no"))
			rec, err := repo.GetByID(ctx, prefix+"cp-1")
			require.NoError(t, err)
			require.Equal(t, repository.NostrPublishStateFailed, rec.PublishState)
			require.Equal(t, "abandoned: blocked: no", rec.LastPublishError)
			require.Equal(t, 1, rec.PublishAttempts)

			require.NoError(t, repo.MarkPublished(ctx, prefix+"cp-2", time.Now().UTC()))
			require.NoError(t, repo.AbandonPublish(ctx, prefix+"cp-2", "abandoned: late"))
			rec, err = repo.GetByID(ctx, prefix+"cp-2")
			require.NoError(t, err)
			require.Equal(t, repository.NostrPublishStatePublished, rec.PublishState, "published rows are never moved to failed")

			cp, err = repo.ListUnpublishedAfter(ctx, repository.NostrPublishTargetControlPlane, nil, 10)
			require.NoError(t, err)
			require.Empty(t, cp)
		})
	}
}

// The failed-row count refuses to scan nostr_events until ensure-indexes has
// built its partial index, then counts through it. The same ensure-indexes run
// validates the 000071 publish-state check that startup added NOT VALID.
func TestPgNostrEventCountPublishFailedAfterEnsureIndexes(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL failed-row count test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	_, err = pool.Exec(ctx, `TRUNCATE nostr_events, nostr_event_archive_batches CASCADE`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DROP INDEX IF EXISTS idx_nostr_events_publish_failed`)
	require.NoError(t, err)

	repo := repository.NewPgNostrEventRepository(pool)
	now := time.Now().UTC()
	for _, row := range []struct{ id, state string }{
		{"failed-1", repository.NostrPublishStatePending},
		{"failed-2", repository.NostrPublishStatePending},
		{"pending-1", repository.NostrPublishStatePending},
	} {
		_, err := repo.Record(ctx, &repository.NostrEventRecord{ID: row.id, Kind: 30900, PubKey: "pub", Content: "{}", Sig: "sig", CreatedAt: now, PublishState: row.state, PublishTarget: repository.NostrPublishTargetControlPlane})
		require.NoError(t, err)
	}
	require.NoError(t, repo.AbandonPublish(ctx, "failed-1", "abandoned: test"))
	require.NoError(t, repo.AbandonPublish(ctx, "failed-2", "abandoned: test"))

	_, err = repo.CountPublishFailed(ctx)
	require.ErrorIs(t, err, repository.ErrNostrPublishFailedIndexNotReady)

	require.NoError(t, repository.NewPgNostrEventArchiveRepository(pool).EnsureOnlineIndexes(ctx))
	failed, err := repo.CountPublishFailed(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), failed)
	depth, err := repo.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), depth)

	var validated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'nostr_events_publish_state_check'`).Scan(&validated))
	require.True(t, validated, "ensure-indexes validates the 000071 publish-state check")
}
