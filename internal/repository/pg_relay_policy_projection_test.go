package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestRelayPolicyProjectionReplacementPredicate(t *testing.T) {
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	confirmedAt := now
	current := RelayPolicyProjection{
		EventID:          strings.Repeat("b", 64),
		EventCreatedAt:   now,
		Schema:           "bahia.relay-settings.v1",
		CanonicalPayload: []byte(`{"schema":"bahia.relay-settings.v1"}`),
		PayloadHash:      strings.Repeat("c", 64),
		RelayConfirmedAt: &confirmedAt,
	}
	candidate := current
	candidate.RelayConfirmedAt = nil

	tests := []struct {
		name      string
		current   RelayPolicyProjection
		candidate RelayPolicyProjection
		want      bool
	}{
		{name: "newer wins", current: current, candidate: func() RelayPolicyProjection { p := candidate; p.EventCreatedAt = now.Add(time.Second); return p }(), want: true},
		{name: "older rejected", current: current, candidate: func() RelayPolicyProjection { p := candidate; p.EventCreatedAt = now.Add(-time.Second); return p }()},
		{name: "equal time lower event id wins", current: current, candidate: func() RelayPolicyProjection { p := candidate; p.EventID = strings.Repeat("a", 64); return p }(), want: true},
		{name: "equal time higher event id rejected", current: current, candidate: func() RelayPolicyProjection { p := candidate; p.EventID = strings.Repeat("d", 64); return p }()},
		{name: "same cached event becomes relay confirmed", current: func() RelayPolicyProjection { p := current; p.RelayConfirmedAt = nil; return p }(), candidate: candidate, want: true},
		{name: "same confirmed event rejected", current: current, candidate: candidate},
		{name: "same event with different payload rejected", current: func() RelayPolicyProjection { p := current; p.RelayConfirmedAt = nil; return p }(), candidate: func() RelayPolicyProjection {
			p := candidate
			p.CanonicalPayload = []byte(`{"schema":"different"}`)
			return p
		}()},
		{name: "restore cannot demote confirmed head", current: current, candidate: candidate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, RelayPolicyProjectionShouldReplace(tt.current, tt.candidate))
		})
	}
}

