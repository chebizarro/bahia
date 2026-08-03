package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgRelayPolicyProjectionPromoteUsesAtomicReplaceableOrdering(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgRelayPolicyProjectionRepositoryWithDB(mock)
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	projection := RelayPolicyProjection{
		AuthorPubkey:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		EventID:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		EventCreatedAt: now.Add(-time.Minute), EventAcceptedAt: now,
		Schema: "bahia.relay-settings.v1", CanonicalPayload: []byte(`{"schema":"bahia.relay-settings.v1"}`),
		PayloadHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		SourceRelay: "wss://secondary.example", LastSyncAt: now,
	}

	mock.ExpectQuery("INSERT INTO relay_policy_projections").
		WithArgs(
			projection.AuthorPubkey, projection.EventID, projection.EventCreatedAt,
			projection.EventAcceptedAt, projection.Schema, []byte(projection.CanonicalPayload),
			projection.PayloadHash, projection.SourceRelay, projection.LastSyncAt,
		).
		WillReturnRows(pgxmock.NewRows([]string{"event_id"}).AddRow(projection.EventID))
	promoted, err := repo.Promote(context.Background(), projection)
	require.NoError(t, err)
	require.True(t, promoted)

	mock.ExpectQuery("INSERT INTO relay_policy_projections").
		WithArgs(
			projection.AuthorPubkey, projection.EventID, projection.EventCreatedAt,
			projection.EventAcceptedAt, projection.Schema, []byte(projection.CanonicalPayload),
			projection.PayloadHash, projection.SourceRelay, projection.LastSyncAt,
		).
		WillReturnRows(pgxmock.NewRows([]string{"event_id"}))
	promoted, err = repo.Promote(context.Background(), projection)
	require.NoError(t, err)
	require.False(t, promoted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgRelayPolicyProjectionGetAndMarkSynced(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgRelayPolicyProjectionRepositoryWithDB(mock)
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	author := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	eventID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	payload := []byte(`{"schema":"bahia.relay-settings.v1"}`)
	hash := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	mock.ExpectQuery(regexp.QuoteMeta("FROM relay_policy_projections")).
		WithArgs(author).
		WillReturnRows(pgxmock.NewRows([]string{
			"author_pubkey", "event_id", "event_created_at", "event_accepted_at", "schema",
			"canonical_payload", "payload_hash", "source_relay", "last_sync_at",
		}).AddRow(author, eventID, now.Add(-time.Minute), now, "bahia.relay-settings.v1", payload, hash, "wss://relay.example", now))

	projection, err := repo.Get(context.Background(), author)
	require.NoError(t, err)
	require.Equal(t, eventID, projection.EventID)
	require.Equal(t, payload, []byte(projection.CanonicalPayload))

	syncedAt := now.Add(time.Minute)
	mock.ExpectExec("UPDATE relay_policy_projections").
		WithArgs(author, syncedAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, repo.MarkSynced(context.Background(), author, syncedAt))
	require.NoError(t, mock.ExpectationsWereMet())
}
