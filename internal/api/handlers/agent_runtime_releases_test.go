package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

type captureAgentReleaseReader struct {
	orgID, serviceID uuid.UUID
	agentID, channel string
	rollback         *domain.AgentServiceRuntimeRelease
}

func (c *captureAgentReleaseReader) ListServiceReleases(_ context.Context, org, service uuid.UUID) ([]domain.AgentServiceRuntimeRelease, error) {
	c.orgID, c.serviceID = org, service
	return []domain.AgentServiceRuntimeRelease{}, nil
}
func (c *captureAgentReleaseReader) GetRollbackRelease(_ context.Context, org uuid.UUID, agent string, service uuid.UUID, channel string) (*domain.AgentServiceRuntimeRelease, error) {
	c.orgID, c.serviceID, c.agentID, c.channel = org, service, agent, channel
	return c.rollback, nil
}

func agentReleaseRequest(method, path string, org, service uuid.UUID) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("serviceId", service.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.ContextWithAuthz(ctx, &auth.AuthzContext{OrgID: org, Member: &domain.OrgMember{OrgID: org, Role: domain.RoleViewer}})
	return req.WithContext(ctx)
}

func TestAgentRuntimeReleaseHandlersPreserveTenantAndRollbackSelectors(t *testing.T) {
	org, serviceID := uuid.New(), uuid.New()
	reader := &captureAgentReleaseReader{}
	handler := NewAgentRuntimeReleaseHandler(reader)
	w := httptest.NewRecorder()
	handler.ListServiceReleases(w, agentReleaseRequest(http.MethodGet, "/services/x/runtime-releases", org, serviceID))
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	if reader.orgID != org || reader.serviceID != serviceID {
		t.Fatalf("list tenant/service=%s/%s", reader.orgID, reader.serviceID)
	}
	reader.rollback = &domain.AgentServiceRuntimeRelease{}
	w = httptest.NewRecorder()
	handler.GetRollbackRelease(w, agentReleaseRequest(http.MethodGet, "/services/x/runtime-releases/rollback?agent_id=agent-a&release_channel=stable", org, serviceID))
	if w.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", w.Code, w.Body.String())
	}
	if reader.orgID != org || reader.agentID != "agent-a" || reader.channel != "stable" {
		t.Fatalf("rollback selectors=%s/%s/%s", reader.orgID, reader.agentID, reader.channel)
	}
}
