package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgRelayPolicyProjectionRepository stores validated relay-policy heads in PostgreSQL.
type PgRelayPolicyProjectionRepository struct {
	pool pgQueryer
}

func NewPgRelayPolicyProjectionRepository(pool *pgxpool.Pool) *PgRelayPolicyProjectionRepository {
	return newPgRelayPolicyProjectionRepositoryWithDB(pool)
}

func newPgRelayPolicyProjectionRepositoryWithDB(db pgQueryer) *PgRelayPolicyProjectionRepository {
	return &PgRelayPolicyProjectionRepository{pool: db}
}

func (r *PgRelayPolicyProjectionRepository) Get(ctx context.Context, authorPubkey string) (*RelayPolicyProjection, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT author_pubkey, event_id, event_created_at, event_accepted_at, schema,
		       canonical_payload, payload_hash, source_relay, last_sync_at
		FROM relay_policy_projections
		WHERE author_pubkey = $1
	`, authorPubkey)

	projection := &RelayPolicyProjection{}
	if err := row.Scan(
		&projection.AuthorPubkey,
		&projection.EventID,
		&projection.EventCreatedAt,
		&projection.EventAcceptedAt,
		&projection.Schema,
		&projection.CanonicalPayload,
		&projection.PayloadHash,
		&projection.SourceRelay,
		&projection.LastSyncAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying relay policy projection: %w", err)
	}
	projection.CanonicalPayload = append([]byte(nil), projection.CanonicalPayload...)
	return projection, nil
}

func (r *PgRelayPolicyProjectionRepository) Promote(ctx context.Context, projection RelayPolicyProjection) (bool, error) {
	var promotedEventID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO relay_policy_projections (
			author_pubkey, event_id, event_created_at, event_accepted_at, schema,
			canonical_payload, payload_hash, source_relay, last_sync_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (author_pubkey) DO UPDATE SET
			event_id = EXCLUDED.event_id,
			event_created_at = EXCLUDED.event_created_at,
			event_accepted_at = EXCLUDED.event_accepted_at,
			schema = EXCLUDED.schema,
			canonical_payload = EXCLUDED.canonical_payload,
			payload_hash = EXCLUDED.payload_hash,
			source_relay = EXCLUDED.source_relay,
			last_sync_at = EXCLUDED.last_sync_at
		WHERE relay_policy_projections.event_created_at < EXCLUDED.event_created_at
		   OR (
				relay_policy_projections.event_created_at = EXCLUDED.event_created_at
				AND relay_policy_projections.event_id > EXCLUDED.event_id
		   )
		RETURNING event_id
	`,
		projection.AuthorPubkey,
		projection.EventID,
		projection.EventCreatedAt.UTC(),
		projection.EventAcceptedAt.UTC(),
		projection.Schema,
		[]byte(projection.CanonicalPayload),
		projection.PayloadHash,
		projection.SourceRelay,
		projection.LastSyncAt.UTC(),
	).Scan(&promotedEventID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("promoting relay policy projection: %w", err)
	}
	return promotedEventID == projection.EventID, nil
}

func (r *PgRelayPolicyProjectionRepository) MarkSynced(ctx context.Context, authorPubkey string, syncedAt time.Time) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE relay_policy_projections
		SET last_sync_at = GREATEST(last_sync_at, $2)
		WHERE author_pubkey = $1
	`, authorPubkey, syncedAt.UTC()); err != nil {
		return fmt.Errorf("marking relay policy projection synchronized: %w", err)
	}
	return nil
}
