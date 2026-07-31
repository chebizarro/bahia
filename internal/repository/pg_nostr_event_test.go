package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

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
			NostrPublishStateNotApplicable, rec.PublishAttempts, rec.LastPublishError, rec.PublishedAt).
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
			NostrPublishStateNotApplicable, rec.PublishAttempts, rec.LastPublishError, rec.PublishedAt).
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
