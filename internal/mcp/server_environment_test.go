package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testEnvironmentRepo struct {
	environments map[uuid.UUID]*domain.Environment
}

func newTestEnvironmentRepo() *testEnvironmentRepo {
	return &testEnvironmentRepo{environments: make(map[uuid.UUID]*domain.Environment)}
}

func (m *testEnvironmentRepo) Create(_ context.Context, env *domain.Environment) error {
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	m.environments[env.ID] = env
	return nil
}

func (m *testEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	env, ok := m.environments[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return env, nil
}

func (m *testEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, env := range m.environments {
		if env.Name == name {
			return env, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testEnvironmentRepo) List(_ context.Context) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(m.environments))
	for _, env := range m.environments {
		out = append(out, *env)
	}
	return out, nil
}
func (m *testEnvironmentRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(m.environments))
	for _, env := range m.environments {
		if env.OrgID == orgID {
			out = append(out, *env)
		}
	}
	return out, nil
}

func (m *testEnvironmentRepo) Update(_ context.Context, env *domain.Environment) error {
	m.environments[env.ID] = env
	return nil
}

func (m *testEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.environments, id)
	return nil
}

func newTestMCPEnvironmentServer() (*Server, *testEnvironmentRepo) {
	envRepo := newTestEnvironmentRepo()
	registry := service.NewRegistryService(
		nil,
		envRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		&testStateRepo{},
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	server := NewServer(registry, zap.NewNop())
	return server, envRepo
}

func TestCallTool_EnvironmentListGetAndMutationsDeprecated(t *testing.T) {
	ctx := authorizedMCPContext()
	server, envRepo := newTestMCPEnvironmentServer()
	envID := uuid.New()
	envRepo.environments[envID] = &domain.Environment{
		ID:                 envID,
		Name:               "staging",
		LoomWorkerSelector: map[string]any{"tier": "small"},
		RuntimeConfig:      map[string]any{"replicas": float64(1)},
		DeployStrategy:     domain.DeployStrategyBlueGreen,
		Protected:          true,
	}

	getByIDRes, err := server.CallTool(ctx, "bahia_get_environment", map[string]interface{}{"environment_id": envID.String()})
	if err != nil {
		t.Fatalf("get by id call err: %v", err)
	}
	if getByIDRes.IsError {
		t.Fatalf("get by id returned error: %s", getByIDRes.Content[0].Text)
	}
	getByIDPayload := decodeResultMap(t, getByIDRes)
	if getByIDPayload["name"] != "staging" {
		t.Fatalf("expected environment name staging, got %v", getByIDPayload["name"])
	}
	if getByIDPayload["protected"] != true {
		t.Fatalf("expected protected=true, got %v", getByIDPayload["protected"])
	}
	if getByIDPayload["deploy_strategy"] != "blue_green" {
		t.Fatalf("expected blue_green deploy strategy, got %v", getByIDPayload["deploy_strategy"])
	}

	getByNameRes, err := server.CallTool(ctx, "bahia_get_environment", map[string]interface{}{"name": "staging"})
	if err != nil {
		t.Fatalf("get by name call err: %v", err)
	}
	if getByNameRes.IsError {
		t.Fatalf("get by name returned error: %s", getByNameRes.Content[0].Text)
	}
	if got := decodeResultMap(t, getByNameRes)["id"]; got != envID.String() {
		t.Fatalf("expected environment id %s, got %v", envID, got)
	}

	listRes, err := server.CallTool(ctx, "bahia_list_environments", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list call err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected 1 environment, got %v", listPayload["total"])
	}

	assertSignerFirstMutationError(t, server, "bahia_create_environment", map[string]interface{}{
		"name": "production",
	})
	assertSignerFirstMutationError(t, server, "bahia_update_environment", map[string]interface{}{
		"environment_id": envID.String(),
		"name":           "production",
	})
	assertSignerFirstMutationError(t, server, "bahia_delete_environment", map[string]interface{}{"environment_id": envID.String()})

	if len(envRepo.environments) != 1 {
		t.Fatalf("deprecated mutations must not change repository state, got %d environments", len(envRepo.environments))
	}
	if envRepo.environments[envID].Name != "staging" {
		t.Fatalf("deprecated update mutated environment name to %q", envRepo.environments[envID].Name)
	}
}
