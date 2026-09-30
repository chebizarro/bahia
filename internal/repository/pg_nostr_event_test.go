package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/require"
)

func TestPgNostrEventRepositoryCountUnpublished(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM nostr_events WHERE publish_state = \$1`).
		WithArgs(NostrPublishStatePending).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(7)))

	count, err := repo.CountUnpublished(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(7), count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryRecordReportsInserted(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	rec := &NostrEventRecord{
		ID:         "event-1",
		Kind:       5101,
		PubKey:     "pubkey",
		Content:    "{}",
		Tags:       json.RawMessage("[]"),
		Sig:        "sig",
		CreatedAt:  time.Unix(100, 0).UTC(),
		ReceivedAt: time.Unix(101, 0).UTC(),
	}

	mock.ExpectExec("INSERT INTO nostr_events").
		WithArgs(rec.ID, rec.Kind, rec.PubKey, rec.Content, rec.Tags, rec.Sig, rec.CreatedAt, rec.ReceivedAt, rec.EntityType, rec.EntityID,
			NostrPublishStateNotApplicable, rec.PublishAttempts, rec.LastPublishError, rec.PublishedAt, NostrPublishTargetDefault).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	inserted, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryRecordReportsDuplicate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	rec := &NostrEventRecord{
		ID:        "event-1",
		Kind:      5101,
		PubKey:    "pubkey",
		Content:   "{}",
		Tags:      json.RawMessage("[]"),
		Sig:       "sig",
		CreatedAt: time.Unix(100, 0).UTC(),
	}

	mock.ExpectExec("INSERT INTO nostr_events").
		WithArgs(rec.ID, rec.Kind, rec.PubKey, rec.Content, rec.Tags, pgxmock.AnyArg(), rec.CreatedAt, pgxmock.AnyArg(), rec.EntityType, rec.EntityID,
			NostrPublishStateNotApplicable, rec.PublishAttempts, rec.LastPublishError, rec.PublishedAt, NostrPublishTargetDefault).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 0"))

	inserted, err := repo.Record(ctx, rec)
	require.NoError(t, err)
	require.False(t, inserted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryLatestCreatedAtForKinds(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	latest := time.Unix(200, 0).UTC()
	mock.ExpectQuery("SELECT MAX\\(created_at\\) FROM nostr_events WHERE kind = ANY").
		WithArgs([]int{5101, 5961}).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(latest))

	got, err := repo.LatestCreatedAtForKinds(ctx, []int{5101, 5961})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, latest.Equal(*got))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryLatestCreatedAtForKindsAndAuthors(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	latest := time.Unix(300, 0).UTC()
	mock.ExpectQuery("SELECT MAX\\(created_at\\) FROM nostr_events WHERE kind = ANY\\(\\$1\\) AND pubkey = ANY").
		WithArgs([]int{5961}, []string{"operator"}).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(latest))

	got, err := repo.LatestCreatedAtForKindsAndAuthors(ctx, []int{5961}, []string{"operator"})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, latest.Equal(*got))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryListUnpublishedAfterUsesKeysetCursor(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	cursor := &NostrOutboxCursor{ReceivedAt: time.Unix(100, 0).UTC(), ID: "event-9"}
	mock.ExpectQuery(`WHERE publish_state = \$1 AND publish_target = \$2 AND \(received_at, id\) > \(\$3, \$4\) ORDER BY received_at ASC, id ASC LIMIT \$5`).
		WithArgs(NostrPublishStatePending, NostrPublishTargetControlPlane, cursor.ReceivedAt, cursor.ID, 25).
		WillReturnRows(pgxmock.NewRows([]string{"id", "kind", "pubkey", "content", "tags", "sig", "created_at", "received_at", "entity_type", "entity_id", "publish_state", "publish_attempts", "last_publish_error", "published_at", "publish_target"}))

	records, err := repo.ListUnpublishedAfter(ctx, NostrPublishTargetControlPlane, cursor, 25)
	require.NoError(t, err)
	require.Empty(t, records)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgNostrEventRepositoryAbandonPublishOnlyUpdatesPendingRows(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgNostrEventRepositoryWithDB(mock)
	mock.ExpectExec(`UPDATE nostr_events\s+SET publish_state = \$2, publish_attempts = publish_attempts \+ 1,\s+last_publish_error = \$3\s+WHERE id = \$1 AND publish_state = \$4`).
		WithArgs("event-1", NostrPublishStateFailed, "abandoned: blocked: no", NostrPublishStatePending).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	require.NoError(t, repo.AbandonPublish(ctx, "event-1", "abandoned: blocked: no"))
	require.NoError(t, mock.ExpectationsWereMet())
}
