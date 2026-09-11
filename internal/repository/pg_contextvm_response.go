package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgContextVMResponseStore persists terminal ContextVM responses in PostgreSQL.
type PgContextVMResponseStore struct {
	pool pgQueryer
}

func NewPgContextVMResponseStore(pool *pgxpool.Pool) *PgContextVMResponseStore {
	return newPgContextVMResponseStoreWithDB(pool)
}

func newPgContextVMResponseStoreWithDB(db pgQueryer) *PgContextVMResponseStore {
	return &PgContextVMResponseStore{pool: db}
}

func (s *PgContextVMResponseStore) Put(ctx context.Context, record ContextVMResponseRecord) error {
	result, err := s.pool.Exec(ctx, `
		INSERT INTO contextvm_responses (requester_pubkey, method, progress_token, request_fingerprint, response, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (requester_pubkey, method, progress_token) DO UPDATE SET
			request_fingerprint = EXCLUDED.request_fingerprint,
			response = EXCLUDED.response,
			created_at = EXCLUDED.created_at
		WHERE contextvm_responses.request_fingerprint IS NULL
			OR contextvm_responses.request_fingerprint = EXCLUDED.request_fingerprint
	`, record.RequesterPubkey, record.Method, record.ProgressToken, nullableString(record.RequestFingerprint), record.Response, record.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("storing ContextVM response: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrContextVMResponseFingerprintConflict
	}
	return nil
}

func (s *PgContextVMResponseStore) Get(ctx context.Context, requesterPubkey, method, progressToken string, createdAfter time.Time) (*ContextVMResponseRecord, error) {
	record := &ContextVMResponseRecord{}
	err := s.pool.QueryRow(ctx, `
		SELECT requester_pubkey, method, progress_token, COALESCE(request_fingerprint, ''), response, created_at
		FROM contextvm_responses
		WHERE requester_pubkey = $1 AND method = $2 AND progress_token = $3 AND created_at >= $4
	`, requesterPubkey, method, progressToken, createdAfter.UTC()).Scan(
		&record.RequesterPubkey,
		&record.Method,
		&record.ProgressToken,
		&record.RequestFingerprint,
		&record.Response,
		&record.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("loading ContextVM response: %w", err)
	}
	record.Response = append([]byte(nil), record.Response...)
	record.CreatedAt = record.CreatedAt.UTC()
	return record, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *PgContextVMResponseStore) DeleteCreatedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.pool.Exec(ctx, `DELETE FROM contextvm_responses WHERE created_at < $1`, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("deleting expired ContextVM responses: %w", err)
	}
	return result.RowsAffected(), nil
}
