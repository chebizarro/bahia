package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type nip98ReplayDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// PGNIP98ReplayStore persists replay claims so all API replicas share them.
type PGNIP98ReplayStore struct{ db nip98ReplayDB }

func NewPGNIP98ReplayStore(pool *pgxpool.Pool) *PGNIP98ReplayStore {
	return &PGNIP98ReplayStore{db: pool}
}

func newPGNIP98ReplayStore(db nip98ReplayDB) *PGNIP98ReplayStore {
	return &PGNIP98ReplayStore{db: db}
}

func (s *PGNIP98ReplayStore) Claim(ctx context.Context, eventID string, expiresAt time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("NIP-98 replay database is not configured")
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM nip98_replay_claims WHERE expires_at <= NOW()`); err != nil {
		return false, fmt.Errorf("pruning NIP-98 replay claims: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO nip98_replay_claims (event_id, expires_at)
		VALUES ($1, $2)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID, expiresAt)
	if err != nil {
		return false, fmt.Errorf("inserting NIP-98 replay claim: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
