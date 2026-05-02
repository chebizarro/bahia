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

type testServiceRepo struct {
	services map[uuid.UUID]*domain.Service
}

type testStateRepo struct{}

func (m *testStateRepo) Upsert(_ context.Context, _ *domain.EnvironmentServiceState) error {
	return nil
}

func (m *testStateRepo) Get(_ context.Context, _, _ uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return nil, repository.ErrNotFound
}

func (m *testStateRepo) ListByEnvironment(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *testStateRepo) ListByService(_ context.Context, _ uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *testStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *testStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func newTestServiceRepo() *testServiceRepo {
	return &testServiceRepo{services: make(map[uuid.UUID]*domain.Service)}
}

func (m *testServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	m.services[svc.ID] = svc
	return nil
}

func (m *testServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	svc, ok := m.services[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return svc, nil
}

func (m *testServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for _, svc := range m.services {
		if svc.Name == name {
			return svc, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testServiceRepo) List(_ context.Context) ([]domain.Service, error) {
	out := make([]domain.Service, 0, len(m.services))
	for _, svc := range m.services {
		out = append(out, *svc)
	}
	return out, nil
}

func (m *testServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	m.services[svc.ID] = svc
	return nil
}

func (m *testServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.services, id)
	return nil
}

func newTestMCPServiceServer() (*Server, *testServiceRepo) {
	svcRepo := newTestServiceRepo()
	registry := service.NewRegistryService(
		svcRepo,
		nil,
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
	return server, svcRepo
}

func TestCallTool_ServiceListGetCreateDelete(t *testing.T) {
	ctx := context.Background()
	server, svcRepo := newTestMCPServiceServer()

	createRes, err := server.CallTool(ctx, "bahia_create_service", map[string]interface{}{
		"name":          "api",
		"artifact_repo": "registry.example.com/api",
		"repo_url":      "https://example.com/api.git",
		"runtime_type":  "compose",
	})
	if err != nil {
		t.Fatalf("create call err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	serviceID := createPayload["service_id"].(string)
	if createPayload["status"] != "created" {
		t.Fatalf("expected created status, got %v", createPayload["status"])
	}
	if len(svcRepo.services) != 1 {
		t.Fatalf("expected service to be persisted, got %d", len(svcRepo.services))
	}

	getByIDRes, err := server.CallTool(ctx, "bahia_get_service", map[string]interface{}{"service_id": serviceID})
	if err != nil {
		t.Fatalf("get by id call err: %v", err)
	}
	if getByIDRes.IsError {
		t.Fatalf("get by id returned error: %s", getByIDRes.Content[0].Text)
	}
	getByIDPayload := decodeResultMap(t, getByIDRes)
	if getByIDPayload["name"] != "api" {
		t.Fatalf("expected service name api, got %v", getByIDPayload["name"])
	}
	if getByIDPayload["artifact_repo"] != "registry.example.com/api" {
		t.Fatalf("unexpected artifact_repo: %v", getByIDPayload["artifact_repo"])
	}
	if getByIDPayload["repo_url"] != "https://example.com/api.git" {
		t.Fatalf("unexpected repo_url: %v", getByIDPayload["repo_url"])
	}
	if getByIDPayload["runtime_type"] != "compose" {
		t.Fatalf("unexpected runtime_type: %v", getByIDPayload["runtime_type"])
	}

	getByNameRes, err := server.CallTool(ctx, "bahia_get_service", map[string]interface{}{"name": "api"})
	if err != nil {
		t.Fatalf("get by name call err: %v", err)
	}
	if getByNameRes.IsError {
		t.Fatalf("get by name returned error: %s", getByNameRes.Content[0].Text)
	}
	if got := decodeResultMap(t, getByNameRes)["id"]; got != serviceID {
		t.Fatalf("expected service id %s, got %v", serviceID, got)
	}

	listRes, err := server.CallTool(ctx, "bahia_list_services", map[string]interface{}{})
	if err != nil {
		t.Fatalf("list call err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected 1 service, got %v", listPayload["total"])
	}
	services := listPayload["services"].([]interface{})
	listed := services[0].(map[string]interface{})
	if listed["id"] != serviceID || listed["name"] != "api" {
		t.Fatalf("unexpected listed service: %#v", listed)
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_service", map[string]interface{}{"service_id": serviceID})
	if err != nil {
		t.Fatalf("delete call err: %v", err)
	}
	if deleteRes.IsError {
		t.Fatalf("delete returned error: %s", deleteRes.Content[0].Text)
	}
	deletePayload := decodeResultMap(t, deleteRes)
	if deletePayload["status"] != "deleted" || deletePayload["service_id"] != serviceID {
		t.Fatalf("unexpected delete payload: %#v", deletePayload)
	}
	if len(svcRepo.services) != 0 {
		t.Fatalf("expected service to be deleted, got %d services", len(svcRepo.services))
	}
}

func TestCallTool_UpdateService_AllFields(t *testing.T) {
	ctx := context.Background()
	server, svcRepo := newTestMCPServiceServer()
	serviceID := uuid.New()
	svcRepo.services[serviceID] = &domain.Service{
		ID:            serviceID,
		Name:          "api",
		RepoURL:       "https://example.com/old.git",
		ArtifactRepo:  "registry.example.com/api",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeDocker,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	result, err := server.CallTool(ctx, "bahia_update_service", map[string]interface{}{
		"service_id":     serviceID.String(),
		"name":           "api-v2",
		"repo_url":       "https://example.com/new.git",
		"artifact_repo":  "registry.example.com/api-v2",
		"default_branch": "develop",
		"runtime_type":   "compose",
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if result.IsError {
		t.Fatalf("update returned error: %s", result.Content[0].Text)
	}

	payload := decodeResultMap(t, result)
	service := payload["service"].(map[string]interface{})

	if service["name"] != "api-v2" {
		t.Fatalf("expected updated name, got %v", service["name"])
	}
	if service["repo_url"] != "https://example.com/new.git" {
		t.Fatalf("expected updated repo_url, got %v", service["repo_url"])
	}
	if service["artifact_repo"] != "registry.example.com/api-v2" {
		t.Fatalf("expected updated artifact_repo, got %v", service["artifact_repo"])
	}
	if service["default_branch"] != "develop" {
		t.Fatalf("expected updated default_branch, got %v", service["default_branch"])
	}
	if service["runtime_type"] != "compose" {
		t.Fatalf("expected updated runtime_type, got %v", service["runtime_type"])
	}
}

func TestCallTool_UpdateService_InvalidRuntimeType(t *testing.T) {
	ctx := context.Background()
	server, svcRepo := newTestMCPServiceServer()
	serviceID := uuid.New()
	svcRepo.services[serviceID] = &domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}

	result, err := server.CallTool(ctx, "bahia_update_service", map[string]interface{}{
		"service_id":   serviceID.String(),
		"runtime_type": "invalid",
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for invalid runtime_type")
	}
}
