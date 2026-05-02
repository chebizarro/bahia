package repository

import (
	"context"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

const workerColumns = `pubkey, name, description, architecture,
	max_concurrent_jobs, current_queue_depth, software, pricing, resources, accelerators, runtime_target,
	min_duration_secs, max_duration_secs, geohash, preferred_relays,
	last_advertisement_at, status, created_at, updated_at`

// WorkerRepository manages Loom worker records.
type WorkerRepository interface {
	Upsert(ctx context.Context, w *domain.Worker) error
	GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error)
	List(ctx context.Context, status string, limit int) ([]domain.Worker, error)
	UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error
}

// PgWorkerRepository is a PostgreSQL implementation of WorkerRepository.
type PgWorkerRepository struct {
	pool pgQueryer
}

// NewPgWorkerRepository creates a new PgWorkerRepository.
func NewPgWorkerRepository(pool *pgxpool.Pool) *PgWorkerRepository {
	return &PgWorkerRepository{pool: pool}
}

// Upsert inserts or updates a worker record (keyed by pubkey).
func (r *PgWorkerRepository) Upsert(ctx context.Context, w *domain.Worker) error {
	softwareJSON, err := marshalJSON(w.Software, "worker software")
	if err != nil {
		return err
	}
	pricingJSON, err := marshalJSON(w.Pricing, "worker pricing")
	if err != nil {
		return err
	}
	resourcesJSON, err := marshalJSON(w.Resources, "worker resources")
	if err != nil {
		return err
	}
	acceleratorsJSON, err := marshalJSON(w.Accelerators, "worker accelerators")
	if err != nil {
		return err
	}
	runtimeTargetJSON, err := marshalJSON(w.RuntimeTarget, "worker runtime target")
	if err != nil {
		return err
	}
	relaysJSON, err := marshalJSON(w.PreferredRelays, "worker preferred relays")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO workers (pubkey, name, description, architecture,
		max_concurrent_jobs, current_queue_depth, software, pricing,
			resources, accelerators, runtime_target,
			min_duration_secs, max_duration_secs, geohash, preferred_relays,
			last_advertisement_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, now(), now())
		ON CONFLICT (pubkey) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			architecture = EXCLUDED.architecture,
			max_concurrent_jobs = EXCLUDED.max_concurrent_jobs,
			current_queue_depth = EXCLUDED.current_queue_depth,
			software = EXCLUDED.software,
			pricing = EXCLUDED.pricing,
			resources = EXCLUDED.resources,
			accelerators = EXCLUDED.accelerators,
			runtime_target = EXCLUDED.runtime_target,
			min_duration_secs = EXCLUDED.min_duration_secs,
			max_duration_secs = EXCLUDED.max_duration_secs,
			geohash = EXCLUDED.geohash,
			preferred_relays = EXCLUDED.preferred_relays,
			last_advertisement_at = EXCLUDED.last_advertisement_at,
			status = EXCLUDED.status,
			updated_at = now()
	`, w.PubKey, w.Name, w.Description, w.Architecture,
		w.MaxConcurrentJobs, w.CurrentQueueDepth, softwareJSON, pricingJSON, resourcesJSON, acceleratorsJSON, runtimeTargetJSON,
		w.MinDurationSecs, w.MaxDurationSecs, w.Geohash, relaysJSON,
		w.LastAdvertisementAt, string(w.Status))
	if err != nil {
		return fmt.Errorf("upserting worker: %w", err)
	}
	return nil
}

// GetByPubKey retrieves a worker by its public key.
func (r *PgWorkerRepository) GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error) {
	err := r.pool.QueryRow(ctx, `
		SELECT `+workerColumns+`
		FROM workers WHERE pubkey = $1
	`, pubkey)
	w, scanErr := scanWorker(err)
	if scanErr != nil {
		if scanErr == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying worker: %w", scanErr)
	}
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
			SELECT `+workerColumns+`
			FROM workers WHERE status = $1
			ORDER BY last_advertisement_at DESC LIMIT $2
		`, status, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT `+workerColumns+`
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
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning worker: %w", err)
		}
		workers = append(workers, *w)
	}
	return workers, rows.Err()
}

func scanWorker(row pgx.Row) (*domain.Worker, error) {
	w := &domain.Worker{}
	var softwareJSON, pricingJSON, resourcesJSON, acceleratorsJSON, runtimeTargetJSON, relaysJSON []byte
	if err := row.Scan(
		&w.PubKey, &w.Name, &w.Description, &w.Architecture,
		&w.MaxConcurrentJobs, &w.CurrentQueueDepth, &softwareJSON, &pricingJSON, &resourcesJSON, &acceleratorsJSON, &runtimeTargetJSON,
		&w.MinDurationSecs, &w.MaxDurationSecs, &w.Geohash, &relaysJSON,
		&w.LastAdvertisementAt, &w.Status, &w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(softwareJSON, &w.Software, "worker software"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(pricingJSON, &w.Pricing, "worker pricing"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(resourcesJSON, &w.Resources, "worker resources"); err != nil {
		return nil, err
	}
	if w.Resources != nil && reflect.DeepEqual(*w.Resources, domain.WorkerResources{}) {
		w.Resources = nil
	}
	if err := unmarshalJSON(acceleratorsJSON, &w.Accelerators, "worker accelerators"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(runtimeTargetJSON, &w.RuntimeTarget, "worker runtime target"); err != nil {
		return nil, err
	}
	if w.RuntimeTarget != nil && reflect.DeepEqual(*w.RuntimeTarget, domain.WorkerRuntimeTarget{}) {
		w.RuntimeTarget = nil
	}
	if err := unmarshalJSON(relaysJSON, &w.PreferredRelays, "worker preferred relays"); err != nil {
		return nil, err
	}
	return w, nil
}

// UpdateStatus updates a worker's status.
func (r *PgWorkerRepository) UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE workers SET status = $1, updated_at = now() WHERE pubkey = $2`, string(status), pubkey)
	if err != nil {
		return fmt.Errorf("updating worker status: %w", err)
	}
	return nil
}
