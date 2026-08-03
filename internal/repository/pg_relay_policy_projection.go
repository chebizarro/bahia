package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
				canonical_payload, payload_hash, source_relay, last_sync_at, relay_confirmed_at
		FROM relay_policy_projections
		WHERE author_pubkey = $1
	`, authorPubkey)
	projection := &RelayPolicyProjection{}
	var relayConfirmedAt sql.NullTime
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
		&relayConfirmedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying relay policy projection: %w", err)
	}
	projection.CanonicalPayload = append([]byte(nil), projection.CanonicalPayload...)
	if relayConfirmedAt.Valid {
		confirmedAt := relayConfirmedAt.Time.UTC()
		projection.RelayConfirmedAt = &confirmedAt
	}
	return projection, nil
}

func (r *PgRelayPolicyProjectionRepository) Promote(ctx context.Context, projection RelayPolicyProjection) (bool, error) {
	var promotedEventID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO relay_policy_projections (
			author_pubkey, event_id, event_created_at, event_accepted_at, schema,
			canonical_payload, payload_hash, source_relay, last_sync_at, relay_confirmed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $4)
		ON CONFLICT (author_pubkey) DO UPDATE SET
			event_id = EXCLUDED.event_id,
			event_created_at = EXCLUDED.event_created_at,
			event_accepted_at = CASE WHEN relay_policy_projections.event_id = EXCLUDED.event_id THEN relay_policy_projections.event_accepted_at ELSE EXCLUDED.event_accepted_at END,
			schema = CASE WHEN relay_policy_projections.event_id = EXCLUDED.event_id THEN relay_policy_projections.schema ELSE EXCLUDED.schema END,
			canonical_payload = CASE WHEN relay_policy_projections.event_id = EXCLUDED.event_id THEN relay_policy_projections.canonical_payload ELSE EXCLUDED.canonical_payload END,
			payload_hash = CASE WHEN relay_policy_projections.event_id = EXCLUDED.event_id THEN relay_policy_projections.payload_hash ELSE EXCLUDED.payload_hash END,
			source_relay = EXCLUDED.source_relay,
			last_sync_at = EXCLUDED.last_sync_at,
			relay_confirmed_at = EXCLUDED.relay_confirmed_at
		WHERE relay_policy_projections.event_created_at < EXCLUDED.event_created_at
			OR (relay_policy_projections.event_created_at = EXCLUDED.event_created_at
				AND relay_policy_projections.event_id > EXCLUDED.event_id)
			OR (relay_policy_projections.event_id = EXCLUDED.event_id
				AND relay_policy_projections.event_created_at = EXCLUDED.event_created_at
				AND relay_policy_projections.schema = EXCLUDED.schema
				AND relay_policy_projections.canonical_payload = EXCLUDED.canonical_payload
				AND relay_policy_projections.payload_hash = EXCLUDED.payload_hash
				AND relay_policy_projections.relay_confirmed_at IS NULL)
		RETURNING event_id
	`, projection.AuthorPubkey, projection.EventID, projection.EventCreatedAt.UTC(),
		projection.EventAcceptedAt.UTC(), projection.Schema, []byte(projection.CanonicalPayload),
		projection.PayloadHash, projection.SourceRelay, projection.LastSyncAt.UTC()).Scan(&promotedEventID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("promoting relay policy projection: %w", err)
	}
	return promotedEventID == projection.EventID, nil
}

func (r *PgRelayPolicyProjectionRepository) Export(ctx context.Context, authorPubkey string, exportedAt time.Time) (*RelayPolicyProjectionBackup, error) {
	projection, err := r.Get(ctx, strings.ToLower(strings.TrimSpace(authorPubkey)))
	if err != nil || projection == nil {
		return nil, err
	}
	backup, err := NewRelayPolicyProjectionBackup(*projection, exportedAt)
	if err != nil {
		return nil, fmt.Errorf("exporting relay policy projection: %w", err)
	}
	return &backup, nil
}

func (r *PgRelayPolicyProjectionRepository) RestoreCached(ctx context.Context, backup RelayPolicyProjectionBackup) (bool, error) {
	if err := ValidateRelayPolicyProjectionBackup(backup); err != nil {
		return false, fmt.Errorf("restoring relay policy projection: %w", err)
	}
	var restoredEventID string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO relay_policy_projections (
			author_pubkey, event_id, event_created_at, event_accepted_at, schema,
			canonical_payload, payload_hash, source_relay, last_sync_at, relay_confirmed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL)
		ON CONFLICT (author_pubkey) DO UPDATE SET
			event_id = EXCLUDED.event_id,
			event_created_at = EXCLUDED.event_created_at,
			event_accepted_at = EXCLUDED.event_accepted_at,
			schema = EXCLUDED.schema,
			canonical_payload = EXCLUDED.canonical_payload,
			payload_hash = EXCLUDED.payload_hash,
			source_relay = EXCLUDED.source_relay,
			last_sync_at = EXCLUDED.last_sync_at,
			relay_confirmed_at = CASE WHEN relay_policy_projections.event_id = EXCLUDED.event_id THEN relay_policy_projections.relay_confirmed_at ELSE NULL END
		WHERE relay_policy_projections.event_created_at < EXCLUDED.event_created_at
			OR (relay_policy_projections.event_created_at = EXCLUDED.event_created_at
				AND relay_policy_projections.event_id > EXCLUDED.event_id)
			OR (relay_policy_projections.event_id = EXCLUDED.event_id
				AND relay_policy_projections.event_created_at = EXCLUDED.event_created_at
				AND relay_policy_projections.schema = EXCLUDED.schema
				AND relay_policy_projections.canonical_payload = EXCLUDED.canonical_payload
				AND relay_policy_projections.payload_hash = EXCLUDED.payload_hash
				AND relay_policy_projections.relay_confirmed_at IS NULL)
		RETURNING event_id
	`, strings.ToLower(strings.TrimSpace(backup.AuthorPubkey)),
		strings.ToLower(strings.TrimSpace(backup.EventID)), backup.EventCreatedAt.UTC(),
		backup.EventAcceptedAt.UTC(), backup.PolicySchema, []byte(backup.CanonicalPayload),
		strings.ToLower(strings.TrimSpace(backup.PayloadHash)), strings.TrimSpace(backup.SourceRelay),
		backup.LastSyncAt.UTC()).Scan(&restoredEventID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("restoring cached relay policy projection: %w", err)
	}
	return restoredEventID == strings.ToLower(strings.TrimSpace(backup.EventID)), nil
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
