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
