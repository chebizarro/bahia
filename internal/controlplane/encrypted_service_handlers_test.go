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
		ArtifactID: artifactID, StableServiceKey: "api", Ports: []string{"127.0.0.1:8080:8080"},
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

func TestGenericAppSignerFirstEnvironmentUnitOnboardingFlow(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	revision := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	oldUnitID := uuid.New()
	oldGitSource := &domain.GitSourceBinding{
		RepositoryURL: "https://git.example/existing.git",
		Ref:           "refs/heads/main",
		Branch:        "main",
		CommitSHA:     "abc123",
	}
	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "edge-01-docker", UpdatedAt: revision,
		RuntimeConfig:  map[string]any{"type": "docker", "host_alias": "edge-01-docker", "management_mode": "direct_runtime"},
		DeployStrategy: domain.DeployStrategyReplace,
	}
	environmentRegistry := &fakeEncryptedRegistryMutations{
		environments: map[uuid.UUID]*domain.Environment{environmentID: environment},
		deploymentUnits: map[uuid.UUID][]*domain.DeploymentUnit{environmentID: {{
			ID: oldUnitID, EnvironmentID: environmentID, Key: "existing-docker", RuntimeType: domain.RuntimeTypeDocker,
			EndpointRef: "edge-01-docker", OwnershipMode: domain.OwnershipModeExternal,
			ReconcileMode: domain.ReconcileModeObserveOnly, GitSource: oldGitSource,
		}}},
	}
	environmentHandlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		Registry: environmentRegistry, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop(),
	})
	updateParams, err := json.Marshal(map[string]any{
		"id": environmentID.String(), "expected_updated_at": revision.Format(time.RFC3339Nano),
		"targeting": map[string]any{"default_unit_key": "generic-app-compose"},
		"deployment_units": []map[string]any{
			{
				"key": "existing-docker", "runtime_type": "docker",
				"endpoint_ref": "edge-01-docker", "ownership_mode": "external", "reconcile_mode": "observe_only",
				"git_source": map[string]any{"repository_url": oldGitSource.RepositoryURL, "ref": oldGitSource.Ref, "branch": oldGitSource.Branch, "commit_sha": oldGitSource.CommitSHA},
			},
			{
				"key": "generic-app-compose", "runtime_type": "compose", "endpoint_ref": "edge-01-docker",
				"compose_dir": "/srv/bahia/generic-app", "ownership_mode": "bahia_managed", "reconcile_mode": "auto_apply",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environmentHandlers.UpdateEnvironment(ctx, ContextVMRequest{
		Event: encryptedRequesterEvent(t),
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodEnvironmentUpdate, Params: updateParams},
	}); err != nil {
		t.Fatalf("environment/update: %v", err)
	}

	var oldUnit, newUnit *domain.DeploymentUnit
	for _, unit := range environmentRegistry.deploymentUnits[environmentID] {
		switch unit.Key {
		case "existing-docker":
			oldUnit = unit
		case "generic-app-compose":
			newUnit = unit
		}
	}
	if oldUnit == nil || newUnit == nil || newUnit.ID == uuid.Nil {
		t.Fatalf("updated deployment units = %#v", environmentRegistry.deploymentUnits[environmentID])
	}
	if oldUnit.GitSource == nil || oldUnit.GitSource.RepositoryURL != oldGitSource.RepositoryURL || oldUnit.GitSource.Ref != oldGitSource.Ref || oldUnit.GitSource.Branch != oldGitSource.Branch || oldUnit.GitSource.CommitSHA != oldGitSource.CommitSHA {
		t.Fatalf("pre-existing unit was not preserved by key with git_source: %#v", oldUnit)
	}
	if newUnit.RuntimeType != domain.RuntimeTypeCompose || newUnit.OwnershipMode != domain.OwnershipModeBahiaManaged {
		t.Fatalf("new generic-app unit = %#v", newUnit)
	}

	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion, ServiceName: "generic-app",
		Ports: []string{"127.0.0.1:18080:8080"}, RestartPolicy: "unless-stopped",
	}
	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "generic-app", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environmentRegistry.environments[environmentID]}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/generic-app", ImageTag: "v1", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: newUnit}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	planner, err := service.NewPublicRoutePlanner(service.PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1", DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []service.PublicRouteZone{{Name: "example.com", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{orgID}, TTL: 300}},
		Origins: []service.PublicRouteOrigin{{DeploymentUnitID: newUnit.ID, Host: "127.0.0.1", AllowedPorts: []int{18080}}},
	}, routing.StaticResolver{"cloudflare": routeAttachBackend{}})
	if err != nil {
		t.Fatalf("configure public route planner: %v", err)
	}
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, publicRoutes: planner, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)}, logger: zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)
	previewParams, err := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &newUnit.ID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed,
	})
	if err != nil {
		t.Fatal(err)
	}
	previewResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: previewParams},
	})
	if err != nil {
		t.Fatalf("service/deploy-preview: %v", err)
	}
	preview := previewResult.(map[string]any)
	previewState := preview["desired_state"].(*domain.DesiredServiceSpec)
	if previewState.DeploymentUnitID == nil || *previewState.DeploymentUnitID != newUnit.ID || previewState.DeploymentUnitKey != newUnit.Key {
		t.Fatalf("preview target = %#v", previewState)
	}

	deployParams, err := json.Marshal(dto.ServiceDeployRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &newUnit.ID,
		ArtifactID: artifactID, ExpectedDesiredStateHash: previewState.DesiredHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	deployResult, err := handlers.deploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeploy, Params: deployParams},
	})
	if err != nil {
		t.Fatalf("service/deploy: %v", err)
	}
	deployPayload := deployResult.(map[string]any)
	intentID, err := uuid.Parse(deployPayload["intent_id"].(string))
	if err != nil {
		t.Fatalf("parse deploy intent id: %v", err)
	}
	deployIntent := intentRepo.intents[intentID]
	if deployIntent == nil || deployIntent.DeploymentUnitID == nil || *deployIntent.DeploymentUnitID != newUnit.ID || deployIntent.DesiredState.DeploymentUnitKey != newUnit.Key {
		t.Fatalf("deploy intent target = %#v", deployIntent)
	}
	if err := intentRepo.UpdateStatus(ctx, intentID, domain.IntentStatusDeployed); err != nil {
		t.Fatalf("mark deploy intent deployed: %v", err)
	}

	routeParams, err := json.Marshal(dto.ServiceRouteAttachRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &newUnit.ID,
		PublicRoute: &domain.PublicRouteRequest{Hostname: "generic-app.example.com", UpstreamScheme: "http", UpstreamPort: 18080, HealthPath: "/healthz", TLS: "managed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	routeResult, err := handlers.routeAttach(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceRouteAttach, Params: routeParams},
	})
	if err != nil {
		t.Fatalf("service/route-attach: %v", err)
	}
	routePayload := routeResult.(map[string]any)
	if routePayload["deployment_unit_id"] == nil || *(routePayload["deployment_unit_id"].(*uuid.UUID)) != newUnit.ID {
		t.Fatalf("route attachment target = %#v", routePayload["deployment_unit_id"])
	}
	var routeIntent *domain.DeploymentIntent
	for _, intent := range intentRepo.intents {
		if intent.Metadata["contextvm_method"] == ContextVMMethodServiceRouteAttach {
			routeIntent = intent
		}
	}
	if routeIntent == nil || routeIntent.DesiredState == nil || routeIntent.DesiredState.PublicRoute == nil || routeIntent.DesiredState.PublicRoute.Hostname != "generic-app.example.com" {
		t.Fatalf("route attachment intent = %#v", routeIntent)
	}
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
