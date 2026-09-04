package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestValidateManagedDeployReviewHashRequiresExpectedHash(t *testing.T) {
	svc := &domain.Service{
		RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{
			Managed: &domain.ManagedRuntimeConfig{},
		},
	}
	if err := validateManagedDeployReviewHash(svc, ""); err == nil || !strings.Contains(err.Error(), "expected_desired_state_hash is required") {
		t.Fatalf("blank managed deploy hash error = %v", err)
	}
	if err := validateManagedDeployReviewHash(svc, "sha256:reviewed"); err != nil {
		t.Fatalf("reviewed managed deploy rejected: %v", err)
	}
}

func TestCarryForwardRollbackPublicRouteIncludesRouteInSignedHash(t *testing.T) {
	serviceID, environmentID, unitID := uuid.New(), uuid.New(), uuid.New()
	route := &domain.DesiredPublicRoutePlan{
		SchemaVersion:    domain.PublicRouteSchemaVersion,
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		DeploymentUnitID: unitID,
		Hostname:         "arcana.example.com",
	}
	supersededState := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         serviceID,
		EnvironmentID:     environmentID,
		DeploymentUnitID:  &unitID,
		DeploymentUnitKey: "arcana",
		ArtifactID:        uuid.New(),
		PublicRoute:       route,
	}
	desiredState := &domain.DesiredServiceSpec{
		SchemaVersion:     domain.DesiredStateSchemaVersion,
		ServiceID:         serviceID,
		EnvironmentID:     environmentID,
		DeploymentUnitID:  &unitID,
		DeploymentUnitKey: "arcana",
		ArtifactID:        uuid.New(),
	}
	withoutRoute := desiredState.ComputeDesiredHash()

	carryForwardRollbackPublicRoute(desiredState, &domain.DeploymentIntent{DesiredState: supersededState})

	if desiredState.PublicRoute != route {
		t.Fatal("rollback desired state did not carry the superseded public route")
	}
	if desiredState.DesiredHash == withoutRoute {
		t.Fatal("rollback desired-state hash did not include the public route")
	}
}

type routeAttachBackend struct{}

func (routeAttachBackend) Check(context.Context, *domain.DesiredPublicRoutePlan) error { return nil }
func (routeAttachBackend) Apply(context.Context, *domain.DesiredPublicRoutePlan) error { return nil }

type routeAttachDeploymentUnitRepo struct {
	unit *domain.DeploymentUnit
}

func (r *routeAttachDeploymentUnitRepo) Create(context.Context, *domain.DeploymentUnit) error {
	return nil
}

func (r *routeAttachDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	if r.unit == nil || r.unit.ID != id {
		return nil, nil
	}
	return r.unit, nil
}

func (r *routeAttachDeploymentUnitRepo) GetByEnvironmentKey(context.Context, uuid.UUID, string) (*domain.DeploymentUnit, error) {
	return nil, nil
}

func (r *routeAttachDeploymentUnitRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.DeploymentUnit, error) {
	return nil, nil
}

func (r *routeAttachDeploymentUnitRepo) ResolveDefault(context.Context, *domain.Environment) (*domain.DeploymentUnit, error) {
	return nil, nil
}

type routeAttachFixture struct {
	handlers      *encryptedServiceHandlers
	serviceID     uuid.UUID
	environmentID uuid.UUID
	unitID        uuid.UUID
	unitRepo      *routeAttachDeploymentUnitRepo
	current       *domain.DeploymentIntent
	intentRepo    *testDeploymentIntentRepo
	originalHash  string
}

