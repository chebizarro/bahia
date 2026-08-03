package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgEnvironmentServiceStateRepository is a PostgreSQL implementation.
type PgEnvironmentServiceStateRepository struct {
	pool pgQueryer
}

func NewPgEnvironmentServiceStateRepository(pool *pgxpool.Pool) *PgEnvironmentServiceStateRepository {
	return newPgEnvironmentServiceStateRepositoryWithDB(pool)
}

func newPgEnvironmentServiceStateRepositoryWithDB(db pgQueryer) *PgEnvironmentServiceStateRepository {
	return &PgEnvironmentServiceStateRepository{pool: db}
}

const stateColumns = `service_id, environment_id, deployment_unit_id, desired_artifact_id, desired_intent_id, last_successful_run_id, current_observation_id, drift_status, desired_runtime_state, desired_hash, reconcile_failure_metadata, reconcile_backoff_until, reconcile_consecutive_failures, last_reconciled_at, updated_at`

const stateColumnsWithAlias = `ess.service_id, ess.environment_id, ess.deployment_unit_id, ess.desired_artifact_id, ess.desired_intent_id, ess.last_successful_run_id, ess.current_observation_id, ess.drift_status, ess.desired_runtime_state, ess.desired_hash, ess.reconcile_failure_metadata, ess.reconcile_backoff_until, ess.reconcile_consecutive_failures, ess.last_reconciled_at, ess.updated_at`

