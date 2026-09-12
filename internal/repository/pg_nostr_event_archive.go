package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	NostrArchiveStatusClaimed   = "claimed"
	NostrArchiveStatusExported  = "exported"
	NostrArchiveStatusProtected = "protected"
	NostrArchiveStatusPruned    = "pruned"
)

var (
	ErrNostrArchiveBatchNotFound = errors.New("nostr archive batch not found")
	ErrNostrArchiveNotProtected  = errors.New("nostr archive batch is not protected")
)

// NostrEventArchiveBatch is the durable state for one two-phase archive batch.
type NostrEventArchiveBatch struct {
	ID              uuid.UUID  `json:"id"`
	CutoffAt        time.Time  `json:"cutoff_at"`
	Kinds           []int      `json:"kinds"`
	Status          string     `json:"status"`
	RowCount        int64      `json:"row_count"`
	ExportedPath    string     `json:"exported_path,omitempty"`
	SHA256          string     `json:"sha256,omitempty"`
	CompressedBytes int64      `json:"compressed_bytes,omitempty"`
	ObjectURI       string     `json:"object_uri,omitempty"`
	ObjectVersion   string     `json:"object_version,omitempty"`
	ClaimedAt       time.Time  `json:"claimed_at"`
	ExportedAt      *time.Time `json:"exported_at,omitempty"`
	ProtectedAt     *time.Time `json:"protected_at,omitempty"`
	PrunedAt        *time.Time `json:"pruned_at,omitempty"`
}

// NostrEventStorageStats is a catalog-backed snapshot; it avoids a full table
// count on every metrics refresh.
type NostrEventStorageStats struct {
	TotalBytes        int64            `json:"total_bytes"`
	HeapBytes         int64            `json:"heap_bytes"`
	IndexBytes        int64            `json:"index_bytes"`
	EstimatedLiveRows int64            `json:"estimated_live_rows"`
	EstimatedDeadRows int64            `json:"estimated_dead_rows"`
	OldestHotEvent    *time.Time       `json:"oldest_hot_event,omitempty"`
	ArchiveBatches    map[string]int64 `json:"archive_batches"`
}

// PgNostrEventArchiveRepository manages crash-safe event archival.
type PgNostrEventArchiveRepository struct {
	pool *pgxpool.Pool
}

func NewPgNostrEventArchiveRepository(pool *pgxpool.Pool) *PgNostrEventArchiveRepository {
	return &PgNostrEventArchiveRepository{pool: pool}
}

// NostrEventArchiveOnlineIndexStatements returns the intentionally non-startup
// DDL. Each statement is safe to retry and does not hold a table write lock for
// the duration of a 19GB index build.
func NostrEventArchiveOnlineIndexStatements() []string {
	return []string{
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nostr_events_archive_eligible ON nostr_events(received_at, id) WHERE archive_batch_id IS NULL AND publish_state <> 'pending'`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nostr_events_archive_batch ON nostr_events(archive_batch_id, received_at, id) WHERE archive_batch_id IS NOT NULL`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nostr_events_kind_created ON nostr_events(kind, created_at DESC, id DESC)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nostr_events_kind_author_created ON nostr_events(kind, pubkey, created_at DESC, id DESC)`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_nostr_events_entity_created ON nostr_events(entity_type, entity_id, created_at DESC, id DESC) WHERE entity_type IS NOT NULL AND entity_id IS NOT NULL`,
	}
}

func (r *PgNostrEventArchiveRepository) EnsureOnlineIndexes(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return errors.New("nostr archive repository is not configured")
	}
	for _, statement := range NostrEventArchiveOnlineIndexStatements() {
		if _, err := r.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("creating Nostr archive online index: %w", err)
		}
	}
	if _, err := r.pool.Exec(ctx, `ALTER TABLE nostr_events VALIDATE CONSTRAINT nostr_events_archive_batch_id_fkey`); err != nil {
		return fmt.Errorf("validating Nostr archive ownership constraint: %w", err)
	}
	return nil
}

