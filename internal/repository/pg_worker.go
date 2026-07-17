package repository

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

const workerColumns = `pubkey, name, description, architecture,
	max_concurrent_jobs, current_queue_depth, software, pricing, resources, accelerators, telemetry, pressure, ml_capabilities, capabilities, runtime_target,
	min_duration_secs, max_duration_secs, geohash, preferred_relays,
	last_advertisement_at, status, scheduling_state, scheduling_note, standby_assignments, labels, created_at, updated_at`

// WorkerRepository manages Loom worker records.
type WorkerRepository interface {
	Upsert(ctx context.Context, w *domain.Worker) error
	GetByPubKey(ctx context.Context, pubkey string) (*domain.Worker, error)
	List(ctx context.Context, status string, limit int) ([]domain.Worker, error)
	UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error
}

// WorkerLabelLister lists workers through label containment queries.
type WorkerLabelLister interface {
	ListByLabels(ctx context.Context, labels map[string]string, limit int) ([]domain.Worker, error)
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
	telemetryJSON, err := marshalJSON(w.Telemetry, "worker telemetry")
	if err != nil {
		return err
	}
	pressureJSON, err := marshalJSON(w.Pressure, "worker pressure")
	if err != nil {
		return err
	}
	mlCapabilitiesJSON, err := marshalJSON(w.MLCapabilities, "worker ML capabilities")
	if err != nil {
		return err
	}
	capabilitiesJSON, err := marshalJSON(w.Capabilities, "worker capabilities")
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
	standbyAssignmentsUpdate := w.StandbyAssignments != nil
	standbyAssignmentsJSON, err := marshalJSON(w.StandbyAssignments, "worker standby assignments")
	if err != nil {
		return err
	}
	labelsUpdate := w.Labels != nil
	labels := w.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := marshalJSON(labels, "worker labels")
	if err != nil {
		return err
	}
	schedulingStateProvided := w.SchedulingState != ""
	schedulingState := w.SchedulingState
	if !schedulingStateProvided {
		schedulingState = domain.WorkerSchedulingActive
	}

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO workers (pubkey, name, description, architecture,
		max_concurrent_jobs, current_queue_depth, software, pricing,
			resources, accelerators, telemetry, pressure, ml_capabilities, capabilities, runtime_target,
			min_duration_secs, max_duration_secs, geohash, preferred_relays,
			last_advertisement_at, status, scheduling_state, scheduling_note, standby_assignments, labels, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, now(), now())
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
			telemetry = EXCLUDED.telemetry,
			pressure = EXCLUDED.pressure,
			ml_capabilities = EXCLUDED.ml_capabilities,
			capabilities = EXCLUDED.capabilities,
			runtime_target = EXCLUDED.runtime_target,
			min_duration_secs = EXCLUDED.min_duration_secs,
			max_duration_secs = EXCLUDED.max_duration_secs,
			geohash = EXCLUDED.geohash,
			preferred_relays = EXCLUDED.preferred_relays,
			last_advertisement_at = EXCLUDED.last_advertisement_at,
			status = EXCLUDED.status,
			scheduling_state = CASE WHEN $26 THEN EXCLUDED.scheduling_state ELSE workers.scheduling_state END,
			scheduling_note = CASE WHEN $26 THEN EXCLUDED.scheduling_note ELSE workers.scheduling_note END,
			standby_assignments = CASE WHEN $27 THEN EXCLUDED.standby_assignments ELSE workers.standby_assignments END,
			labels = CASE WHEN $28 THEN EXCLUDED.labels ELSE workers.labels END,
			updated_at = now()
		WHERE EXCLUDED.last_advertisement_at >= workers.last_advertisement_at
	`, w.PubKey, w.Name, w.Description, w.Architecture,
		w.MaxConcurrentJobs, w.CurrentQueueDepth, softwareJSON, pricingJSON, resourcesJSON, acceleratorsJSON, telemetryJSON, pressureJSON, mlCapabilitiesJSON, capabilitiesJSON, runtimeTargetJSON,
		w.MinDurationSecs, w.MaxDurationSecs, w.Geohash, relaysJSON,
		w.LastAdvertisementAt, string(w.Status), string(schedulingState), w.SchedulingNote, standbyAssignmentsJSON, labelsJSON, schedulingStateProvided, standbyAssignmentsUpdate, labelsUpdate)
	if err != nil {
		return fmt.Errorf("upserting worker: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("worker %s advertisement at %s: %w", w.PubKey, w.LastAdvertisementAt.UTC().Format(time.RFC3339Nano), ErrStaleWrite)
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

	return scanWorkers(rows)
}

// ListByLabels returns workers whose labels contain all requested key/value pairs.
func (r *PgWorkerRepository) ListByLabels(ctx context.Context, labels map[string]string, limit int) ([]domain.Worker, error) {
	if len(labels) == 0 {
		return r.List(ctx, "", limit)
	}
	if limit <= 0 {
		limit = 100
	}
	labelsJSON, err := marshalJSON(labels, "worker label selector")
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+workerColumns+`
		FROM workers
		WHERE labels @> $1::jsonb
		ORDER BY last_advertisement_at DESC LIMIT $2
	`, labelsJSON, limit)
	if err != nil {
		return nil, fmt.Errorf("listing workers by labels: %w", err)
	}
	defer rows.Close()

	return scanWorkers(rows)
}

func scanWorkers(rows pgx.Rows) ([]domain.Worker, error) {
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
	var softwareJSON, pricingJSON, resourcesJSON, acceleratorsJSON, telemetryJSON, pressureJSON, mlCapabilitiesJSON, capabilitiesJSON, runtimeTargetJSON, relaysJSON, standbyAssignmentsJSON, labelsJSON []byte
	if err := row.Scan(
		&w.PubKey, &w.Name, &w.Description, &w.Architecture,
		&w.MaxConcurrentJobs, &w.CurrentQueueDepth, &softwareJSON, &pricingJSON, &resourcesJSON, &acceleratorsJSON, &telemetryJSON, &pressureJSON, &mlCapabilitiesJSON, &capabilitiesJSON, &runtimeTargetJSON,
		&w.MinDurationSecs, &w.MaxDurationSecs, &w.Geohash, &relaysJSON,
		&w.LastAdvertisementAt, &w.Status, &w.SchedulingState, &w.SchedulingNote, &standbyAssignmentsJSON, &labelsJSON, &w.CreatedAt, &w.UpdatedAt,
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
	if err := unmarshalJSON(telemetryJSON, &w.Telemetry, "worker telemetry"); err != nil {
		return nil, err
	}
	if w.Telemetry != nil && reflect.DeepEqual(*w.Telemetry, domain.WorkerTelemetry{}) {
		w.Telemetry = nil
	}
	if err := unmarshalJSON(pressureJSON, &w.Pressure, "worker pressure"); err != nil {
		return nil, err
	}
	if w.Pressure != nil && reflect.DeepEqual(*w.Pressure, domain.WorkerPressureAssessment{}) {
		w.Pressure = nil
	}
	if err := unmarshalJSON(mlCapabilitiesJSON, &w.MLCapabilities, "worker ML capabilities"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(capabilitiesJSON, &w.Capabilities, "worker capabilities"); err != nil {
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
	if err := unmarshalJSON(standbyAssignmentsJSON, &w.StandbyAssignments, "worker standby assignments"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(labelsJSON, &w.Labels, "worker labels"); err != nil {
		return nil, err
	}
	if w.SchedulingState == "" {
		w.SchedulingState = domain.WorkerSchedulingActive
	}
	return w, nil
}

// UpdateStatus updates a worker's liveness status.
func (r *PgWorkerRepository) UpdateStatus(ctx context.Context, pubkey string, status domain.WorkerStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE workers SET status = $1, updated_at = now() WHERE pubkey = $2`, string(status), pubkey)
	if err != nil {
		return fmt.Errorf("updating worker status: %w", err)
	}
	return nil
}

// UpdateSchedulingState updates only operator scheduling intent fields for a worker.
func (r *PgWorkerRepository) UpdateSchedulingState(ctx context.Context, pubkey string, state domain.WorkerSchedulingState, note string) error {
	if state == "" {
		state = domain.WorkerSchedulingActive
	}
	_, err := r.pool.Exec(ctx, `UPDATE workers SET scheduling_state = $1, scheduling_note = $2, updated_at = now() WHERE pubkey = $3`, string(state), note, pubkey)
	if err != nil {
		return fmt.Errorf("updating worker scheduling state: %w", err)
	}
	return nil
}

// UpdateLabels updates only operator-managed labels for a worker.
func (r *PgWorkerRepository) UpdateLabels(ctx context.Context, pubkey string, labels map[string]string) error {
	if labels == nil {
		labels = map[string]string{}
	}
	labelsJSON, err := marshalJSON(labels, "worker labels")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `UPDATE workers SET labels = $1, updated_at = now() WHERE pubkey = $2`, labelsJSON, pubkey)
	if err != nil {
		return fmt.Errorf("updating worker labels: %w", err)
	}
	return nil
}
