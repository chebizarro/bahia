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
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	server := NewServer(registry, zap.NewNop())
	return server, svcRepo
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
