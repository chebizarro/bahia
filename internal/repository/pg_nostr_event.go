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

const (
	NostrPublishStateNotApplicable = "not_applicable"
	NostrPublishStatePending       = "pending"
	NostrPublishStatePublished     = "published"
)

// NostrEventRecord represents a row in the nostr_events audit table.
type NostrEventRecord struct {
	ID               string
	Kind             int
	PubKey           string
	Content          string
	Tags             json.RawMessage
	Sig              string
	CreatedAt        time.Time
	ReceivedAt       time.Time
	EntityType       string
	EntityID         *uuid.UUID
	PublishState     string
	PublishAttempts  int
	LastPublishError string
	PublishedAt      *time.Time
}

// NostrMigrationCursor is a durable keyset cursor for deterministic migrations.
type NostrMigrationCursor struct {
	Name      string
	CreatedAt time.Time
	EventID   string
}

// NostrEventRepository manages the nostr_events audit trail.
type NostrEventRepository interface {
	// Record inserts rec idempotently. It returns true only when the row was newly inserted.
	Record(ctx context.Context, rec *NostrEventRecord) (bool, error)
	GetByID(ctx context.Context, id string) (*NostrEventRecord, error)
	ListByKind(ctx context.Context, kind int, limit int) ([]NostrEventRecord, error)
	ListByKinds(ctx context.Context, kinds []int, limit int) ([]NostrEventRecord, error)
	FindByTag(ctx context.Context, tagName, tagValue string, kinds []int, limit int) ([]NostrEventRecord, error)
	ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]NostrEventRecord, error)
	LatestCreatedAtForKinds(ctx context.Context, kinds []int) (*time.Time, error)
	LatestCreatedAtForKindsAndAuthors(ctx context.Context, kinds []int, authors []string) (*time.Time, error)
}

// NostrEventOutboxRepository is the durable publish-state extension implemented by
// repositories that can redeliver outbound audit events.
type NostrEventOutboxRepository interface {
	NostrEventRepository
	ListUnpublished(ctx context.Context, limit int) ([]NostrEventRecord, error)
	MarkPublished(ctx context.Context, id string, publishedAt time.Time) error
	RecordPublishFailure(ctx context.Context, id, publishError string) error
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

var _ NostrEventOutboxRepository = (*PgNostrEventRepository)(nil)

// NewPgNostrEventRepository creates a new PgNostrEventRepository.
func NewPgNostrEventRepository(pool *pgxpool.Pool) *PgNostrEventRepository {
	return newPgNostrEventRepositoryWithDB(pool)
}

func newPgNostrEventRepositoryWithDB(db nostrEventDB) *PgNostrEventRepository {
	return &PgNostrEventRepository{pool: db}
}

const nostrEventColumns = `id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id, publish_state, publish_attempts, last_publish_error, published_at`

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
	if rec.PublishState == "" {
		rec.PublishState = NostrPublishStateNotApplicable
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO nostr_events (
			id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id,
			publish_state, publish_attempts, last_publish_error, published_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (id) DO NOTHING
	`, rec.ID, rec.Kind, rec.PubKey, rec.Content, tagsJSON, rec.Sig, rec.CreatedAt, rec.ReceivedAt, rec.EntityType, rec.EntityID,
		rec.PublishState, rec.PublishAttempts, rec.LastPublishError, rec.PublishedAt)
	if err != nil {
		return false, fmt.Errorf("recording nostr event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetByID retrieves a Nostr event by its ID.
func (r *PgNostrEventRepository) GetByID(ctx context.Context, id string) (*NostrEventRecord, error) {
	rec := &NostrEventRecord{}
	err := r.pool.QueryRow(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE id = $1`, id).
		Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID,
			&rec.PublishState, &rec.PublishAttempts, &rec.LastPublishError, &rec.PublishedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying nostr event: %w", err)
	}
	return rec, nil
}

// ListUnpublished returns the oldest pending outbound events first.
func (r *PgNostrEventRepository) ListUnpublished(ctx context.Context, limit int) ([]NostrEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+nostrEventColumns+`
		FROM nostr_events
		WHERE publish_state = $1
		ORDER BY received_at ASC, id ASC
		LIMIT $2`, NostrPublishStatePending, limit)
	if err != nil {
		return nil, fmt.Errorf("listing unpublished nostr events: %w", err)
	}
	defer rows.Close()
	return scanNostrEventRows(rows)
}