func (r *PgEnvironmentServiceStateRepository) Upsert(ctx context.Context, state *domain.EnvironmentServiceState) error {
	state.UpdatedAt = time.Now().UTC()

	desiredRuntimeStateJSON, err := marshalJSON(state.DesiredRuntimeState, "desired runtime state")
	if err != nil {
		return err
	}
	failureMetadataJSON, err := marshalJSON(state.ReconcileFailureMetadata, "reconcile failure metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO environment_service_state (`+stateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (service_id, environment_id) DO UPDATE SET
			deployment_unit_id = EXCLUDED.deployment_unit_id,
			desired_artifact_id = EXCLUDED.desired_artifact_id,
			desired_intent_id = EXCLUDED.desired_intent_id,
			last_successful_run_id = EXCLUDED.last_successful_run_id,
			current_observation_id = EXCLUDED.current_observation_id,
			drift_status = EXCLUDED.drift_status,
			desired_runtime_state = EXCLUDED.desired_runtime_state,
			desired_hash = EXCLUDED.desired_hash,
			reconcile_failure_metadata = EXCLUDED.reconcile_failure_metadata,
			reconcile_backoff_until = EXCLUDED.reconcile_backoff_until,
			reconcile_consecutive_failures = EXCLUDED.reconcile_consecutive_failures,
			last_reconciled_at = EXCLUDED.last_reconciled_at,
			updated_at = EXCLUDED.updated_at
	`, state.ServiceID, state.EnvironmentID, state.DeploymentUnitID, state.DesiredArtifactID, state.DesiredIntentID,
		state.LastSuccessfulRunID, state.CurrentObservationID, state.DriftStatus,
		desiredRuntimeStateJSON, state.DesiredHash, failureMetadataJSON, state.ReconcileBackoffUntil,
		state.ReconcileConsecutiveFailures, state.LastReconciledAt, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting environment service state: %w", err)
	}
	return nil
}

// UpsertObservation advances current runtime state only when the incoming
// observation wins the durable (observed_at, id) ordering. This prevents
// concurrent relay/reconcile writers from regressing the current projection.
func (r *PgEnvironmentServiceStateRepository) UpsertObservation(ctx context.Context, state *domain.EnvironmentServiceState) (bool, error) {
	if state == nil || state.CurrentObservationID == nil {
		return false, fmt.Errorf("observation state requires current_observation_id")
	}
	state.UpdatedAt = time.Now().UTC()
	desiredRuntimeStateJSON, err := marshalJSON(state.DesiredRuntimeState, "desired runtime state")
	if err != nil {
		return false, err
	}
	failureMetadataJSON, err := marshalJSON(state.ReconcileFailureMetadata, "reconcile failure metadata")
	if err != nil {
		return false, err
	}
	cmd, err := r.pool.Exec(ctx, `
		INSERT INTO environment_service_state (`+stateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (service_id, environment_id) DO UPDATE SET
			deployment_unit_id = EXCLUDED.deployment_unit_id,
			desired_artifact_id = EXCLUDED.desired_artifact_id,
			desired_intent_id = EXCLUDED.desired_intent_id,
			last_successful_run_id = EXCLUDED.last_successful_run_id,
			current_observation_id = EXCLUDED.current_observation_id,
			drift_status = EXCLUDED.drift_status,
			desired_runtime_state = EXCLUDED.desired_runtime_state,
			desired_hash = EXCLUDED.desired_hash,
			reconcile_failure_metadata = EXCLUDED.reconcile_failure_metadata,
			reconcile_backoff_until = EXCLUDED.reconcile_backoff_until,
			reconcile_consecutive_failures = EXCLUDED.reconcile_consecutive_failures,
			last_reconciled_at = EXCLUDED.last_reconciled_at,
			updated_at = EXCLUDED.updated_at
		WHERE environment_service_state.current_observation_id IS NULL
		OR EXISTS (
				SELECT 1
				FROM runtime_observations incoming
				LEFT JOIN runtime_observations current
				ON current.id = environment_service_state.current_observation_id
				WHERE incoming.id = EXCLUDED.current_observation_id
				AND (
					current.id IS NULL
					OR incoming.observed_at > current.observed_at
					OR (incoming.observed_at = current.observed_at AND incoming.id > current.id)
				)
		)
	`, state.ServiceID, state.EnvironmentID, state.DeploymentUnitID, state.DesiredArtifactID, state.DesiredIntentID,
		state.LastSuccessfulRunID, state.CurrentObservationID, state.DriftStatus,
		desiredRuntimeStateJSON, state.DesiredHash, failureMetadataJSON, state.ReconcileBackoffUntil,
		state.ReconcileConsecutiveFailures, state.LastReconciledAt, state.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("upserting ordered environment observation state: %w", err)
	}
	return cmd.RowsAffected() == 1, nil
}

func (r *PgEnvironmentServiceStateRepository) scanState(row pgx.Row) (*domain.EnvironmentServiceState, error) {
	s := &domain.EnvironmentServiceState{}
	var desiredRuntimeStateJSON []byte
	var failureMetadataJSON []byte
	var desiredHash sql.NullString
	err := row.Scan(&s.ServiceID, &s.EnvironmentID, &s.DeploymentUnitID, &s.DesiredArtifactID, &s.DesiredIntentID,
		&s.LastSuccessfulRunID, &s.CurrentObservationID, &s.DriftStatus,
		&desiredRuntimeStateJSON, &desiredHash, &failureMetadataJSON, &s.ReconcileBackoffUntil,
		&s.ReconcileConsecutiveFailures, &s.LastReconciledAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	s.DesiredHash = nullStringValue(desiredHash)
	if len(desiredRuntimeStateJSON) > 0 && string(desiredRuntimeStateJSON) != "null" {
		s.DesiredRuntimeState = &domain.DesiredServiceSpec{}
		if err := unmarshalJSON(desiredRuntimeStateJSON, s.DesiredRuntimeState, "desired runtime state"); err != nil {
			return nil, err
		}
	}
	if len(failureMetadataJSON) > 0 && string(failureMetadataJSON) != "null" {
		if err := unmarshalJSON(failureMetadataJSON, &s.ReconcileFailureMetadata, "reconcile failure metadata"); err != nil {
			return nil, err
		}
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
		var desiredRuntimeStateJSON []byte
		var failureMetadataJSON []byte
		var desiredHash sql.NullString
		if err := rows.Scan(&s.ServiceID, &s.EnvironmentID, &s.DeploymentUnitID, &s.DesiredArtifactID, &s.DesiredIntentID,
			&s.LastSuccessfulRunID, &s.CurrentObservationID, &s.DriftStatus,
			&desiredRuntimeStateJSON, &desiredHash, &failureMetadataJSON, &s.ReconcileBackoffUntil,
			&s.ReconcileConsecutiveFailures, &s.LastReconciledAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning state: %w", err)
		}
		s.DesiredHash = nullStringValue(desiredHash)
		if len(desiredRuntimeStateJSON) > 0 && string(desiredRuntimeStateJSON) != "null" {
			s.DesiredRuntimeState = &domain.DesiredServiceSpec{}
			if err := unmarshalJSON(desiredRuntimeStateJSON, s.DesiredRuntimeState, "desired runtime state"); err != nil {
				return nil, fmt.Errorf("scanning state desired runtime: %w", err)
			}
		}
		if len(failureMetadataJSON) > 0 && string(failureMetadataJSON) != "null" {
			if err := unmarshalJSON(failureMetadataJSON, &s.ReconcileFailureMetadata, "reconcile failure metadata"); err != nil {
				return nil, fmt.Errorf("scanning state reconcile failure metadata: %w", err)
			}
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

func (r *PgEnvironmentServiceStateRepository) ListDueForObservation(ctx context.Context, dueBefore time.Time) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `
		SELECT `+stateColumnsWithAlias+`
		FROM environment_service_state ess
		JOIN environments env ON env.id = ess.environment_id
		LEFT JOIN deployment_units du ON du.id = ess.deployment_unit_id
		WHERE (ess.last_reconciled_at IS NULL OR ess.last_reconciled_at <= $1)
		AND COALESCE(
			NULLIF(du.reconcile_mode, ''),
			NULLIF(env.targeting->>'default_reconcile_mode', ''),
			NULLIF(env.runtime_config->>'default_reconcile_mode', ''),
			NULLIF(env.runtime_config->>'reconcile_mode', ''),
			'observe_only'
		) <> 'disabled'
	`, dueBefore)
}

func (r *PgEnvironmentServiceStateRepository) ListAll(ctx context.Context) ([]domain.EnvironmentServiceState, error) {
	return r.listByQuery(ctx, `SELECT `+stateColumns+` FROM environment_service_state`)
}
