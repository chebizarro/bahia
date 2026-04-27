package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// WorkerRepository manages Loom worker records.
type WorkerRepository interface {
	Upsert(ctx context.Context, w *domain.Worker) error
	GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error)
	List(ctx context.Context, status string, limit int) ([]domain.Worker, error)
	UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error
}

// PgWorkerRepository is a PostgreSQL implementation of WorkerRepository.
type PgWorkerRepository struct {
	pool *pgxpool.Pool
}

// NewPgWorkerRepository creates a new PgWorkerRepository.
func NewPgWorkerRepository(pool *pgxpool.Pool) *PgWorkerRepository {
	return &PgWorkerRepository{pool: pool}
}

// Upsert inserts or updates a worker record (keyed by pubkey).
func (r *PgWorkerRepository) Upsert(ctx context.Context, w *domain.Worker) error {
	softwareJSON, err := json.Marshal(w.Software)
	if err != nil {
		return fmt.Errorf("marshaling software: %w", err)
	}
	pricingJSON, err := json.Marshal(w.Pricing)
	if err != nil {
		return fmt.Errorf("marshaling pricing: %w", err)
	}
	relaysJSON, err := json.Marshal(w.PreferredRelays)
	if err != nil {
		return fmt.Errorf("marshaling relays: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO workers (pubkey, name, description, architecture,
			max_concurrent_jobs, current_queue_depth, software, pricing,
			min_duration_secs, max_duration_secs, geohash, preferred_relays,
			last_advertisement_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now(), now())
		ON CONFLICT (pubkey) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			architecture = EXCLUDED.architecture,
			max_concurrent_jobs = EXCLUDED.max_concurrent_jobs,
			current_queue_depth = EXCLUDED.current_queue_depth,
			software = EXCLUDED.software,
			pricing = EXCLUDED.pricing,
			min_duration_secs = EXCLUDED.min_duration_secs,
			max_duration_secs = EXCLUDED.max_duration_secs,
			geohash = EXCLUDED.geohash,
			preferred_relays = EXCLUDED.preferred_relays,
			last_advertisement_at = EXCLUDED.last_advertisement_at,
			status = EXCLUDED.status,
			updated_at = now()
	`, w.PubKey, w.Name, w.Description, w.Architecture,
		w.MaxConcurrentJobs, w.CurrentQueueDepth, softwareJSON, pricingJSON,
		w.MinDurationSecs, w.MaxDurationSecs, w.Geohash, relaysJSON,
		w.LastAdvertisementAt, string(w.Status))
	if err != nil {
		return fmt.Errorf("upserting worker: %w", err)
	}
	return nil
}

// GetByPubKey retrieves a worker by its public key.
func (r *PgWorkerRepository) GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error) {
	w := &domain.Worker{}
	var softwareJSON, pricingJSON, relaysJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT pubkey, name, description, architecture,
			max_concurrent_jobs, current_queue_depth, software, pricing,
			min_duration_secs, max_duration_secs, geohash, preferred_relays,
			last_advertisement_at, status, created_at, updated_at
		FROM workers WHERE pubkey = $1
	`, pubkey).Scan(
		&w.PubKey, &w.Name, &w.Description, &w.Architecture,
		&w.MaxConcurrentJobs, &w.CurrentQueueDepth, &softwareJSON, &pricingJSON,
		&w.MinDurationSecs, &w.MaxDurationSecs, &w.Geohash, &relaysJSON,
		&w.LastAdvertisementAt, &w.Status, &w.CreatedAt, &w.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying worker: %w", err)
	}
	_ = json.Unmarshal(softwareJSON, &w.Software)
	_ = json.Unmarshal(pricingJSON, &w.Pricing)
	_ = json.Unmarshal(relaysJSON, &w.PreferredRelays)
	return w, nil
}

// List returns workers filtered by status (empty = all).
func (r *PgWorkerRepository) List(ctx context.Context, status string, limit int) ([]domain.Worker, error) {
	if limit <= 0 {
		limit = 100
	}

	var rows pgx.Rows
	var err error
	if status != "" {
		rows, err = r.pool.Query(ctx, `
			SELECT pubkey, name, description, architecture,
				max_concurrent_jobs, current_queue_depth, software, pricing,
				min_duration_secs, max_duration_secs, geohash, preferred_relays,
				last_advertisement_at, status, created_at, updated_at
			FROM workers WHERE status = $1
			ORDER BY last_advertisement_at DESC LIMIT $2
		`, status, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT pubkey, name, description, architecture,
				max_concurrent_jobs, current_queue_depth, software, pricing,
				min_duration_secs, max_duration_secs, geohash, preferred_relays,
				last_advertisement_at, status, created_at, updated_at
			FROM workers
			ORDER BY last_advertisement_at DESC LIMIT $1
		`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("listing workers: %w", err)
	}
	defer rows.Close()

	var workers []domain.Worker
	for rows.Next() {
		var w domain.Worker
		var softwareJSON, pricingJSON, relaysJSON []byte
		if err := rows.Scan(
			&w.PubKey, &w.Name, &w.Description, &w.Architecture,
			&w.MaxConcurrentJobs, &w.CurrentQueueDepth, &softwareJSON, &pricingJSON,
			&w.MinDurationSecs, &w.MaxDurationSecs, &w.Geohash, &relaysJSON,
			&w.LastAdvertisementAt, &w.Status, &w.CreatedAt, &w.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning worker: %w", err)
		}
		_ = json.Unmarshal(softwareJSON, &w.Software)
		_ = json.Unmarshal(pricingJSON, &w.Pricing)
		_ = json.Unmarshal(relaysJSON, &w.PreferredRelays)
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

// UpdateStatus updates a worker's status.
func (r *PgWorkerRepository) UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE workers SET status = $1, updated_at = now() WHERE pubkey = $2`, string(status), pubkey)
	if err != nil {
		return fmt.Errorf("updating worker status: %w", err)
	}
	return nil
}
