package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPgLLMRouteObservationRepository_CreateAndLatest(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMRouteObservationRepositoryWithDB(mock)
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	runID := uuid.New()
	obs := &domain.LLMRouteObservation{
		RouteID:           routeID,
		EnvironmentID:     envID,
		ObservedReleaseID: &releaseID,
		ObservedRunID:     &runID,
		BackendKind:       domain.LLMBackendKindVLLM,
		BackendEndpoint:   "http://worker:18000",
		BackendHealth:     domain.HealthStatusHealthy,
		GatewayStatus:     domain.GatewayRouteStatusSynced,
		GatewayTarget:     "chat",
		Source:            "test",
		Metadata:          map[string]any{"probe": "ok"},
	}

	mock.ExpectExec("INSERT INTO llm_route_observations").
		WithArgs(pgxmock.AnyArg(), routeID, envID, &releaseID, &runID, obs.BackendKind, obs.BackendEndpoint, obs.BackendHealth, obs.GatewayStatus, obs.GatewayTarget, "", obs.Source, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, repo.Create(ctx, obs))

	now := time.Now().UTC()
	mock.ExpectQuery("FROM llm_route_observations").
		WithArgs(routeID, envID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "route_id", "environment_id", "observed_release_id", "observed_run_id", "backend_kind", "backend_endpoint", "backend_health", "gateway_status", "gateway_target", "gateway_config_hash", "source", "metadata", "observed_at"}).
			AddRow(obs.ID, routeID, envID, &releaseID, &runID, "vllm", "http://worker:18000", "healthy", "synced", "chat", "hash", "test", []byte(`{"probe":"ok"}`), now))

	latest, err := repo.GetLatest(ctx, routeID, envID)
	require.NoError(t, err)
	require.Equal(t, obs.ID, latest.ID)
	require.Equal(t, "ok", latest.Metadata["probe"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgLLMRouteStateRepository_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMRouteStateRepositoryWithDB(mock)
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()
	obsID := uuid.New()
	state := &domain.LLMRouteState{
		RouteID:              routeID,
		EnvironmentID:        envID,
		DesiredReleaseID:     &releaseID,
		DesiredIntentID:      &intentID,
		ActiveRunID:          &runID,
		CurrentObservationID: &obsID,
		DriftStatus:          domain.DriftStatusInSync,
		GatewayStatus:        domain.GatewayRouteStatusSynced,
		BackendKind:          domain.LLMBackendKindVLLM,
		BackendEndpoint:      "http://worker:18000",
		BackendHealth:        domain.HealthStatusHealthy,
		GatewayTarget:        "chat",
	}

	mock.ExpectExec("INSERT INTO llm_route_state").
		WithArgs(routeID, envID, &releaseID, &intentID, &runID, &obsID, state.DriftStatus, state.GatewayStatus, state.BackendKind, state.BackendEndpoint, state.BackendHealth, state.GatewayTarget, state.LastReconciledAt, pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))
	require.NoError(t, repo.Upsert(ctx, state))

	now := time.Now().UTC()
	mock.ExpectQuery("FROM llm_route_state WHERE route_id = \\$1 AND environment_id = \\$2").
		WithArgs(routeID, envID).
		WillReturnRows(pgxmock.NewRows([]string{"route_id", "environment_id", "desired_release_id", "desired_intent_id", "active_run_id", "current_observation_id", "drift_status", "gateway_status", "backend_kind", "backend_endpoint", "backend_health", "gateway_target", "last_reconciled_at", "updated_at"}).
			AddRow(routeID, envID, &releaseID, &intentID, &runID, &obsID, "in_sync", "synced", "vllm", "http://worker:18000", "healthy", "chat", nil, now))

	got, err := repo.Get(ctx, routeID, envID)
	require.NoError(t, err)
	require.Equal(t, domain.DriftStatusInSync, got.DriftStatus)
	require.Equal(t, domain.GatewayRouteStatusSynced, got.GatewayStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}
