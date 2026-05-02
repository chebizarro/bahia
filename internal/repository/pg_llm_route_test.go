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

func TestPgLLMRouteRepository_CreateAndGetByName(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMRouteRepositoryWithDB(mock)
	route := &domain.LLMRoute{
		Name:        "chat",
		Description: "chat route",
		GatewayConfig: &domain.LLMGatewayRouteConfig{
			PublicModel: "chat",
			Path:        "/v1/models/chat",
		},
		DefaultPlacementPolicy: &domain.LLMPlacementPolicy{PreferredKinds: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}},
		Metadata:               map[string]any{"owner": "ops"},
	}

	mock.ExpectExec("INSERT INTO llm_routes").
		WithArgs(pgxmock.AnyArg(), "chat", "chat route", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	require.NoError(t, repo.Create(ctx, route))
	require.NotEqual(t, uuid.Nil, route.ID)

	now := time.Now().UTC()
	mock.ExpectQuery("FROM llm_routes WHERE name = \\$1").
		WithArgs("chat").
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "description", "gateway_config", "default_placement_policy", "default_promotion_gate", "metadata", "created_at", "updated_at"}).
			AddRow(route.ID, "chat", "chat route", []byte(`{"public_model":"chat","path":"/v1/models/chat"}`), []byte(`{"preferred_kinds":["vllm"]}`), []byte(`null`), []byte(`{"owner":"ops"}`), now, now))

	got, err := repo.GetByName(ctx, "chat")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "chat", got.Name)
	require.Equal(t, "ops", got.Metadata["owner"])
	require.Equal(t, domain.LLMBackendKindVLLM, got.DefaultPlacementPolicy.PreferredKinds[0])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgLLMReleaseRepository_ListByRoute(t *testing.T) {
	ctx := context.Background()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	repo := newPgLLMReleaseRepositoryWithDB(mock)
	routeID := uuid.New()
	releaseID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("FROM llm_releases").
		WithArgs(routeID, 20, 0).
		WillReturnRows(pgxmock.NewRows([]string{"id", "route_id", "version", "model_ref", "model_source", "model_revision", "estimated_vram_gb", "backend_preferences", "runtime_backend", "external_backend", "placement_policy", "promotion_gate", "metadata", "created_at"}).
			AddRow(releaseID, routeID, "v1", "llama", "huggingface", "main", 24, []byte(`["vllm"]`), []byte(`{"image":"vllm","container_port":8000,"host_port":18000,"health_path":"/health"}`), []byte(`null`), []byte(`{"min_gpu_count":1}`), []byte(`null`), []byte(`{"sha":"abc"}`), now))

	releases, err := repo.ListByRoute(ctx, routeID, 20, 0)
	require.NoError(t, err)
	require.Len(t, releases, 1)
	require.Equal(t, releaseID, releases[0].ID)
	require.Equal(t, domain.LLMBackendKindVLLM, releases[0].BackendPreferences[0])
	require.Equal(t, "vllm", releases[0].RuntimeBackend.Image)
	require.NoError(t, mock.ExpectationsWereMet())
}
