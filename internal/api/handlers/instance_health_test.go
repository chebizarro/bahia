package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	apimiddleware "github.com/openagentsinc/bahia/internal/api/middleware"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type instanceHealthRepoFake struct {
	repository.ManagedInstanceHealthRepository
	rows       []domain.ManagedInstanceHealth
	health     *domain.ManagedInstanceHealth
	override   *domain.MaintenanceOverride
	events     []domain.ManagedInstanceHealthEvent
	attempts   []domain.RecoveryAttempt
	eventLimit int
}

func (f *instanceHealthRepoFake) ListAllHealth(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return append([]domain.ManagedInstanceHealth(nil), f.rows...), nil
}
func (f *instanceHealthRepoFake) ListHealthByService(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return append([]domain.ManagedInstanceHealth(nil), f.rows...), nil
}
func (f *instanceHealthRepoFake) ListHealthByEnvironment(context.Context, uuid.UUID) ([]domain.ManagedInstanceHealth, error) {
	return append([]domain.ManagedInstanceHealth(nil), f.rows...), nil
}
func (f *instanceHealthRepoFake) ListUnhealthy(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return append([]domain.ManagedInstanceHealth(nil), f.rows...), nil
}
func (f *instanceHealthRepoFake) GetHealth(context.Context, domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	return f.health, nil
}
func (f *instanceHealthRepoFake) GetActiveMaintenanceOverride(context.Context, domain.ManagedInstanceKey, time.Time) (*domain.MaintenanceOverride, error) {
	return f.override, nil
}
func (f *instanceHealthRepoFake) ListRecentHealthEvents(_ context.Context, _ domain.ManagedInstanceKey, limit int) ([]domain.ManagedInstanceHealthEvent, error) {
	f.eventLimit = limit
	return append([]domain.ManagedInstanceHealthEvent(nil), f.events...), nil
}
func (f *instanceHealthRepoFake) ListRecentRecoveryAttempts(context.Context, domain.ManagedInstanceKey, int) ([]domain.RecoveryAttempt, error) {
	return append([]domain.RecoveryAttempt(nil), f.attempts...), nil
}

type instanceServiceRepoFake struct {
	repository.ServiceRepository
	byID map[uuid.UUID]*domain.Service
}

func (f *instanceServiceRepoFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	return f.byID[id], nil
}

type instanceEnvironmentRepoFake struct {
	repository.EnvironmentRepository
	byID map[uuid.UUID]*domain.Environment
}

func (f *instanceEnvironmentRepoFake) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return f.byID[id], nil
}

type instanceOperatorFake struct {
	setKey      domain.ManagedInstanceKey
	setActor    string
	setReason   string
	clearKey    domain.ManagedInstanceKey
	clearActor  string
	setOverride *domain.MaintenanceOverride
}

func (f *instanceOperatorFake) SetMaintenanceOverride(_ context.Context, key domain.ManagedInstanceKey, actor, reason string, _ *time.Time) (*domain.MaintenanceOverride, error) {
	f.setKey, f.setActor, f.setReason = key, actor, reason
	return f.setOverride, nil
}
func (f *instanceOperatorFake) ClearMaintenanceOverride(_ context.Context, key domain.ManagedInstanceKey, actor string) error {
	f.clearKey, f.clearActor = key, actor
	return nil
}

