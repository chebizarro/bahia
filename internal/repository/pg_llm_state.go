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

// PgLLMRouteObservationRepository is a PostgreSQL implementation.
type PgLLMRouteObservationRepository struct {
	pool pgQueryer
}

func NewPgLLMRouteObservationRepository(pool *pgxpool.Pool) *PgLLMRouteObservationRepository {
	return newPgLLMRouteObservationRepositoryWithDB(pool)
}

func newPgLLMRouteObservationRepositoryWithDB(db pgQueryer) *PgLLMRouteObservationRepository {
	return &PgLLMRouteObservationRepository{pool: db}
}

const llmObservationColumns = `id, route_id, environment_id, observed_release_id, observed_run_id, backend_kind, backend_endpoint, backend_health, gateway_status, gateway_target, gateway_config_hash, source, metadata, observed_at`

func (r *PgLLMRouteObservationRepository) Create(ctx context.Context, observation *domain.LLMRouteObservation) error {
	if observation.ID == uuid.Nil {
		observation.ID = uuid.New()
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	metaJSON, err := marshalJSON(observation.Metadata, "LLM route observation metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO llm_route_observations (`+llmObservationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, observation.ID, observation.RouteID, observation.EnvironmentID, observation.ObservedReleaseID, observation.ObservedRunID,
		observation.BackendKind, observation.BackendEndpoint, observation.BackendHealth, observation.GatewayStatus,
		observation.GatewayTarget, observation.GatewayConfigHash, observation.Source, metaJSON, observation.ObservedAt)
	if err != nil {
		return fmt.Errorf("inserting LLM route observation: %w", err)
	}
	return nil
}

func (r *PgLLMRouteObservationRepository) scanObservation(row pgx.Row) (*domain.LLMRouteObservation, error) {
	observation := &domain.LLMRouteObservation{}
	var metaJSON []byte
	if err := row.Scan(&observation.ID, &observation.RouteID, &observation.EnvironmentID, &observation.ObservedReleaseID,
		&observation.ObservedRunID, &observation.BackendKind, &observation.BackendEndpoint, &observation.BackendHealth,
		&observation.GatewayStatus, &observation.GatewayTarget, &observation.GatewayConfigHash, &observation.Source,
		&metaJSON, &observation.ObservedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &observation.Metadata, "LLM route observation metadata"); err != nil {
		return nil, err
	}
	return observation, nil
}

func (r *PgLLMRouteObservationRepository) GetLatest(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteObservation, error) {
	observation, err := r.scanObservation(r.pool.QueryRow(ctx, `
		SELECT `+llmObservationColumns+` FROM llm_route_observations
		WHERE route_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC LIMIT 1
	`, routeID, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying latest LLM route observation: %w", err)
	}
	return observation, nil
}

func (r *PgLLMRouteObservationRepository) ListByRouteEnv(ctx context.Context, routeID, envID uuid.UUID, limit int) ([]domain.LLMRouteObservation, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+llmObservationColumns+` FROM llm_route_observations
		WHERE route_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC LIMIT $3
	`, routeID, envID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing LLM route observations: %w", err)
	}
	defer rows.Close()

	var observations []domain.LLMRouteObservation
	for rows.Next() {
		observation, err := r.scanObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM route observation: %w", err)
		}
		observations = append(observations, *observation)
	}
	return observations, rows.Err()
}

// PgLLMRouteStateRepository is a PostgreSQL implementation.
type PgLLMRouteStateRepository struct {
	pool pgQueryer
}

func NewPgLLMRouteStateRepository(pool *pgxpool.Pool) *PgLLMRouteStateRepository {
	return newPgLLMRouteStateRepositoryWithDB(pool)
}

func newPgLLMRouteStateRepositoryWithDB(db pgQueryer) *PgLLMRouteStateRepository {
	return &PgLLMRouteStateRepository{pool: db}
}