func (r *PgNostrEventArchiveRepository) OnlineIndexesReady(ctx context.Context) (bool, error) {
	var count int
	var constraintValid bool
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(bool_and(indexes.indisvalid AND indexes.indisready), false)
		FROM pg_class classes
		JOIN pg_index indexes ON indexes.indexrelid = classes.oid
		WHERE classes.relname = ANY($1)
	`, []string{
		"idx_nostr_events_archive_eligible",
		"idx_nostr_events_archive_batch",
		"idx_nostr_events_kind_created",
		"idx_nostr_events_kind_author_created",
		"idx_nostr_events_entity_created",
	}).Scan(&count, &constraintValid)
	if err != nil {
		return false, fmt.Errorf("checking Nostr archive online indexes: %w", err)
	}
	if count != len(NostrEventArchiveOnlineIndexStatements()) || !constraintValid {
		return false, nil
	}
	if err := r.pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conname = 'nostr_events_archive_batch_id_fkey'`).Scan(&constraintValid); err != nil {
		return false, fmt.Errorf("checking Nostr archive ownership constraint: %w", err)
	}
	return constraintValid, nil
}

// ClaimArchiveBatch marks a deterministic bounded set of eligible rows. It
// never selects the durable pending outbox.
func (r *PgNostrEventArchiveRepository) ClaimArchiveBatch(ctx context.Context, cutoff time.Time, limit int, kinds []int) (*NostrEventArchiveBatch, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("nostr archive repository is not configured")
	}
	if limit <= 0 {
		return nil, errors.New("nostr archive batch limit must be positive")
	}
	if kinds == nil {
		kinds = []int{}
	}
	ready, err := r.OnlineIndexesReady(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errors.New("Nostr archive online indexes are not ready; run ensure-indexes before claiming rows")
	}
	batchID := uuid.New()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("beginning Nostr archive claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `INSERT INTO nostr_event_archive_batches (id, cutoff_at, kinds) VALUES ($1, $2, $3)`, batchID, cutoff.UTC(), kinds); err != nil {
		return nil, fmt.Errorf("creating Nostr archive batch: %w", err)
	}
	var count int64
	err = tx.QueryRow(ctx, `
		WITH candidates AS (
			SELECT id
			FROM nostr_events
			WHERE archive_batch_id IS NULL
			  AND publish_state <> 'pending'
			  AND received_at < $2
			  AND (cardinality($4::integer[]) = 0 OR kind = ANY($4))
			ORDER BY received_at ASC, id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		), claimed AS (
			UPDATE nostr_events AS events
			SET archive_batch_id = $1
			FROM candidates
			WHERE events.id = candidates.id
			RETURNING events.id
		)
		SELECT COUNT(*) FROM claimed
	`, batchID, cutoff.UTC(), limit, kinds).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("claiming Nostr archive rows: %w", err)
	}
	if count == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM nostr_event_archive_batches WHERE id = $1`, batchID); err != nil {
			return nil, fmt.Errorf("discarding empty Nostr archive batch: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("committing empty Nostr archive claim: %w", err)
		}
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE nostr_event_archive_batches SET row_count = $2 WHERE id = $1`, batchID, count); err != nil {
		return nil, fmt.Errorf("recording Nostr archive row count: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing Nostr archive claim: %w", err)
	}
	return r.GetArchiveBatch(ctx, batchID)
}

