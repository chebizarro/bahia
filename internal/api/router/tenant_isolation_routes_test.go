package router_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	runtimeadapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/router"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type tenantIsolationSBOMRepo struct{}

func (tenantIsolationSBOMRepo) CreateSBOM(context.Context, *domain.ArtifactSBOM) error { return nil }
func (tenantIsolationSBOMRepo) GetSBOMByID(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) GetSBOMByArtifact(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) GetSBOMByHash(context.Context, string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (tenantIsolationSBOMRepo) CreatePackages(context.Context, []domain.SBOMPackage) error {
	return nil
}
func (tenantIsolationSBOMRepo) ListPackagesBySBOM(context.Context, uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (tenantIsolationSBOMRepo) SearchPackagesByName(context.Context, string, int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

type tenantIsolationRuntimeResolver struct{}

func (tenantIsolationRuntimeResolver) Resolve(*domain.Service, *domain.Environment) (runtimeadapter.Runtime, error) {
	return nil, errors.New("runtime resolution should not run for a cross-tenant request")
}

type tenantIsolationFixture struct {
	server       *httptest.Server
	aliceKey     string
	orgA         uuid.UUID
	orgB         uuid.UUID
	serviceA     uuid.UUID
	serviceB     uuid.UUID
	environmentA uuid.UUID
	environmentB uuid.UUID
	runB         uuid.UUID
	artifactB    uuid.UUID
}

func newTenantIsolationFixture(t *testing.T) tenantIsolationFixture {
	t.Helper()
	const aliceKey = "0000000000000000000000000000000000000000000000000000000000000001"
	aliceSecret, err := nostr.SecretKeyFromHex(aliceKey)
	if err != nil {
		t.Fatal(err)
	}
	alicePubkey := aliceSecret.Public().Hex()

	orgA, orgB := uuid.New(), uuid.New()
	serviceA := &domain.Service{ID: uuid.New(), OrgID: orgA, Name: "service-a"}
	serviceB := &domain.Service{ID: uuid.New(), OrgID: orgB, Name: "service-b"}
	environmentA := &domain.Environment{ID: uuid.New(), OrgID: orgA, Name: "environment-a"}
	environmentB := &domain.Environment{ID: uuid.New(), OrgID: orgB, Name: "environment-b"}

	services := newMockServiceRepo()
	services.services[serviceA.ID] = serviceA
	services.services[serviceB.ID] = serviceB
	environments := newMockEnvRepo()
	environments.envs[environmentA.ID] = environmentA
	environments.envs[environmentB.ID] = environmentB
	builds := newMockBuildRepo()
	artifacts := newMockArtifactRepo()
	artifactB := &domain.Artifact{ID: uuid.New(), ServiceID: serviceB.ID}
	artifacts.artifacts[artifactB.ID] = artifactB
	intents := newMockIntentRepo()
	intentB := &domain.DeploymentIntent{ID: uuid.New(), ServiceID: serviceB.ID, EnvironmentID: environmentB.ID}
	intents.intents[intentB.ID] = intentB
	runs := newMockRunRepo()
	runB := &domain.DeploymentRun{ID: uuid.New(), DeploymentIntentID: intentB.ID}
	runs.runs[runB.ID] = runB
	states := newMockStateRepo()
	states.states[sk(serviceA.ID, environmentA.ID)] = &domain.EnvironmentServiceState{ServiceID: serviceA.ID, EnvironmentID: environmentA.ID}
	states.states[sk(serviceB.ID, environmentB.ID)] = &domain.EnvironmentServiceState{ServiceID: serviceB.ID, EnvironmentID: environmentB.ID}

	registry := service.NewRegistryService(
		services, environments, builds, artifacts, intents, runs, newMockObsRepo(), states,
		nil, &events.NoopPublisher{}, zap.NewNop(),
	)

	lookup := &rbacMemberLookup{members: map[uuid.UUID]map[string]domain.Role{
		orgA: {alicePubkey: domain.RoleViewer},
	}}

	handler := router.NewWithDeps(registry, zap.NewNop(), config.CORSConfig{}, nil, router.RouterDeps{
		AuthMiddleware:  auth.MiddlewareConfig{Enabled: true, NIP98Validator: auth.NewNIP98Validator(auth.DefaultNIP98Config())},
		Runs:            runs,
		Services:        services,
		Environments:    environments,
		RuntimeResolver: tenantIsolationRuntimeResolver{},
		Artifacts:       artifacts,
		SBOMs:           tenantIsolationSBOMRepo{},
		SBOMImporter:    &service.SBOMOrchestrator{},
		Blossom:         &blossom.Client{},
		RBAC:            auth.NewRBAC(lookup),
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return tenantIsolationFixture{
		server:       server,
		aliceKey:     aliceKey,
		orgA:         orgA,
		orgB:         orgB,
		serviceA:     serviceA.ID,
		serviceB:     serviceB.ID,
		environmentA: environmentA.ID,
		environmentB: environmentB.ID,
		runB:         runB.ID,
		artifactB:    artifactB.ID,
	}
}

func TestSensitiveRoutesRejectCrossTenantRequests(t *testing.T) {
	fixture := newTenantIsolationFixture(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		orgID  uuid.UUID
	}{
		{name: "deployment run logs", method: http.MethodGet, path: "/api/v1/deployments/runs/" + fixture.runB.String() + "/logs"},
		{name: "live logs", method: http.MethodGet, path: "/api/v1/services/" + fixture.serviceB.String() + "/environments/" + fixture.environmentB.String() + "/logs"},
		{name: "ingest SBOM", method: http.MethodPost, path: "/api/v1/artifacts/" + fixture.artifactB.String() + "/sbom", body: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := fixture.server.URL + tt.path
			req, err := http.NewRequest(tt.method, url, strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", makeRouterNIP98HeaderWithKey(t, fixture.aliceKey, tt.method, url, []byte(tt.body)))
			if tt.orgID != uuid.Nil {
				req.Header.Set("X-Bahia-Org-ID", tt.orgID.String())
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer closeResponseBody(t, resp.Body)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}
}
