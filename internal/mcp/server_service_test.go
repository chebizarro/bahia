package mcp

import (
	"context"
	"strings"
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

func (m *testStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
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
func (m *testServiceRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	out := make([]domain.Service, 0, len(m.services))
	for _, svc := range m.services {
		if svc.OrgID == orgID {
			out = append(out, *svc)
		}
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

func TestCallTool_ServiceListGetAndMutationsDeprecated(t *testing.T) {
	ctx := authorizedMCPContext()
	server, svcRepo := newTestMCPServiceServer()
	serviceID := uuid.New()
	svcRepo.services[serviceID] = &domain.Service{
		ID:            serviceID,
		Name:          "api",
		RepoURL:       "https://example.com/api.git",
		ArtifactRepo:  "registry.example.com/api",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeCompose,
	}

	getByIDRes, err := server.CallTool(ctx, "bahia_get_service", map[string]interface{}{"service_id": serviceID.String()})
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
	if got := decodeResultMap(t, getByNameRes)["id"]; got != serviceID.String() {
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

	assertSignerFirstMutationError(t, server, "bahia_create_service", map[string]interface{}{
		"name":          "new-api",
		"artifact_repo": "registry.example.com/new-api",
	})
	assertSignerFirstMutationError(t, server, "bahia_update_service", map[string]interface{}{
		"service_id": serviceID.String(),
		"name":       "api-v2",
	})
	assertSignerFirstMutationError(t, server, "bahia_delete_service", map[string]interface{}{"service_id": serviceID.String()})

	if len(svcRepo.services) != 1 {
		t.Fatalf("deprecated mutations must not change repository state, got %d services", len(svcRepo.services))
	}
	if svcRepo.services[serviceID].Name != "api" {
		t.Fatalf("deprecated update mutated service name to %q", svcRepo.services[serviceID].Name)
	}
}

func assertSignerFirstMutationError(t *testing.T, server *Server, tool string, args map[string]interface{}) {
	t.Helper()
	result, err := server.CallTool(authorizedMCPContext(), tool, args)
	if err != nil {
		t.Fatalf("%s call err: %v", tool, err)
	}
	if !result.IsError {
		t.Fatalf("expected %s to return an error result", tool)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "direct registry mutation") || !strings.Contains(text, "ContextVM/Nostr") {
		t.Fatalf("expected signer-first migration error for %s, got %q", tool, text)
	}
}
