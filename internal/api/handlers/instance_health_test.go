package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

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

func TestInstanceHealthHandlerMaintenanceForwardsAuthenticatedActorAndKey(t *testing.T) {
	key := testInstanceKey()
	override := &domain.MaintenanceOverride{ManagedInstanceKey: key, Actor: "npub1operator", Reason: "planned", CreatedAt: time.Now().UTC()}
	op := &instanceOperatorFake{setOverride: override}
	h := NewInstanceHealthHandler(op)
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
