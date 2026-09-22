package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
)

type vmHTTPMembers struct{ org uuid.UUID }

func (m vmHTTPMembers) GetMember(_ context.Context, org uuid.UUID, key string) (*domain.OrgMember, error) {
	if org != m.org {
		return nil, repository.ErrNotFound
	}
	return &domain.OrgMember{OrgID: org, Pubkey: key, Role: domain.RoleViewer}, nil
}
func (m vmHTTPMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type vmHTTPResources struct {
	repository.PersistentVMDeploymentRepository
	vm    domain.PersistentVMDeployment
	calls int
}

func (r *vmHTTPResources) Get(_ context.Context, org, id uuid.UUID) (*domain.PersistentVMDeployment, error) {
	r.calls++
	if org != r.vm.OrgID || id != r.vm.ID {
		return nil, repository.ErrNotFound
	}
	return &r.vm, nil
}
func (r *vmHTTPResources) List(_ context.Context, org uuid.UUID, limit, offset int) ([]domain.PersistentVMDeployment, error) {
	r.calls++
	if org != r.vm.OrgID {
		return nil, repository.ErrNotFound
	}
	return []domain.PersistentVMDeployment{r.vm}, nil
}

type vmHTTPRepo struct {
	repository.VirtualizationRepository
	resources *vmHTTPResources
}

func (r vmHTTPRepo) Deployments() repository.PersistentVMDeploymentRepository { return r.resources }
func TestVirtualizationHTTPAuthorizationAndPublicData(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	resources := &vmHTTPResources{vm: domain.PersistentVMDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: 1}, LifecycleClass: domain.VMLifecyclePersistent, Provider: domain.VMProviderLibvirt, Purpose: domain.VMPurposeDesktop, DesiredPower: domain.VMDesiredRunning, DisplayName: "password=private-sentinel"}}
	h := &VirtualizationHandler{Query: readmodel.VirtualizationQuery{Repository: vmHTTPRepo{resources: resources}}, RBAC: auth.NewRBAC(vmHTTPMembers{org})}
	mux := chi.NewRouter()
	mux.Get("/vms/{id}", h.Read(domain.PersistentVMResource, false))
	mux.Get("/vms", h.Read(domain.PersistentVMResource, true))
	for _, tc := range []struct {
		name, url string
		principal *auth.Principal
		status    int
	}{
		{"anonymous", "/vms/" + id.String() + "?org_id=" + org.String(), nil, 401},
		{"auth_disabled", "/vms?org_id=" + org.String(), &auth.Principal{PubKey: strings.Repeat("a", 64)}, 401},
		{"system_without_key", "/vms?org_id=" + org.String(), auth.SystemPrincipal("test"), 401},
		{"cross_tenant", "/vms?org_id=" + uuid.New().String(), &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}, 403},
		{"invalid_pagination", "/vms?org_id=" + org.String() + "&limit=101", &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}, 400},
		{"get", "/vms/" + id.String() + "?org_id=" + org.String(), &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}, 200},
		{"list", "/vms?org_id=" + org.String(), &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}, 200},
		{"not_found", "/vms/" + uuid.New().String() + "?org_id=" + org.String(), &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.principal != nil {
				r = r.WithContext(auth.ContextWithPrincipal(r.Context(), tc.principal))
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-sentinel") {
				t.Fatal("private data leaked")
			}
		})
	}
	if resources.calls != 3 {
		t.Fatalf("unauthorized repository access: %d", resources.calls)
	}
	h.RBAC = nil
	r := httptest.NewRequest("GET", "/vms?org_id="+org.String(), nil)
	r = r.WithContext(auth.ContextWithPrincipal(r.Context(), &auth.Principal{Method: auth.MethodNIP98, PubKey: strings.Repeat("a", 64)}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
