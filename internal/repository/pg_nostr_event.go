package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/google/uuid"
)

// NostrEventRecord represents a row in the nostr_events audit table.
type NostrEventRecord struct {
	ID         string
	Kind       int
	PubKey     string
	Content    string
	Tags       json.RawMessage
	Sig        string
	CreatedAt  time.Time
	ReceivedAt time.Time
	EntityType string
	EntityID   *uuid.UUID
}

// NostrEventRepository manages the nostr_events audit trail.
type NostrEventRepository interface {
	Record(ctx context.Context, rec *NostrEventRecord) error
	GetByID(ctx context.Context, id string) (*NostrEventRecord, error)
	ListByKind(ctx context.Context, kind int, limit int) ([]NostrEventRecord, error)
	ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]NostrEventRecord, error)
}

// PgNostrEventRepository is a PostgreSQL implementation of NostrEventRepository.
type PgNostrEventRepository struct {
	pool *pgxpool.Pool
}

// NewPgNostrEventRepository creates a new PgNostrEventRepository.
func NewPgNostrEventRepository(pool *pgxpool.Pool) *PgNostrEventRepository {
	return &PgNostrEventRepository{pool: pool}
}

const nostrEventColumns = `id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id`

// Record inserts a Nostr event into the audit table.
// Duplicate event IDs are silently ignored (idempotent).
func (r *PgNostrEventRepository) Record(ctx context.Context, rec *NostrEventRecord) error {
	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}

	tagsJSON := rec.Tags
	if tagsJSON == nil {
		tagsJSON = json.RawMessage("[]")
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO nostr_events (id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING
	`, rec.ID, rec.Kind, rec.PubKey, rec.Content, tagsJSON, rec.Sig, rec.CreatedAt, rec.ReceivedAt, rec.EntityType, rec.EntityID)
	if err != nil {
		return fmt.Errorf("recording nostr event: %w", err)
	}
	return nil
}

// GetByID retrieves a Nostr event by its ID.
func (r *PgNostrEventRepository) GetByID(ctx context.Context, id string) (*NostrEventRecord, error) {
	rec := &NostrEventRecord{}
	err := r.pool.QueryRow(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE id = $1`, id).
		Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying nostr event: %w", err)
	}
	return rec, nil
}

// ListByKind returns the most recent events of a given kind.
func (r *PgNostrEventRepository) ListByKind(ctx context.Context, kind int, limit int) ([]NostrEventRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE kind = $1 ORDER BY created_at DESC LIMIT $2`, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("listing nostr events by kind: %w", err)
	}
	defer rows.Close()
	return scanNostrEventRows(rows)
}

// ListByEntity returns the most recent events for a given entity.
func (r *PgNostrEventRepository) ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]NostrEventRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE entity_type = $1 AND entity_id = $2 ORDER BY created_at DESC LIMIT $3`, entityType, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing nostr events by entity: %w", err)
	}
	defer rows.Close()
	return scanNostrEventRows(rows)
}

func scanNostrEventRows(rows pgx.Rows) ([]NostrEventRecord, error) {
	var records []NostrEventRecord
	for rows.Next() {
		var rec NostrEventRecord
		if err := rows.Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID); err != nil {
			return nil, fmt.Errorf("scanning nostr event: %w", err)
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