func (r *PgNostrEventArchiveRepository) GetArchiveBatch(ctx context.Context, id uuid.UUID) (*NostrEventArchiveBatch, error) {
	batch := &NostrEventArchiveBatch{}
	err := r.pool.QueryRow(ctx, `SELECT id, cutoff_at, kinds, status, row_count, exported_path, sha256, compressed_bytes, object_uri, object_version, claimed_at, exported_at, protected_at, pruned_at FROM nostr_event_archive_batches WHERE id = $1`, id).
		Scan(&batch.ID, &batch.CutoffAt, &batch.Kinds, &batch.Status, &batch.RowCount, &batch.ExportedPath, &batch.SHA256, &batch.CompressedBytes, &batch.ObjectURI, &batch.ObjectVersion, &batch.ClaimedAt, &batch.ExportedAt, &batch.ProtectedAt, &batch.PrunedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNostrArchiveBatchNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading Nostr archive batch: %w", err)
	}
	return batch, nil
}

// ListArchiveBatchJSON returns complete database rows minus archive ownership.
// json_populate_record during restore preserves all event columns without
// expanding the public NostrEventRecord API.
func (r *PgNostrEventArchiveRepository) ListArchiveBatchJSON(ctx context.Context, id uuid.UUID) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT (to_jsonb(events) - 'archive_batch_id')::text FROM nostr_events AS events WHERE archive_batch_id = $1 ORDER BY received_at ASC, id ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("listing Nostr archive batch rows: %w", err)
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, fmt.Errorf("scanning Nostr archive row: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *PgNostrEventArchiveRepository) MarkArchiveExported(ctx context.Context, id uuid.UUID, path, sha256 string, compressedBytes int64) error {
	path = strings.TrimSpace(path)
	sha256 = strings.ToLower(strings.TrimSpace(sha256))
	if path == "" || len(sha256) != 64 || compressedBytes <= 0 {
		return errors.New("Nostr archive export metadata is incomplete")
	}
	tag, err := r.pool.Exec(ctx, `UPDATE nostr_event_archive_batches SET status = 'exported', exported_path = $2, sha256 = $3, compressed_bytes = $4, exported_at = now() WHERE id = $1 AND status IN ('claimed', 'exported')`, id, path, sha256, compressedBytes)
	if err != nil {
		return fmt.Errorf("marking Nostr archive exported: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNostrArchiveBatchNotFound
	}
	return nil
}

func (r *PgNostrEventArchiveRepository) MarkArchiveProtected(ctx context.Context, id uuid.UUID, objectURI, objectVersion, sha256 string) error {
	parsed, err := url.Parse(strings.TrimSpace(objectURI))
	if err != nil || parsed.Scheme == "" || parsed.Scheme == "file" {
		return errors.New("protected object URI must use a non-file absolute scheme")
	}
	if strings.TrimSpace(objectVersion) == "" {
		return errors.New("protected object version is required")
	}
	sha256 = strings.ToLower(strings.TrimSpace(sha256))
	if len(sha256) != 64 {
		return errors.New("protected object SHA-256 is invalid")
	}
	tag, err := r.pool.Exec(ctx, `UPDATE nostr_event_archive_batches SET status = 'protected', object_uri = $2, object_version = $3, protected_at = now() WHERE id = $1 AND status = 'exported' AND sha256 = $4`, id, strings.TrimSpace(objectURI), strings.TrimSpace(objectVersion), sha256)
	if err != nil {
		return fmt.Errorf("protecting Nostr archive batch: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Nostr archive batch is not exported or digest does not match")
	}
	return nil
}

// PruneArchiveBatch deletes at most limit exact batch members. done becomes
// true only after no member remains and the batch is durably marked pruned.
func (r *PgNostrEventArchiveRepository) PruneArchiveBatch(ctx context.Context, id uuid.UUID, limit int) (deleted int64, done bool, err error) {
	if limit <= 0 {
		return 0, false, errors.New("Nostr archive prune limit must be positive")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, false, fmt.Errorf("beginning Nostr archive prune: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM nostr_event_archive_batches WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, ErrNostrArchiveBatchNotFound
		}
		return 0, false, fmt.Errorf("locking Nostr archive batch: %w", err)
	}
	if status != NostrArchiveStatusProtected && status != NostrArchiveStatusPruned {
		return 0, false, ErrNostrArchiveNotProtected
	}
	if status == NostrArchiveStatusPruned {
		return 0, true, nil
	}
	if err := tx.QueryRow(ctx, `WITH victims AS (SELECT ctid FROM nostr_events WHERE archive_batch_id = $1 ORDER BY received_at, id LIMIT $2 FOR UPDATE SKIP LOCKED), deleted AS (DELETE FROM nostr_events events USING victims WHERE events.ctid = victims.ctid RETURNING 1) SELECT COUNT(*) FROM deleted`, id, limit).Scan(&deleted); err != nil {
		return 0, false, fmt.Errorf("pruning Nostr archive rows: %w", err)
	}
	var remaining bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM nostr_events WHERE archive_batch_id = $1)`, id).Scan(&remaining); err != nil {
		return 0, false, fmt.Errorf("checking Nostr archive remainder: %w", err)
	}
	done = !remaining
	if done {
		if _, err := tx.Exec(ctx, `UPDATE nostr_event_archive_batches SET status = 'pruned', pruned_at = now() WHERE id = $1`, id); err != nil {
			return 0, false, fmt.Errorf("completing Nostr archive prune: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("committing Nostr archive prune: %w", err)
	}
	return deleted, done, nil
}

func (r *PgNostrEventArchiveRepository) RestoreArchiveJSON(ctx context.Context, rowJSON string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO nostr_events (id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id, direction, processing_status, processing_error, processed_at, publish_state, publish_attempts, last_publish_error, published_at)
		SELECT id, kind, pubkey, content, tags, sig, created_at, received_at, entity_type, entity_id, direction, processing_status, processing_error, processed_at, publish_state, publish_attempts, last_publish_error, published_at
		FROM jsonb_populate_record(NULL::nostr_events, $1::jsonb)
		ON CONFLICT (id) DO NOTHING
	`, rowJSON)
	if err != nil {
		return false, fmt.Errorf("restoring Nostr archive row: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PgNostrEventArchiveRepository) StorageStats(ctx context.Context) (NostrEventStorageStats, error) {
	stats := NostrEventStorageStats{ArchiveBatches: make(map[string]int64)}
	err := r.pool.QueryRow(ctx, `SELECT pg_total_relation_size('nostr_events'), pg_relation_size('nostr_events'), pg_indexes_size('nostr_events'), COALESCE(n_live_tup,0)::bigint, COALESCE(n_dead_tup,0)::bigint FROM pg_stat_user_tables WHERE relname = 'nostr_events'`).
		Scan(&stats.TotalBytes, &stats.HeapBytes, &stats.IndexBytes, &stats.EstimatedLiveRows, &stats.EstimatedDeadRows)
	if err != nil {
		return stats, fmt.Errorf("querying Nostr event storage stats: %w", err)
	}
	var archiveIndexPresent bool
	if err := r.pool.QueryRow(ctx, `SELECT to_regclass('idx_nostr_events_archive_eligible') IS NOT NULL`).Scan(&archiveIndexPresent); err != nil {
		return stats, fmt.Errorf("checking Nostr archive index: %w", err)
	}
	if archiveIndexPresent {
		var oldest *time.Time
		if err := r.pool.QueryRow(ctx, `SELECT MIN(received_at) FROM nostr_events WHERE archive_batch_id IS NULL AND publish_state <> 'pending'`).Scan(&oldest); err != nil {
			return stats, fmt.Errorf("querying oldest hot Nostr event: %w", err)
		}
		stats.OldestHotEvent = oldest
	}
	rows, err := r.pool.Query(ctx, `SELECT status, COUNT(*) FROM nostr_event_archive_batches GROUP BY status`)
	if err != nil {
		return stats, fmt.Errorf("querying Nostr archive batch stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return stats, err
		}
		stats.ArchiveBatches[status] = count
	}
	return stats, rows.Err()
}
