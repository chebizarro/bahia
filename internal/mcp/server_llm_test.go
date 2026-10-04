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

type testLLMRouteRepo struct {
	routes map[uuid.UUID]*domain.LLMRoute
}

func newTestLLMRouteRepo() *testLLMRouteRepo {
	return &testLLMRouteRepo{routes: make(map[uuid.UUID]*domain.LLMRoute)}
}

func (r *testLLMRouteRepo) Create(_ context.Context, route *domain.LLMRoute) error {
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	now := time.Now().UTC()
	if route.CreatedAt.IsZero() {
		route.CreatedAt = now
	}
	route.UpdatedAt = now
	r.routes[route.ID] = route
	return nil
}

func (r *testLLMRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, ok := r.routes[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return route, nil
}

func (r *testLLMRouteRepo) GetByName(_ context.Context, name string) (*domain.LLMRoute, error) {
	for _, route := range r.routes {
		if route.Name == name {
			return route, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *testLLMRouteRepo) List(_ context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	out := make([]domain.LLMRoute, 0, len(r.routes))
	for _, route := range r.routes {
		out = append(out, *route)
	}
	if offset >= len(out) {
		return []domain.LLMRoute{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *testLLMRouteRepo) Update(_ context.Context, route *domain.LLMRoute) error {
	route.UpdatedAt = time.Now().UTC()
	r.routes[route.ID] = route
	return nil
}

func (r *testLLMRouteRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(r.routes, id)
	return nil
}

type testLLMReleaseRepo struct {
	releases map[uuid.UUID]*domain.LLMRelease
}

func newTestLLMReleaseRepo() *testLLMReleaseRepo {
	return &testLLMReleaseRepo{releases: make(map[uuid.UUID]*domain.LLMRelease)}
}

func (r *testLLMReleaseRepo) Create(_ context.Context, release *domain.LLMRelease) error {
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	if release.CreatedAt.IsZero() {
		release.CreatedAt = time.Now().UTC()
	}
	r.releases[release.ID] = release
	return nil
}

func (r *testLLMReleaseRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRelease, error) {
	release, ok := r.releases[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return release, nil
}

func (r *testLLMReleaseRepo) GetByRouteVersion(_ context.Context, routeID uuid.UUID, version string) (*domain.LLMRelease, error) {
	for _, release := range r.releases {
		if release.RouteID == routeID && release.Version == version {
			return release, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *testLLMReleaseRepo) ListByRoute(_ context.Context, routeID uuid.UUID, limit, offset int) ([]domain.LLMRelease, error) {
	out := make([]domain.LLMRelease, 0)
	for _, release := range r.releases {
		if release.RouteID == routeID {
			out = append(out, *release)
		}
	}
	if offset >= len(out) {
		return []domain.LLMRelease{}, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newTestLLMRegistryServer() (*Server, *testLLMRouteRepo, *testLLMReleaseRepo) {
	routeRepo := newTestLLMRouteRepo()
	releaseRepo := newTestLLMReleaseRepo()
	llmRegistry := service.NewLLMRegistryService(routeRepo, releaseRepo, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{LLMRegistry: llmRegistry})
	return server, routeRepo, releaseRepo
}

func TestGetTools_IncludesLLMTools(t *testing.T) {
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{
		"bahia_llm_create_route":       false,
		"bahia_llm_update_route":       false,
		"bahia_llm_register_release":   false,
		"bahia_llm_list_routes":        false,
		"bahia_llm_list_releases":      false,
		"bahia_llm_deploy":             false,
		"bahia_llm_approve_deployment": false,
		"bahia_llm_reject_deployment":  false,
		"bahia_llm_rollback":           false,
	}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing LLM tool %s", name)
		}
	}
}
