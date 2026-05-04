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

func llmRunMockRows() []string {
	return []string{"id", "deployment_intent_id", "backend_kind", "endpoint_ref", "worker_pubkey", "worker_name", "backend_endpoint", "status", "exit_code", "stdout_ref", "stderr_ref", "metadata", "started_at", "finished_at", "created_at", "updated_at"}
}

func TestPgLLMDeploymentIntentRepository_UpdateApproval(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMDeploymentIntentRepositoryWithDB(mock)
	id := uuid.New()
	mock.ExpectExec("UPDATE llm_deployment_intents SET approval_status").
		WithArgs(id, domain.ApprovalStatusApproved, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	require.NoError(t, repo.UpdateApproval(ctx, id, domain.ApprovalStatusApproved))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgLLMDeploymentRunRepository_QueueClaimAndUpdate(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMDeploymentRunRepositoryWithDB(mock)
	intentID := uuid.New()
	routeID := uuid.New()
	releaseID := uuid.New()
	envID := uuid.New()
	runID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("WITH next_intent").
		WithArgs(pgxmock.AnyArg(), domain.RunStatusQueued, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(llmRunMockRows()).
			AddRow(runID, intentID, "", "", "", "", "", domain.RunStatusQueued, nil, "", "", []byte(`{"route_id":"`+routeID.String()+`","release_id":"`+releaseID.String()+`","environment_id":"`+envID.String()+`","nostr_event_id":"req-1","nostr_request_pubkey":"pub-1"}`), nil, nil, now, now))

	queued, err := repo.EnsureQueuedRunForNextReadyIntent(ctx)
	require.NoError(t, err)
	require.NotNil(t, queued)
	require.Equal(t, domain.RunStatusQueued, queued.Status)
	require.Equal(t, map[string]any{"route_id": routeID.String(), "release_id": releaseID.String(), "environment_id": envID.String(), "nostr_event_id": "req-1", "nostr_request_pubkey": "pub-1"}, queued.Metadata)

	mock.ExpectQuery("UPDATE llm_deployment_runs").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(llmRunMockRows()).
			AddRow(runID, intentID, "vllm", "gpu-a", "worker", "gpu worker", "http://worker:18000", domain.RunStatusRunning, nil, "", "", []byte(`{"target_name":"llm-chat"}`), &now, nil, now, now))

	claimed, err := repo.ClaimNextQueuedRun(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, domain.RunStatusRunning, claimed.Status)
	require.Equal(t, "llm-chat", claimed.Metadata["target_name"])

	claimed.BackendEndpoint = "http://worker:18001"
	mock.ExpectExec("UPDATE llm_deployment_runs").
		WithArgs(claimed.ID, claimed.BackendKind, claimed.EndpointRef, claimed.WorkerPubkey, claimed.WorkerName, claimed.BackendEndpoint, claimed.Status, claimed.ExitCode, claimed.StdoutRef, claimed.StderrRef, pgxmock.AnyArg(), claimed.StartedAt, claimed.FinishedAt, pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	require.NoError(t, repo.Update(ctx, claimed))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgLLMDeploymentRunRepository_RequeueStaleRunning(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMDeploymentRunRepositoryWithDB(mock)
	mock.ExpectExec("UPDATE llm_deployment_runs").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 2"))

	count, err := repo.RequeueStaleRunning(ctx, 15*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}
