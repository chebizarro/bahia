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
	items        []repository.ManagedInstanceHealthListItem
	health       *domain.ManagedInstanceHealth
	override     *domain.MaintenanceOverride
	events       []domain.ManagedInstanceHealthEvent
	attempts     []domain.RecoveryAttempt
	listOptions  repository.ManagedInstanceHealthListOptions
	listCalls    int
	eventLimit   int
	attemptLimit int
}

func (f *instanceHealthRepoFake) ListHealth(_ context.Context, options repository.ManagedInstanceHealthListOptions) ([]repository.ManagedInstanceHealthListItem, error) {
	f.listOptions = options
	f.listCalls++
	return append([]repository.ManagedInstanceHealthListItem(nil), f.items...), nil
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
func (f *instanceHealthRepoFake) ListRecentRecoveryAttempts(_ context.Context, _ domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	f.attemptLimit = limit
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

func TestInstanceHealthHandlerListPassesOrganizationFiltersAndCapsPagination(t *testing.T) {
	orgID := uuid.New()
	serviceID := uuid.New()
	environmentID := uuid.New()
	repo := &instanceHealthRepoFake{items: []repository.ManagedInstanceHealthListItem{{
		Health: domain.ManagedInstanceHealth{
			ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: serviceID, EnvironmentID: environmentID},
			Status:             domain.InstanceHealthStatusUnhealthy,
			FailureReason:      "token=secret",
		},
		MaintenanceOverride: &domain.MaintenanceOverride{Actor: "token=secret", Reason: "password=secret"},
	}}}
	h := NewInstanceHealthHandler(repo, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/instance-health?service_id="+serviceID.String()+"&environment_id="+environmentID.String()+"&unhealthy=true&limit=9999&offset=7", nil)
	req = req.WithContext(apimiddleware.ContextWithAuthz(req.Context(), &auth.AuthzContext{OrgID: orgID, Member: &domain.OrgMember{OrgID: orgID, Role: domain.RoleViewer}}))
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if repo.listCalls != 1 || repo.listOptions.OrgID != orgID || repo.listOptions.ServiceID != serviceID || repo.listOptions.EnvironmentID != environmentID || !repo.listOptions.UnhealthyOnly {
		t.Fatalf("unexpected scoped repository call: calls=%d options=%+v", repo.listCalls, repo.listOptions)
	}
	if repo.listOptions.Limit != maxInstanceHealthListLimit || repo.listOptions.Offset != 7 {
		t.Fatalf("unexpected pagination options: %+v", repo.listOptions)
	}
	var response struct {
		Data   []domain.ManagedInstanceHealth `json:"data"`
		Limit  int                            `json:"limit"`
		Offset int                            `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 1 || response.Data[0].ServiceID != serviceID || response.Limit != maxInstanceHealthListLimit || response.Offset != 7 {
		t.Fatalf("unexpected scoped response: %#v", response)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("collection response was not sanitized: %s", w.Body.String())
	}
}

func TestInstanceHealthHandlerListUsesDefaultLimitAndNonNegativeOffset(t *testing.T) {
	repo := &instanceHealthRepoFake{}
	h := NewInstanceHealthHandler(repo, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/instance-health?offset=-10", nil)
	w := httptest.NewRecorder()

	h.List(w, req)

	if w.Code != http.StatusOK || repo.listOptions.Limit != defaultInstanceHealthListLimit || repo.listOptions.Offset != 0 {
		t.Fatalf("status=%d options=%+v body=%s", w.Code, repo.listOptions, w.Body.String())
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

func TestInstanceHealthHandlerRecoveryAttemptLimitIsBounded(t *testing.T) {
	key := testInstanceKey()
	repo := &instanceHealthRepoFake{}
	h := NewInstanceHealthHandler(repo, nil, nil, nil)
	req := instanceRouteRequest(http.MethodGet, key, "/recovery-attempts?limit=9999", nil)
	w := httptest.NewRecorder()

	h.ListRecoveryAttempts(w, req)

	if w.Code != http.StatusOK || repo.attemptLimit != maxInstanceHealthHistoryLimit {
		t.Fatalf("status=%d limit=%d body=%s", w.Code, repo.attemptLimit, w.Body.String())
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
