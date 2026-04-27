package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgEnvironmentServiceStateRepository is a PostgreSQL implementation.
type PgEnvironmentServiceStateRepository struct {
	pool *pgxpool.Pool
}

func NewPgEnvironmentServiceStateRepository(pool *pgxpool.Pool) *PgEnvironmentServiceStateRepository {
	return &PgEnvironmentServiceStateRepository{pool: pool}
}

const stateColumns = `service_id, environment_id, desired_artifact_id, desired_intent_id, last_successful_run_id, current_observation_id, drift_status, last_reconciled_at, updated_at`

func (r *PgEnvironmentServiceStateRepository) Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error {
	state.UpdatedAt = time.Now().UTC()

	_, err := r.pool.Exec(ctx, `
		INSERT INTO environment_service_state (`+stateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (service_id, environment_id) DO UPDATE SET
			desired_artifact_id = EXCLUDED.desired_artifact_id,
			desired_intent_id = EXCLUDED.desired_intent_id,
			last_successful_run_id = EXCLUDED.last_successful_run_id,
			current_observation_id = EXCLUDED.current_observation_id,
			drift_status = EXCLUDED.drift_status,
			last_reconciled_at = EXCLUDED.last_reconciled_at,
			updated_at = EXCLUDED.updated_at
	`, state.ServiceID, state.EnvironmentID, state.DesiredArtifactID, state.DesiredIntentID,
		state.LastSuccessfulRunID, state.CurrentObservationID, state.DriftStatus, state.LastReconciledAt, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting environment service state: %w", err)
	}
	return nil
}

func (r *PgEnvironmentServiceStateRepository) scanState(row pgx.Row) (*domain.EnvironmentServiceState, error) {
	s := &domain.EnvironmentServiceState{}
	err := row.Scan(&s.ServiceID, &s.EnvironmentID, &s.DesiredArtifactID, &s.DesiredIntentID,
		&s.LastSuccessfulRunID, &s.CurrentObservationID, &s.DriftStatus, &s.LastReconciledAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *PgEnvironmentServiceStateRepository) Get(ctx context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+stateColumns+` FROM environment_service_state WHERE service_id = $1 AND environment_id = $2`, serviceID, envID)
	s, err := r.scanState(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying environment service state: %w", err)
	}
	return s, nil
}

func (r *PgEnvironmentServiceStateRepository) listByQuery(ctx context.Context, query string, args ...any) ([]domain.EnvironmentServiceState, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []domain.EnvironmentServiceState
	for rows.Next() {
		var s domain.EnvironmentServiceState
		if err := rows.Scan(&s.ServiceID, &s.EnvironmentID, &s.DesiredArtifactID, &s.DesiredIntentID,
			&s.LastSuccessfulRunID, &s.CurrentObservationID, &s.DriftStatus, &s.LastReconciledAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning state: %w", err)
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

func (r *PgEnvironmentServiceStateRepository) ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `SELECT `+stateColumns+` FROM environment_service_state WHERE environment_id = $1`, envID)
}

func (r *PgEnvironmentServiceStateRepository) ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `SELECT `+stateColumns+` FROM environment_service_state WHERE service_id = $1`, serviceID)
}

func (r *PgEnvironmentServiceStateRepository) ListDrifted(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `SELECT `+stateColumns+` FROM environment_service_state WHERE drift_status = 'drifted'`)
}

func (r *PgEnvironmentServiceStateRepository) ListAll(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `SELECT `+stateColumns+` FROM environment_service_state`)
}
