package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestLLMOperationalRESTRoutesDisabledByDefault(t *testing.T) {
	cfg := config.Defaults()
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:      cfg,
		LLMRegistry: &service.LLMRegistryService{},
	})

	assertLLMOperationalRESTNotMounted(t, h)
	assertLLMRouteAndReleaseCreationRESTNotMounted(t, h)

	t.Run("read route remains mounted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/llm/routes", nil)
		w := httptest.NewRecorder()

		h.ServeHTTP(w, req)

		if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
			t.Fatalf("LLM read route should remain mounted, got status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestLLMRouteUpdateRESTRemainsMounted(t *testing.T) {
	routeID := uuid.New()
	routeRepo := &llmRouteRepoForRouterTest{byID: map[uuid.UUID]*domain.LLMRoute{
		routeID: &domain.LLMRoute{ID: routeID, Name: "chat-prod", Description: "old"},
	}}
	llmRegistry := service.NewLLMRegistryService(routeRepo, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop())
	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:      config.Defaults(),
		LLMRegistry: llmRegistry,
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/llm/routes/"+routeID.String(), strings.NewReader(`{"description":"new"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT /llm/routes/{id} status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if routeRepo.byID[routeID].Description != "new" {
		t.Fatalf("route description = %q, want update through retained route", routeRepo.byID[routeID].Description)
	}
}

func TestLLMOperationalRESTRoutesNotMountedEvenWhenCompatibilityFlagEnabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Enabled = true
	cfg.LLM.AllowOperationalREST = true

	h := router.NewWithDeps(nil, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		Config:      cfg,
		LLMRegistry: &service.LLMRegistryService{},
	})

	assertLLMOperationalRESTNotMounted(t, h)
	assertLLMRouteAndReleaseCreationRESTNotMounted(t, h)
}

type llmRouteRepoForRouterTest struct {
	byID map[uuid.UUID]*domain.LLMRoute
}

func (r *llmRouteRepoForRouterTest) Create(ctx context.Context, route *domain.LLMRoute) error {
	if route.ID == uuid.Nil {
		route.ID = uuid.New()
	}
	copy := *route
	r.byID[route.ID] = &copy
	return nil
}

func (r *llmRouteRepoForRouterTest) GetByID(ctx context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	if route := r.byID[id]; route != nil {
		copy := *route
		return &copy, nil
	}
	return nil, nil
}

func (r *llmRouteRepoForRouterTest) GetByName(ctx context.Context, name string) (*domain.LLMRoute, error) {
	for _, route := range r.byID {
		if route.Name == name {
			copy := *route
			return &copy, nil
		}
	}
	return nil, nil
}

func (r *llmRouteRepoForRouterTest) List(ctx context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	routes := make([]domain.LLMRoute, 0, len(r.byID))
	for _, route := range r.byID {
		routes = append(routes, *route)
	}
	return routes, nil
}

func (r *llmRouteRepoForRouterTest) Update(ctx context.Context, route *domain.LLMRoute) error {
	if r.byID[route.ID] == nil {
		return repository.ErrNotFound
	}
	copy := *route
	r.byID[route.ID] = &copy
	return nil
}

func (r *llmRouteRepoForRouterTest) Delete(ctx context.Context, id uuid.UUID) error {
	delete(r.byID, id)
	return nil
}

func assertLLMRouteAndReleaseCreationRESTNotMounted(t *testing.T, h http.Handler) {
	t.Helper()
	removedRoutes := []string{
		"/api/v1/llm/routes",
		"/api/v1/llm/routes/11111111-1111-1111-1111-111111111111/releases",
	}
	for _, path := range removedRoutes {
		t.Run("removed "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d, want 404 or 405, body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func assertLLMOperationalRESTNotMounted(t *testing.T, h http.Handler) {
	t.Helper()
	operationalRoutes := []string{
		"/api/v1/llm/intents",
		"/api/v1/llm/intents/11111111-1111-1111-1111-111111111111/approve",
		"/api/v1/llm/intents/11111111-1111-1111-1111-111111111111/reject",
		"/api/v1/llm/rollback",
		"/api/v1/llm/hosts",
		"/api/v1/llm/observations",
	}
	for _, path := range operationalRoutes {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404, body=%s", w.Code, w.Body.String())
			}
		})
	}
}