func TestRelayPolicyProjectionQueriesExecuteOrderingInvariant(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE relay_policy_projections (
			author_pubkey TEXT PRIMARY KEY,
			event_id TEXT NOT NULL,
			event_created_at TIMESTAMP NOT NULL,
			event_accepted_at TIMESTAMP NOT NULL,
			schema TEXT NOT NULL,
			canonical_payload BLOB NOT NULL,
			payload_hash TEXT NOT NULL,
			source_relay TEXT NOT NULL,
			last_sync_at TIMESTAMP NOT NULL,
			relay_confirmed_at TIMESTAMP NULL
		)
	`)
	require.NoError(t, err)

	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	projection := func(author, eventID string, createdAt time.Time) RelayPolicyProjection {
		return RelayPolicyProjection{
			AuthorPubkey: author, EventID: eventID, EventCreatedAt: createdAt, EventAcceptedAt: now,
			Schema: "bahia.relay-settings.v1", CanonicalPayload: []byte(`{"schema":"bahia.relay-settings.v1"}`),
			PayloadHash: strings.Repeat("c", 64), SourceRelay: "wss://relay.example", LastSyncAt: now,
		}
	}
	execute := func(query string, p RelayPolicyProjection) error {
		var eventID string
		return db.QueryRowContext(context.Background(), query,
			p.AuthorPubkey, p.EventID, p.EventCreatedAt.UTC(), p.EventAcceptedAt.UTC(),
			p.Schema, []byte(p.CanonicalPayload), p.PayloadHash, p.SourceRelay, p.LastSyncAt.UTC(),
		).Scan(&eventID)
	}

	orderedAuthor := strings.Repeat("1", 64)
	require.NoError(t, execute(promoteRelayPolicyProjectionSQL, projection(orderedAuthor, strings.Repeat("b", 64), now)))
	require.ErrorIs(t, execute(promoteRelayPolicyProjectionSQL, projection(orderedAuthor, strings.Repeat("a", 64), now.Add(-time.Second))), sql.ErrNoRows)
	require.ErrorIs(t, execute(promoteRelayPolicyProjectionSQL, projection(orderedAuthor, strings.Repeat("d", 64), now)), sql.ErrNoRows)
	require.NoError(t, execute(promoteRelayPolicyProjectionSQL, projection(orderedAuthor, strings.Repeat("a", 64), now)))
	require.NoError(t, execute(promoteRelayPolicyProjectionSQL, projection(orderedAuthor, strings.Repeat("f", 64), now.Add(time.Second))))

	cachedAuthor := strings.Repeat("2", 64)
	cached := projection(cachedAuthor, strings.Repeat("b", 64), now)
	require.NoError(t, execute(restoreCachedRelayPolicyProjectionSQL, cached))
	var confirmedAt sql.NullTime
	require.NoError(t, db.QueryRow("SELECT relay_confirmed_at FROM relay_policy_projections WHERE author_pubkey = $1", cachedAuthor).Scan(&confirmedAt))
	require.False(t, confirmedAt.Valid)
	require.NoError(t, execute(promoteRelayPolicyProjectionSQL, cached))
	require.NoError(t, db.QueryRow("SELECT relay_confirmed_at FROM relay_policy_projections WHERE author_pubkey = $1", cachedAuthor).Scan(&confirmedAt))
	require.True(t, confirmedAt.Valid)

	confirmedAuthor := strings.Repeat("3", 64)
	confirmed := projection(confirmedAuthor, strings.Repeat("b", 64), now)
	require.NoError(t, execute(promoteRelayPolicyProjectionSQL, confirmed))
	require.ErrorIs(t, execute(restoreCachedRelayPolicyProjectionSQL, confirmed), sql.ErrNoRows)
	confirmedAt = sql.NullTime{}
	require.NoError(t, db.QueryRow("SELECT relay_confirmed_at FROM relay_policy_projections WHERE author_pubkey = $1", confirmedAuthor).Scan(&confirmedAt))
	require.True(t, confirmedAt.Valid)
}

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

func TestPgRelayPolicyProjectionRestoreCachedPreservesProvenanceAndClearsConfirmation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	repo := newPgRelayPolicyProjectionRepositoryWithDB(mock)
	now := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
	payload := []byte(`{"schema":"bahia.relay-settings.v1"}`)
	sum := sha256.Sum256(payload)
	backup := RelayPolicyProjectionBackup{
		BackupSchema:     RelayPolicyProjectionBackupSchema,
		AuthorPubkey:     strings.Repeat("a", 64),
		EventID:          strings.Repeat("b", 64),
		EventCreatedAt:   now.Add(-time.Minute),
		EventAcceptedAt:  now,
		PolicySchema:     "bahia.relay-settings.v1",
		CanonicalPayload: payload,
		PayloadHash:      hex.EncodeToString(sum[:]),
		SourceRelay:      "wss://relay.example",
		LastSyncAt:       now,
		ExportedAt:       now.Add(time.Minute),
	}
	mock.ExpectQuery("INSERT INTO relay_policy_projections").
		WithArgs(backup.AuthorPubkey, backup.EventID, backup.EventCreatedAt, backup.EventAcceptedAt,
			backup.PolicySchema, []byte(backup.CanonicalPayload), backup.PayloadHash,
			backup.SourceRelay, backup.LastSyncAt).
		WillReturnRows(pgxmock.NewRows([]string{"event_id"}).AddRow(backup.EventID))

	restored, err := repo.RestoreCached(context.Background(), backup)
	require.NoError(t, err)
	require.True(t, restored)
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
			"canonical_payload", "payload_hash", "source_relay", "last_sync_at", "relay_confirmed_at",
		}).AddRow(author, eventID, now.Add(-time.Minute), now, "bahia.relay-settings.v1", payload, hash, "wss://relay.example", now, now))

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