const llmStateColumns = `route_id, environment_id, desired_release_id, desired_intent_id, active_run_id, current_observation_id, drift_status, gateway_status, backend_kind, backend_endpoint, backend_health, gateway_target, last_reconciled_at, updated_at`

func (r *PgLLMRouteStateRepository) Upsert(ctx context.Context, state *domain.LLMRouteState) error {
	state.UpdatedAt = time.Now().UTC()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO llm_route_state (`+llmStateColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (route_id, environment_id) DO UPDATE SET
			desired_release_id = EXCLUDED.desired_release_id,
			desired_intent_id = EXCLUDED.desired_intent_id,
			active_run_id = EXCLUDED.active_run_id,
			current_observation_id = EXCLUDED.current_observation_id,
			drift_status = EXCLUDED.drift_status,
			gateway_status = EXCLUDED.gateway_status,
			backend_kind = EXCLUDED.backend_kind,
			backend_endpoint = EXCLUDED.backend_endpoint,
			backend_health = EXCLUDED.backend_health,
			gateway_target = EXCLUDED.gateway_target,
			last_reconciled_at = EXCLUDED.last_reconciled_at,
			updated_at = EXCLUDED.updated_at
	`, state.RouteID, state.EnvironmentID, state.DesiredReleaseID, state.DesiredIntentID, state.ActiveRunID,
		state.CurrentObservationID, state.DriftStatus, state.GatewayStatus, state.BackendKind, state.BackendEndpoint,
		state.BackendHealth, state.GatewayTarget, state.LastReconciledAt, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting LLM route state: %w", err)
	}
	return nil
}

func (r *PgLLMRouteStateRepository) scanState(row pgx.Row) (*domain.LLMRouteState, error) {
	state := &domain.LLMRouteState{}
	if err := row.Scan(&state.RouteID, &state.EnvironmentID, &state.DesiredReleaseID, &state.DesiredIntentID,
		&state.ActiveRunID, &state.CurrentObservationID, &state.DriftStatus, &state.GatewayStatus,
		&state.BackendKind, &state.BackendEndpoint, &state.BackendHealth, &state.GatewayTarget,
		&state.LastReconciledAt, &state.UpdatedAt); err != nil {
		return nil, err
	}
	return state, nil
}

func (r *PgLLMRouteStateRepository) Get(ctx context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error) {
	state, err := r.scanState(r.pool.QueryRow(ctx, `SELECT `+llmStateColumns+` FROM llm_route_state WHERE route_id = $1 AND environment_id = $2`, routeID, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying LLM route state: %w", err)
	}
	return state, nil
}

func (r *PgLLMRouteStateRepository) listByQuery(ctx context.Context, query string, args ...any) ([]domain.LLMRouteState, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []domain.LLMRouteState
	for rows.Next() {
		state, err := r.scanState(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning LLM route state: %w", err)
		}
		states = append(states, *state)
	}
	return states, rows.Err()
}

func (r *PgLLMRouteStateRepository) ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.LLMRouteState, error) {
	return r.listByQuery(ctx, `SELECT `+llmStateColumns+` FROM llm_route_state WHERE environment_id = $1`, envID)
}

func (r *PgLLMRouteStateRepository) ListByRoute(ctx context.Context, routeID uuid.UUID) ([]domain.LLMRouteState, error) {
	return r.listByQuery(ctx, `SELECT `+llmStateColumns+` FROM llm_route_state WHERE route_id = $1`, routeID)
}

func (r *PgLLMRouteStateRepository) ListDrifted(ctx context.Context) ([]domain.LLMRouteState, error) {
	return r.listByQuery(ctx, `SELECT `+llmStateColumns+` FROM llm_route_state WHERE drift_status = 'drifted'`)
}

func (r *PgLLMRouteStateRepository) ListAll(ctx context.Context) ([]domain.LLMRouteState, error) {
	return r.listByQuery(ctx, `SELECT `+llmStateColumns+` FROM llm_route_state`)
}