func newRouteAttachFixture(t *testing.T, envProtected, zoneProtected bool) *routeAttachFixture {
	t.Helper()
	orgID, serviceID, environmentID, unitID, artifactID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", RuntimeType: domain.RuntimeTypeCompose}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, OrgID: orgID, Name: "prod", Protected: envProtected}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/api", ImageTag: "v1", ImageDigest: "sha256:abc"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{},
		&testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	desired := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: serviceID, EnvironmentID: environmentID,
		DeploymentUnitID: &unitID, DeploymentUnitKey: "prod-compose", UnitRuntimeType: domain.RuntimeTypeCompose,
		ArtifactID: artifactID, StableServiceKey: "api", Ports: []string{"127.0.0.1:18080:8080"},
	}
	originalHash := desired.ComputeDesiredHash()
	current := &domain.DeploymentIntent{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID, ArtifactID: artifactID,
		RequestedBy: "seed", SourceKind: domain.SourceKindManual, Status: domain.IntentStatusDeployed,
		DesiredState: desired, DesiredHash: desired.DesiredHash, CreatedAt: time.Now().Add(-time.Minute),
	}
	if err := registry.CreateDeploymentIntent(context.Background(), current); err != nil {
		t.Fatalf("seed deployed intent: %v", err)
	}
	planner, err := service.NewPublicRoutePlanner(service.PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1", DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []service.PublicRouteZone{{Name: "example.com", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{orgID}, Protected: zoneProtected, TTL: 300}},
		Origins: []service.PublicRouteOrigin{{DeploymentUnitID: unitID, Host: "127.0.0.1", AllowedPorts: []int{8080}}},
	}, routing.StaticResolver{"cloudflare": routeAttachBackend{}})
	if err != nil {
		t.Fatalf("new route planner: %v", err)
	}
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "prod-compose", RuntimeType: domain.RuntimeTypeCompose,
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}}
	return &routeAttachFixture{
		handlers: &encryptedServiceHandlers{
			registry: registry, policy: policy, publicRoutes: planner, deploymentUnits: unitRepo,
			authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
			logger:     zap.NewNop(),
		},
		serviceID: serviceID, environmentID: environmentID, unitID: unitID, unitRepo: unitRepo, current: current,
		intentRepo: intentRepo, originalHash: originalHash,
	}
}

func (f *routeAttachFixture) request(t *testing.T, serviceID uuid.UUID, route domain.PublicRouteRequest) ContextVMRequest {
	return f.requestForUnit(t, serviceID, &f.unitID, route)
}