func TestInstanceHealthHandlerListFiltersOrganizationAndSanitizes(t *testing.T) {
	orgID, otherOrg := uuid.New(), uuid.New()
	serviceID, otherServiceID := uuid.New(), uuid.New()
	environmentID, otherEnvironmentID := uuid.New(), uuid.New()
	repo := &instanceHealthRepoFake{rows: []domain.ManagedInstanceHealth{
		{ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID}, Status: domain.InstanceHealthStatusUnhealthy, FailureReason: "token=secret"},
		{ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: otherServiceID, EnvironmentID: otherEnvironmentID}, Status: domain.InstanceHealthStatusHealthy},
	}}
	h := NewInstanceHealthHandler(repo,
		&instanceServiceRepoFake{byID: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, OrgID: orgID}, otherServiceID: {ID: otherServiceID, OrgID: otherOrg}}},
		&instanceEnvironmentRepoFake{byID: map[uuid.UUID]*domain.Environment{environmentID: {ID: environmentID, OrgID: orgID}, otherEnvironmentID: {ID: otherEnvironmentID, OrgID: otherOrg}}}, nil)
	req := httptest.NewRequest(http.MethodGet, "/instance-health?unhealthy=true", nil)
	req = req.WithContext(apimiddleware.ContextWithAuthz(req.Context(), &auth.AuthzContext{OrgID: orgID, Member: &domain.OrgMember{OrgID: orgID, Role: domain.RoleViewer}}))
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data []domain.ManagedInstanceHealth `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ServiceID != serviceID {
		t.Fatalf("unexpected scoped rows: %#v", response.Data)
	}
	if strings.Contains(response.Data[0].FailureReason, "secret") {
		t.Fatalf("failure reason was not sanitized: %q", response.Data[0].FailureReason)
	}
}

func TestInstanceHealthHandlerGetIncludesActiveOverride(t *testing.T) {
	key := testInstanceKey()
	repo := &instanceHealthRepoFake{
		health:   &domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusStopped, RestartCount: 2, LastRecoveryAttempt: &domain.RecoveryAttempt{Evidence: "password=hunter2"}},
		override: &domain.MaintenanceOverride{ManagedInstanceKey: key, Actor: "npub1operator", Reason: "maintenance", CreatedAt: time.Now().UTC()},
	}
	h := NewInstanceHealthHandler(repo, nil, nil, nil)
	req := instanceRouteRequest(http.MethodGet, key, "/health", nil)
	w := httptest.NewRecorder()

	h.Get(w, req)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "maintenance_override") || strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestInstanceHealthHandlerHistoryLimitIsBoundedAndSanitized(t *testing.T) {
	key := testInstanceKey()
	repo := &instanceHealthRepoFake{events: []domain.ManagedInstanceHealthEvent{{ManagedInstanceKey: key, Reason: "authorization: Bearer secret", Evidence: "token=secret"}}}
	h := NewInstanceHealthHandler(repo, nil, nil, nil)
	req := instanceRouteRequest(http.MethodGet, key, "/events?limit=9999", nil)
	w := httptest.NewRecorder()

	h.ListEvents(w, req)

	if w.Code != http.StatusOK || repo.eventLimit != maxInstanceHealthHistoryLimit || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("status=%d limit=%d body=%s", w.Code, repo.eventLimit, w.Body.String())
	}
}

func TestInstanceHealthHandlerMaintenanceForwardsAuthenticatedActorAndKey(t *testing.T) {
	key := testInstanceKey()
	override := &domain.MaintenanceOverride{ManagedInstanceKey: key, Actor: "npub1operator", Reason: "planned", CreatedAt: time.Now().UTC()}
	op := &instanceOperatorFake{setOverride: override}
	h := NewInstanceHealthHandler(&instanceHealthRepoFake{}, nil, nil, op)
	req := instanceRouteRequest(http.MethodPost, key, "/maintenance", strings.NewReader(`{"reason":"planned"}`))
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{Subject: "npub1operator", PubKey: "operator", Method: auth.MethodNIP98}))
	w := httptest.NewRecorder()

	h.SetMaintenance(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if op.setKey != key || op.setActor != "npub1operator" || op.setReason != "planned" {
		t.Fatalf("unexpected operator call: key=%+v actor=%q reason=%q", op.setKey, op.setActor, op.setReason)
	}

	clearReq := instanceRouteRequest(http.MethodDelete, key, "/maintenance", nil)
	clearReq = clearReq.WithContext(auth.ContextWithPrincipal(clearReq.Context(), &auth.Principal{Subject: "npub1operator", PubKey: "operator", Method: auth.MethodNIP98}))
	clearW := httptest.NewRecorder()
	h.ClearMaintenance(clearW, clearReq)
	if clearW.Code != http.StatusOK || op.clearKey != key || op.clearActor != "npub1operator" {
		t.Fatalf("clear status=%d key=%+v actor=%q body=%s", clearW.Code, op.clearKey, op.clearActor, clearW.Body.String())
	}
}

func testInstanceKey() domain.ManagedInstanceKey {
	return domain.ManagedInstanceKey{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DeploymentUnitID: uuid.New(), RuntimeTargetName: "edge-agent"}
}

func instanceRouteRequest(method string, key domain.ManagedInstanceKey, suffix string, body *strings.Reader) *http.Request {
	var requestBody *strings.Reader
	if body == nil {
		requestBody = strings.NewReader("")
	} else {
		requestBody = body
	}
	separator := "?"
	if strings.Contains(suffix, "?") {
		separator = "&"
	}
	req := httptest.NewRequest(method, suffix+separator+"runtime_target_name="+key.RuntimeTargetName, requestBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("serviceId", key.ServiceID.String())
	rctx.URLParams.Add("envId", key.EnvironmentID.String())
	rctx.URLParams.Add("deploymentUnitId", key.DeploymentUnitID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}
