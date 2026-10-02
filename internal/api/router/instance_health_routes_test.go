package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type routeInstanceHealthRepo struct {
	repository.ManagedInstanceHealthRepository
}

func (routeInstanceHealthRepo) ListHealth(context.Context, repository.ManagedInstanceHealthListOptions) ([]repository.ManagedInstanceHealthListItem, error) {
	return []repository.ManagedInstanceHealthListItem{}, nil
}

type routeInstanceServiceRepo struct{ repository.ServiceRepository }

func (routeInstanceServiceRepo) GetByID(context.Context, uuid.UUID) (*domain.Service, error) {
	return nil, nil
}

type routeInstanceEnvironmentRepo struct {
	repository.EnvironmentRepository
}

func (routeInstanceEnvironmentRepo) GetByID(context.Context, uuid.UUID) (*domain.Environment, error) {
	return nil, nil
}

func TestInstanceHealthCollectionRouteIsMountedAndRequireRepoGated(t *testing.T) {
	registry := newTestRegistryService()
	deps := router.RouterDeps{
		InstanceHealth: routeInstanceHealthRepo{},
		Services:       routeInstanceServiceRepo{},
		Environments:   routeInstanceEnvironmentRepo{},
	}
	h := router.NewWithDeps(registry, zap.NewNop(), config.CORSConfig{}, nil, deps)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance-health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mounted route status=%d body=%s", w.Code, w.Body.String())
	}

	// Without ServiceRepository, the handler is not created and the route
	// is not mounted — chi returns 404 (the route does not exist).
	h = router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		InstanceHealth: routeInstanceHealthRepo{},
	})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when deps incomplete, got status=%d body=%s", w.Code, w.Body.String())
	}
}