func (f *routeAttachFixture) requestForUnit(t *testing.T, serviceID uuid.UUID, unitID *uuid.UUID, route domain.PublicRouteRequest) ContextVMRequest {
	t.Helper()
	params, err := json.Marshal(dto.ServiceRouteAttachRequest{
		ServiceID: serviceID, EnvironmentID: f.environmentID, DeploymentUnitID: unitID, PublicRoute: &route,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ContextVMRequest{Event: makeContextVMEvent(t, testRequesterKey, `{}`), RPC: ContextVMJSONRPCRequest{Method: ContextVMMethodServiceRouteAttach, Params: params}}
}

func validRouteAttachRequest() domain.PublicRouteRequest {
	return domain.PublicRouteRequest{Hostname: "api.example.com", UpstreamScheme: "http", UpstreamPort: 8080, HealthPath: "/healthz", TLS: "managed"}
}

func TestRouteAttachCreatesSignedIntentFromCurrentDesiredState(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, false)
	result, err := fixture.handlers.routeAttach(context.Background(), fixture.request(t, fixture.serviceID, validRouteAttachRequest()))
	if err != nil {
		t.Fatalf("routeAttach: %v", err)
	}
	payload := result.(map[string]any)
	if payload["status"] != string(domain.IntentStatusApproved) {
		t.Fatalf("result = %#v", payload)
	}
	if len(fixture.intentRepo.intents) != 2 {
		t.Fatalf("intents = %d, want baseline + route attachment", len(fixture.intentRepo.intents))
	}
	var attached *domain.DeploymentIntent
	for _, intent := range fixture.intentRepo.intents {
		if intent.Metadata["contextvm_method"] == ContextVMMethodServiceRouteAttach {
			attached = intent
		}
	}
	if attached == nil || attached.DesiredState == nil || attached.DesiredState.PublicRoute == nil {
		t.Fatalf("route attachment intent = %#v", attached)
	}
	if attached.DesiredHash == fixture.originalHash || attached.DesiredHash != attached.DesiredState.DesiredHash {
		t.Fatalf("route hash = %q state hash = %q original = %q", attached.DesiredHash, attached.DesiredState.DesiredHash, fixture.originalHash)
	}
	if attached.ArtifactID != attached.DesiredState.ArtifactID || attached.DesiredState.PublicRoute.Hostname != "api.example.com" {
		t.Fatalf("route attachment did not preserve current artifact/spec: %#v", attached)
	}
}

func TestRouteAttachRejectsIneligibleDeploymentUnitsBeforeCreatingIntent(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*routeAttachFixture)
		unitID    func(*routeAttachFixture) *uuid.UUID
		wantError string
	}{
		{
			name: "nil deployment unit",
			configure: func(f *routeAttachFixture) {
				for _, intent := range f.intentRepo.intents {
					intent.DeploymentUnitID = nil
					intent.DesiredState.DeploymentUnitID = nil
				}
			},
			unitID:    func(*routeAttachFixture) *uuid.UUID { return nil },
			wantError: "requires an explicit deployment unit",
		},
		{
			name: "missing deployment unit",
			configure: func(f *routeAttachFixture) {
				f.unitRepo.unit = nil
			},
			unitID:    func(f *routeAttachFixture) *uuid.UUID { return &f.unitID },
			wantError: "not found",
		},
		{
			name: "adopted deployment unit",
			configure: func(f *routeAttachFixture) {
				f.unitRepo.unit.OwnershipMode = domain.OwnershipModeAdopted
			},
			unitID:    func(f *routeAttachFixture) *uuid.UUID { return &f.unitID },
			wantError: "requires a Bahia-managed deployment unit",
		},
		{
			name: "non-Compose deployment unit",
			configure: func(f *routeAttachFixture) {
				f.unitRepo.unit.RuntimeType = domain.RuntimeTypeDocker
				f.current.DesiredState.UnitRuntimeType = domain.RuntimeTypeDocker
			},
			unitID:    func(f *routeAttachFixture) *uuid.UUID { return &f.unitID },
			wantError: "requires a Compose deployment unit",
		},
		{
			name: "Loom-dispatched deployment unit",
			configure: func(f *routeAttachFixture) {
				f.unitRepo.unit.RuntimeConfig = map[string]any{"dispatch_mode": "loom"}
			},
			unitID:    func(f *routeAttachFixture) *uuid.UUID { return &f.unitID },
			wantError: "requires direct runtime dispatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRouteAttachFixture(t, false, false)
			test.configure(fixture)
			_, err := fixture.handlers.routeAttach(context.Background(), fixture.requestForUnit(t, fixture.serviceID, test.unitID(fixture), validRouteAttachRequest()))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("routeAttach error = %v, want containing %q", err, test.wantError)
			}
			if len(fixture.intentRepo.intents) != 1 {
				t.Fatalf("ineligible deployment unit created an intent: %d", len(fixture.intentRepo.intents))
			}
		})
	}
}

func TestRouteAttachUnknownServiceCreatesNoIntent(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, false)
	_, err := fixture.handlers.routeAttach(context.Background(), fixture.request(t, uuid.New(), validRouteAttachRequest()))
	if err == nil || !strings.Contains(err.Error(), "service not found") {
		t.Fatalf("unknown service error = %v", err)
	}
	if len(fixture.intentRepo.intents) != 1 {
		t.Fatalf("unknown service created an intent: %d", len(fixture.intentRepo.intents))
	}
}

func TestRouteAttachAllowlistViolationCreatesNoIntent(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, false)
	route := validRouteAttachRequest()
	route.Hostname = "api.other.invalid"
	_, err := fixture.handlers.routeAttach(context.Background(), fixture.request(t, fixture.serviceID, route))
	if err == nil || !strings.Contains(err.Error(), "outside Bahia-managed public zones") {
		t.Fatalf("allowlist error = %v", err)
	}
	if len(fixture.intentRepo.intents) != 1 {
		t.Fatalf("allowlist violation created an intent: %d", len(fixture.intentRepo.intents))
	}
}

func TestRouteAttachProtectedZoneRejectsUnprotectedEnvironment(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, true)
	_, err := fixture.handlers.routeAttach(context.Background(), fixture.request(t, fixture.serviceID, validRouteAttachRequest()))
	if err == nil || !strings.Contains(err.Error(), "requires a protected environment") {
		t.Fatalf("protected zone error = %v", err)
	}
	if len(fixture.intentRepo.intents) != 1 {
		t.Fatalf("protected-zone violation created an intent: %d", len(fixture.intentRepo.intents))
	}
}
