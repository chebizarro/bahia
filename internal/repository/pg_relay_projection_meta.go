package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgRelayProjectionMetaRepository stores relay projection ordering metadata in Postgres.
type PgRelayProjectionMetaRepository struct {
	pool pgQueryer
}

func NewPgRelayProjectionMetaRepository(pool *pgxpool.Pool) *PgRelayProjectionMetaRepository {
	return newPgRelayProjectionMetaRepositoryWithDB(pool)
}

func newPgRelayProjectionMetaRepositoryWithDB(db pgQueryer) *PgRelayProjectionMetaRepository {
	return &PgRelayProjectionMetaRepository{pool: db}
}

func (r *PgRelayProjectionMetaRepository) Get(ctx context.Context, stream, entityKey string) (*RelayProjectionMeta, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT stream, entity_key, updated_at, source_event_id, tombstoned
		FROM relay_projection_meta
		WHERE stream = $1 AND entity_key = $2
	`, stream, entityKey)

	meta := &RelayProjectionMeta{}
	if err := row.Scan(&meta.Stream, &meta.EntityKey, &meta.UpdatedAt, &meta.SourceEventID, &meta.Tombstoned); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying relay projection meta: %w", err)
	}
	return meta, nil
}

func (r *PgRelayProjectionMetaRepository) Upsert(ctx context.Context, meta RelayProjectionMeta) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO relay_projection_meta (stream, entity_key, updated_at, source_event_id, tombstoned)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (stream, entity_key) DO UPDATE SET
			updated_at = EXCLUDED.updated_at,
			source_event_id = EXCLUDED.source_event_id,
			tombstoned = EXCLUDED.tombstoned
		WHERE relay_projection_meta.updated_at < EXCLUDED.updated_at
	`, meta.Stream, meta.EntityKey, meta.UpdatedAt, meta.SourceEventID, meta.Tombstoned)
	if err != nil {
		return fmt.Errorf("upserting relay projection meta: %w", err)
	}
	return nil
}

func (r *PgRelayProjectionMetaRepository) ListByStream(ctx context.Context, stream string) ([]RelayProjectionMeta, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT stream, entity_key, updated_at, source_event_id, tombstoned
		FROM relay_projection_meta
		WHERE stream = $1
		ORDER BY entity_key
	`, stream)
	if err != nil {
		return nil, fmt.Errorf("listing relay projection meta: %w", err)
	}
	defer rows.Close()

	metas := make([]RelayProjectionMeta, 0)
	for rows.Next() {
		var meta RelayProjectionMeta
		if err := rows.Scan(&meta.Stream, &meta.EntityKey, &meta.UpdatedAt, &meta.SourceEventID, &meta.Tombstoned); err != nil {
			return nil, fmt.Errorf("scanning relay projection meta: %w", err)
		}
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating relay projection meta: %w", err)
	}
	return metas, nil
}