// MarkPublished records a successful relay acceptance (including duplicate OK).
func (r *PgNostrEventRepository) MarkPublished(ctx context.Context, id string, publishedAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE nostr_events
		SET publish_state = $2, publish_attempts = publish_attempts + 1,
		    last_publish_error = '', published_at = $3
		WHERE id = $1
	`, id, NostrPublishStatePublished, publishedAt)
	if err != nil {
		return fmt.Errorf("marking nostr event %s published: %w", id, err)
	}
	return nil
}

// RecordPublishFailure retains the event as pending and records retry diagnostics.
func (r *PgNostrEventRepository) RecordPublishFailure(ctx context.Context, id, publishError string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE nostr_events
		SET publish_state = $2, publish_attempts = publish_attempts + 1,
		    last_publish_error = $3
		WHERE id = $1
	`, id, NostrPublishStatePending, publishError)
	if err != nil {
		return fmt.Errorf("recording nostr event %s publish failure: %w", id, err)
	}
	return nil
}

// FindLatestByKindPubkeyDTag returns the newest event with the same kind, pubkey, and Nostr d tag.
func (r *PgNostrEventRepository) FindLatestByKindPubkeyDTag(ctx context.Context, kind int, pubkey, dTag, excludeID string) (*NostrEventRecord, error) {
	rec := &NostrEventRecord{}
	err := r.pool.QueryRow(ctx, `SELECT `+nostrEventColumns+` FROM nostr_events WHERE kind = $1 AND pubkey = $2 AND id <> $4 AND EXISTS (SELECT 1 FROM jsonb_array_elements(tags::jsonb) tag WHERE tag->>0 = 'd' AND tag->>1 = $3) ORDER BY created_at DESC LIMIT 1`, kind, pubkey, dTag, excludeID).
		Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID,
			&rec.PublishState, &rec.PublishAttempts, &rec.LastPublishError, &rec.PublishedAt)
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

// ListByKinds returns the most recent events for any of kinds.
func (r *PgNostrEventRepository) ListByKinds(ctx context.Context, kinds []int, limit int) ([]NostrEventRecord, error) {
	return r.ListByKindsPage(ctx, kinds, nil, limit)
}

// ListByKindsPage returns a deterministic keyset page after the supplied cursor.
func (r *PgNostrEventRepository) ListByKindsPage(ctx context.Context, kinds []int, after *NostrMigrationCursor, limit int) ([]NostrEventRecord, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	query := `SELECT ` + nostrEventColumns + ` FROM nostr_events WHERE kind = ANY($1)`
	args := []any{kinds}
	if after != nil {
		query += ` AND (created_at, id) > ($2, $3)`
		args = append(args, after.CreatedAt, after.EventID)
	}
	query += ` ORDER BY created_at ASC, id ASC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing nostr events by kinds: %w", err)
	}
	defer rows.Close()
	return scanNostrEventRows(rows)
}

func (r *PgNostrEventRepository) GetMigrationCursor(ctx context.Context, name string) (*NostrMigrationCursor, error) {
	cursor := &NostrMigrationCursor{Name: name}
	err := r.pool.QueryRow(ctx, `SELECT created_at, event_id FROM nostr_migration_cursors WHERE name = $1`, name).Scan(&cursor.CreatedAt, &cursor.EventID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying nostr migration cursor %q: %w", name, err)
	}
	return cursor, nil
}

func (r *PgNostrEventRepository) SaveMigrationCursor(ctx context.Context, cursor NostrMigrationCursor) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nostr_migration_cursors (name, created_at, event_id, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (name) DO UPDATE SET created_at = EXCLUDED.created_at, event_id = EXCLUDED.event_id, updated_at = NOW()
		WHERE (nostr_migration_cursors.created_at, nostr_migration_cursors.event_id) < (EXCLUDED.created_at, EXCLUDED.event_id)
	`, cursor.Name, cursor.CreatedAt, cursor.EventID)
	if err != nil {
		return fmt.Errorf("saving nostr migration cursor %q: %w", cursor.Name, err)
	}
	return nil
}

// FindByTag returns events containing tagName=tagValue, optionally restricted by kind.
func (r *PgNostrEventRepository) FindByTag(ctx context.Context, tagName, tagValue string, kinds []int, limit int) ([]NostrEventRecord, error) {
	if tagName == "" || tagValue == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + nostrEventColumns + ` FROM nostr_events WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(tags::jsonb) tag WHERE tag->>0 = $1 AND tag->>1 = $2)`
	args := []any{tagName, tagValue}
	if len(kinds) > 0 {
		query += ` AND kind = ANY($3)`
		args = append(args, kinds)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + fmt.Sprint(len(args)+1)
	args = append(args, limit)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("finding nostr events by tag: %w", err)
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
		if err := rows.Scan(&rec.ID, &rec.Kind, &rec.PubKey, &rec.Content, &rec.Tags, &rec.Sig, &rec.CreatedAt, &rec.ReceivedAt, &rec.EntityType, &rec.EntityID,
			&rec.PublishState, &rec.PublishAttempts, &rec.LastPublishError, &rec.PublishedAt); err != nil {
			return nil, fmt.Errorf("scanning nostr event: %w", err)
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
