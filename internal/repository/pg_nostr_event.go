package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	// Record inserts rec idempotently. It returns true only when the row was newly inserted.
	Record(ctx context.Context, rec *NostrEventRecord) (bool, error)
	GetByID(ctx context.Context, id string) (*NostrEventRecord, error)
	ListByKind(ctx context.Context, kind int, limit int) ([]NostrEventRecord, error)
	ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]NostrEventRecord, error)
	LatestCreatedAtForKinds(ctx context.Context, kinds []int) (*time.Time, error)
	LatestCreatedAtForKindsAndAuthors(ctx context.Context, kinds []int, authors []string) (*time.Time, error)
}

type nostrEventDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgNostrEventRepository is a PostgreSQL implementation of NostrEventRepository.
type PgNostrEventRepository struct {
	pool nostrEventDB
}

// NewPgNostrEventRepository creates a new PgNostrEventRepository.
func NewPgNostrEventRepository(pool *pgxpool.Pool) *PgNostrEventRepository {
	return newPgNostrEventRepositoryWithDB(pool)
}

func newPgNostrEventRepositoryWithDB(db nostrEventDB) *PgNostrEventRepository {
	return &PgNostrEventRepository{pool: db}
}

const nostrEventColumns = `id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id`

// Record inserts a Nostr event into the audit table.
// Duplicate event IDs are ignored idempotently and reported as inserted=false.
func (r *PgNostrEventRepository) Record(ctx context.Context, rec *NostrEventRecord) (bool, error) {
	if rec.ReceivedAt.IsZero() {
		rec.ReceivedAt = time.Now().UTC()
	}

	tagsJSON := rec.Tags
	if tagsJSON == nil {
		tagsJSON = json.RawMessage("[]")
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO nostr_events (id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING
	`, rec.ID, rec.Kind, rec.PubKey, rec.Content, tagsJSON, rec.Sig, rec.CreatedAt, rec.ReceivedAt, rec.EntityType, rec.EntityID)
	if err != nil {
		return false, fmt.Errorf("recording nostr event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
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

// FindLatestByKindPubkeyDTag returns the newest event with the same kind, pubkey, and Nostr d tag.
func (r *PgNostrEventRepository) FindLatestByKindPubkeyDTag(ctx context.Context, kind int, pubkey, dTag, excludeID string) (*NostrEventRecord, error) {
	rec := &NostrEventRecord{}
	err := r.pool.QueryRow(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE kind = $1 AND pubkey = $2 AND id <> $4 AND EXISTS (SELECT 1 FROM jsonb_array_elements(tags::jsonb) tag WHERE tag->>0 = 'd' AND tag->>1 = $3) ORDER BY created_at DESC LIMIT 1`, kind, pubkey, dTag, excludeID).
		Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying nostr event by d tag: %w", err)
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

// LatestCreatedAtForKinds returns the newest persisted created_at cursor for any of kinds.
func (r *PgNostrEventRepository) LatestCreatedAtForKinds(ctx context.Context, kinds []int) (*time.Time, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	return r.latestCreatedAt(ctx, `SELECT MAX(created_at) FROM nostr_events WHERE kind = ANY($1)`, kinds)
}

// LatestCreatedAtForKindsAndAuthors returns the newest cursor for events matching kinds and authors.
func (r *PgNostrEventRepository) LatestCreatedAtForKindsAndAuthors(ctx context.Context, kinds []int, authors []string) (*time.Time, error) {
	if len(kinds) == 0 || len(authors) == 0 {
		return nil, nil
	}
	return r.latestCreatedAt(ctx, `SELECT MAX(created_at) FROM nostr_events WHERE kind = ANY($1) AND pubkey = ANY($2)`, kinds, authors)
}

func (r *PgNostrEventRepository) latestCreatedAt(ctx context.Context, query string, args ...any) (*time.Time, error) {
	var latest sql.NullTime
	if err := r.pool.QueryRow(ctx, query, args...).Scan(&latest); err != nil {
		return nil, fmt.Errorf("querying latest nostr event cursor: %w", err)
	}
	if !latest.Valid {
		return nil, nil
	}
	return &latest.Time, nil
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
