package mcp

import (
	"context"
	"testing"
	"time"

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

func TestCallTool_EnvironmentListGetCreateDelete(t *testing.T) {
	ctx := context.Background()
	server, envRepo := newTestMCPEnvironmentServer()

	createRes, err := server.CallTool(ctx, "bahia_create_environment", map[string]interface{}{
		"name":            "staging",
		"protected":       true,
		"deploy_strategy": "blue_green",
	})
	if err != nil {
		t.Fatalf("create call err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	envID := createPayload["environment_id"].(string)
	if createPayload["status"] != "created" {
		t.Fatalf("expected created status, got %v", createPayload["status"])
	}
	if len(envRepo.environments) != 1 {
		t.Fatalf("expected environment to be persisted, got %d", len(envRepo.environments))
	}

	getByIDRes, err := server.CallTool(ctx, "bahia_get_environment", map[string]interface{}{"environment_id": envID})
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
	if got := decodeResultMap(t, getByNameRes)["id"]; got != envID {
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
	environments := listPayload["environments"].([]interface{})
	listed := environments[0].(map[string]interface{})
	if listed["id"] != envID || listed["name"] != "staging" {
		t.Fatalf("unexpected listed environment: %#v", listed)
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_environment", map[string]interface{}{"environment_id": envID})
	if err != nil {
		t.Fatalf("delete call err: %v", err)
	}
	if deleteRes.IsError {
		t.Fatalf("delete returned error: %s", deleteRes.Content[0].Text)
	}
	deletePayload := decodeResultMap(t, deleteRes)
	if deletePayload["status"] != "deleted" || deletePayload["environment_id"] != envID {
		t.Fatalf("unexpected delete payload: %#v", deletePayload)
	}
	if len(envRepo.environments) != 0 {
		t.Fatalf("expected environment to be deleted, got %d environments", len(envRepo.environments))
	}
}

func TestCallTool_UpdateEnvironment_AllFields(t *testing.T) {
	ctx := context.Background()
	server, envRepo := newTestMCPEnvironmentServer()
	envID := uuid.New()
	envRepo.environments[envID] = &domain.Environment{
		ID:                 envID,
		Name:               "staging",
		LoomWorkerSelector: map[string]any{"tier": "small"},
		RuntimeConfig:      map[string]any{"replicas": float64(1)},
		DeployStrategy:     domain.DeployStrategyReplace,
		Protected:          true,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	result, err := server.CallTool(ctx, "bahia_update_environment", map[string]interface{}{
		"environment_id":       envID.String(),
		"name":                 "production",
		"loom_worker_selector": map[string]interface{}{"tier": "large", "region": "us-west"},
		"runtime_config":       map[string]interface{}{"replicas": float64(3), "autoscale": true},
		"deploy_strategy":      "canary",
		"protected":            false,
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if result.IsError {
		t.Fatalf("update returned error: %s", result.Content[0].Text)
	}

	payload := decodeResultMap(t, result)
	environment := payload["environment"].(map[string]interface{})

	if environment["name"] != "production" {
		t.Fatalf("expected updated name, got %v", environment["name"])
	}
	if environment["deploy_strategy"] != "canary" {
		t.Fatalf("expected updated deploy strategy, got %v", environment["deploy_strategy"])
	}
	if protected, ok := environment["protected"].(bool); !ok || protected {
		t.Fatalf("expected protected=false, got %v", environment["protected"])
	}

	selector := environment["loom_worker_selector"].(map[string]interface{})
	if selector["tier"] != "large" {
		t.Fatalf("expected selector tier=large, got %v", selector["tier"])
	}

	runtimeConfig := environment["runtime_config"].(map[string]interface{})
	if runtimeConfig["replicas"].(float64) != 3 {
		t.Fatalf("expected runtime replicas=3, got %v", runtimeConfig["replicas"])
	}
}

func TestCallTool_UpdateEnvironment_InvalidDeployStrategy(t *testing.T) {
	ctx := context.Background()
	server, envRepo := newTestMCPEnvironmentServer()
	envID := uuid.New()
	envRepo.environments[envID] = &domain.Environment{ID: envID, Name: "staging", DeployStrategy: domain.DeployStrategyReplace}

	result, err := server.CallTool(ctx, "bahia_update_environment", map[string]interface{}{
		"environment_id":  envID.String(),
		"deploy_strategy": "invalid",
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for invalid deploy strategy")
	}
}
