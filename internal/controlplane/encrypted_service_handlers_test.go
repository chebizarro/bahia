package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
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
		Zones:         []service.PublicRouteZone{{Name: "example.com", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{orgID}, Protected: zoneProtected, TTL: 300}},
		Origins:       []service.PublicRouteOrigin{{DeploymentUnitID: unitID, Host: "127.0.0.1", AllowedPorts: []int{8080}}},
		InternalHTTPS: &service.InternalHTTPSPlannerConfig{Provider: "nginx", Listen: "443 ssl", CertFile: "/etc/nginx/tls/fullchain.pem", KeyFile: "/etc/nginx/tls/privkey.pem", ConfigHash: "sha256:internal", Zones: []string{"example.com"}},
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
	return f.requestForUnitWithInternal(t, serviceID, unitID, route, nil)
}

func (f *routeAttachFixture) requestForUnitWithInternal(t *testing.T, serviceID uuid.UUID, unitID *uuid.UUID, route domain.PublicRouteRequest, internal *bool) ContextVMRequest {
	t.Helper()
	params, err := json.Marshal(dto.ServiceRouteAttachRequest{
		ServiceID: serviceID, EnvironmentID: f.environmentID, DeploymentUnitID: unitID, PublicRoute: &route, Internal: internal,
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
	if attached.ArtifactID != attached.DesiredState.ArtifactID || attached.DesiredState.PublicRoute.Hostname != "api.example.com" || attached.DesiredState.PublicRoute.InternalHTTPS == nil {
		t.Fatalf("route attachment did not preserve current artifact/spec or auto-plan internal HTTPS: %#v", attached)
	}
}

func TestRouteAttachInternalFalseOptsOutOfPlannerDerivedStanza(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, false)
	internal := false
	request := fixture.requestForUnitWithInternal(t, fixture.serviceID, &fixture.unitID, validRouteAttachRequest(), &internal)
	if _, err := fixture.handlers.routeAttach(context.Background(), request); err != nil {
		t.Fatalf("routeAttach: %v", err)
	}
	for _, intent := range fixture.intentRepo.intents {
		if intent.Metadata["contextvm_method"] == ContextVMMethodServiceRouteAttach {
			if intent.DesiredState == nil || intent.DesiredState.PublicRoute == nil || intent.DesiredState.PublicRoute.InternalHTTPS != nil {
				t.Fatalf("internal opt-out intent = %#v", intent)
			}
			return
		}
	}
	t.Fatal("route attachment intent not created")
}

func TestRouteAttachAllowsDockerDeploymentUnits(t *testing.T) {
	fixture := newRouteAttachFixture(t, false, false)
	fixture.unitRepo.unit.RuntimeType = domain.RuntimeTypeDocker
	fixture.current.DesiredState.UnitRuntimeType = domain.RuntimeTypeDocker

	result, err := fixture.handlers.routeAttach(context.Background(), fixture.request(t, fixture.serviceID, validRouteAttachRequest()))
	if err != nil {
		t.Fatalf("routeAttach() error = %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok || payload["status"] != string(domain.IntentStatusApproved) {
		t.Fatalf("routeAttach result = %#v", result)
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
			name: "non-direct-runtime deployment unit",
			configure: func(f *routeAttachFixture) {
				f.unitRepo.unit.RuntimeType = domain.RuntimeTypeK8s
				f.current.DesiredState.UnitRuntimeType = domain.RuntimeTypeK8s
			},
			unitID:    func(f *routeAttachFixture) *uuid.UUID { return &f.unitID },
			wantError: "requires a direct-runtime Compose or Docker deployment unit",
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

func TestCompactDeployPreviewPayloadSize(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	envVars := map[string]string{
		"APP_ENV":                     "production",
		"DB_HOST":                     "db-primary.internal.example.com",
		"DB_PORT":                     "5432",
		"DB_NAME":                     "app_production",
		"DB_USER":                     "app_user",
		"REDIS_URL":                   "redis://redis.internal:6379/0",
		"LOG_LEVEL":                   "info",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel-collector.internal:4317",
		"METRICS_PORT":                "9090",
		"CACHE_TTL_SECONDS":           "3600",
		"MAX_CONNECTIONS":             "100",
		"RATE_LIMIT_RPS":              "500",
		"GRACEFUL_SHUTDOWN_SECONDS":   "30",
		"FEATURE_FLAG_EXPERIMENTAL":   "true",
		"TRACING_SAMPLE_RATE":         "0.1",
		"NODE_NAME":                   "worker-01",
	}

	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "production-app",
		Ports:         []string{"127.0.0.1:18080:8080", "127.0.0.1:19090:9090"},
		Environment:   envVars,
		Healthcheck: &domain.ManagedHTTPHealthcheck{
			Protocol: "http", Method: "GET", Path: "/healthz", Port: 8080,
			Interval: "30s", Timeout: "5s", Retries: 3,
		},
		RestartPolicy: "unless-stopped",
		Volumes:       []string{"/srv/bahia/production-app/data:/data", "/srv/bahia/production-app/logs:/var/log/app"},
		PullPolicy:    "always",
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)

	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "prod-docker",
		RuntimeConfig:  map[string]any{"type": "docker", "host_alias": "prod-docker", "management_mode": "direct_runtime"},
		DeployStrategy: domain.DeployStrategyReplace,
	}

	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "production-app", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environment}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID,
		ImageRepo: "ghcr.io/org/production-app", ImageTag: "v3.2.1",
		ImageDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "prod-compose", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	planner, err := service.NewPublicRoutePlanner(service.PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1", DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []service.PublicRouteZone{{Name: "example.com", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{orgID}, TTL: 300}},
		Origins: []service.PublicRouteOrigin{{DeploymentUnitID: unitID, Host: "127.0.0.1", AllowedPorts: []int{18080}}},
		InternalHTTPS: &service.InternalHTTPSPlannerConfig{
			Provider: "nginx", Listen: "443 ssl", CertFile: "/etc/nginx/tls/fullchain.pem", KeyFile: "/etc/nginx/tls/privkey.pem",
			ConfigHash: "sha256:internal", Zones: []string{"example.com"},
		},
	}, routing.StaticResolver{"cloudflare": routeAttachBackend{}})
	if err != nil {
		t.Fatalf("configure public route planner: %v", err)
	}
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, publicRoutes: planner, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
		logger:     zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)
	publicRoute := &domain.PublicRouteRequest{
		Hostname: "production-app.example.com", UpstreamScheme: "http", UpstreamPort: 18080,
		HealthPath: "/healthz", TLS: "managed",
	}

	previewParams, err := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, PublicRoute: publicRoute,
	})
	if err != nil {
		t.Fatal(err)
	}

	fullResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: previewParams},
	})
	if err != nil {
		t.Fatalf("full previewDeploy: %v", err)
	}
	fullBytes, err := json.Marshal(fullResult)
	if err != nil {
		t.Fatalf("marshal full result: %v", err)
	}
	fullSize := len(fullBytes)

	compactParams, err := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, PublicRoute: publicRoute, Compact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	compactResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: compactParams},
	})
	if err != nil {
		t.Fatalf("compact previewDeploy: %v", err)
	}
	compactBytes, err := json.Marshal(compactResult)
	if err != nil {
		t.Fatalf("marshal compact result: %v", err)
	}
	compactSize := len(compactBytes)

	t.Logf("Production-scale full: %d bytes, compact: %d bytes", fullSize, compactSize)

	if compactSize > 4096 {
		t.Fatalf("production-scale compact size %d exceeds absolute limit 4096", compactSize)
	}
	if maxCompact := fullSize * 40 / 100; compactSize > maxCompact {
		t.Fatalf("compact size %d exceeds 40%% of full size %d (max %d)", compactSize, fullSize, maxCompact)
	}
}

func TestCompactDeployPreviewPayloadSizeExtreme(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	envVars := make(map[string]string, 120)
	for i := 0; i < 120; i++ {
		envVars[fmt.Sprintf("ENV_VAR_%03d", i)] = fmt.Sprintf("value-is-%d-with-padding-to-make-it-realistic-for-production-configs", i)
	}
	volumes := make([]string, 20)
	for i := 0; i < 20; i++ {
		volumes[i] = fmt.Sprintf("/srv/mounts/vol-%02d:/container/path/vol-%02d:ro", i, i)
	}
	ports := make([]string, 10)
	for i := 0; i < 10; i++ {
		ports[i] = fmt.Sprintf("127.0.0.1:%d:%d", 10000+i, 8080+i)
	}
	labels := make(map[string]string, 60)
	for i := 0; i < 60; i++ {
		labels[fmt.Sprintf("label.key.%02d", i)] = fmt.Sprintf("label-value-%02d-with-some-length", i)
	}

	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "extreme-app",
		Ports:         ports,
		Environment:   envVars,
		Healthcheck: &domain.ManagedHTTPHealthcheck{
			Protocol: "http", Method: "GET", Path: "/healthz", Port: 8080,
		},
		RestartPolicy: "always",
		Volumes:       volumes,
		PullPolicy:    "always",
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)

	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "extreme-env",
		RuntimeConfig:  map[string]any{"management_mode": "direct_runtime"},
		DeployStrategy: domain.DeployStrategyReplace,
	}

	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "extreme-app", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environment}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID,
		ImageRepo: "ghcr.io/extreme-app", ImageTag: "v1",
		ImageDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "extreme-unit", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
		logger:     zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)

	compactParams, err := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, Compact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	compactResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: compactParams},
	})
	if err != nil {
		t.Fatalf("extreme compact previewDeploy: %v", err)
	}
	compactBytes, err := json.Marshal(compactResult)
	if err != nil {
		t.Fatalf("marshal extreme compact result: %v", err)
	}
	compactSize := len(compactBytes)

	payload := compactResult.(map[string]any)
	summary := payload["desired_state_summary"].(*desiredStateSummary)

	t.Logf("Extreme-scale compact: %d bytes (env_keys=%d/%d truncated=%v, volumes=%d/%d truncated=%v, ports=%d/%d truncated=%v, labels=%d/%d truncated=%v)",
		compactSize, len(summary.EnvKeys), summary.EnvKeyCount, summary.EnvKeysTruncated,
		len(summary.Volumes), 20, summary.VolumesTruncated,
		len(summary.Ports), 10, summary.PortsTruncated,
		len(summary.LabelKeys), summary.LabelsCount, summary.LabelKeysTruncated)

	if compactSize > 16384 {
		t.Fatalf("extreme-scale compact size %d exceeds absolute limit 16384", compactSize)
	}
	if !summary.EnvKeysTruncated {
		t.Fatalf("expected env_keys_truncated=true with 120 env keys, got false (len=%d)", len(summary.EnvKeys))
	}
	if len(summary.EnvKeys) > maxSummaryEnvKeys {
		t.Fatalf("env_keys not capped: len=%d > max=%d", len(summary.EnvKeys), maxSummaryEnvKeys)
	}
	if summary.LabelKeysTruncated {
		t.Fatalf("label_keys_truncated should be false for 60 labels (max=%d)", maxSummaryLabelKeys)
	}
}

func TestCompactDeployPreviewHashPreservation(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "hash-test", Ports: []string{"127.0.0.1:8080:8080"},
		Environment:   map[string]string{"FOO": "bar", "SECRET_TOKEN": "sensitive-value-12345"},
		RestartPolicy: "always", PullPolicy: "always",
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)
	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "hash-env", DeployStrategy: domain.DeployStrategyReplace,
		RuntimeConfig: map[string]any{"management_mode": "direct_runtime"},
	}
	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "hash-test", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environment}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID, ImageRepo: "hash-repo", ImageTag: "v1",
		ImageDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "hash-unit", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
		logger:     zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)

	fullParams, _ := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed,
	})
	fullResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: fullParams},
	})
	if err != nil {
		t.Fatalf("full previewDeploy: %v", err)
	}
	fullHash := fullResult.(map[string]any)["desired_state_hash"].(string)

	compactParams, _ := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, Compact: true,
	})
	compactResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: compactParams},
	})
	if err != nil {
		t.Fatalf("compact previewDeploy: %v", err)
	}
	compactHash := compactResult.(map[string]any)["desired_state_hash"].(string)

	if fullHash != compactHash {
		t.Fatalf("hash mismatch: full=%q compact=%q", fullHash, compactHash)
	}
}

func TestCompactDeployPreviewSummaryEvidence(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "summary-test", Ports: []string{"127.0.0.1:8080:8080"},
		Environment:   map[string]string{"A": "1", "B": "2"},
		Healthcheck:   &domain.ManagedHTTPHealthcheck{Protocol: "http", Method: "GET", Path: "/ready", Port: 8080},
		RestartPolicy: "unless-stopped",
		Volumes:       []string{"/srv/app/data:/data", "/srv/app/config:/config:ro"},
		PullPolicy:    "always",
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)
	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "summary-env", DeployStrategy: domain.DeployStrategyReplace,
		RuntimeConfig: map[string]any{"management_mode": "direct_runtime"},
	}
	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "summary-test", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environment}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID, ImageRepo: "summary-repo", ImageTag: "v1",
		ImageDigest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "summary-unit", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	planner, err := service.NewPublicRoutePlanner(service.PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1", DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []service.PublicRouteZone{{Name: "example.com", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{orgID}, TTL: 300}},
		Origins: []service.PublicRouteOrigin{{DeploymentUnitID: unitID, Host: "127.0.0.1", AllowedPorts: []int{8080}}},
		InternalHTTPS: &service.InternalHTTPSPlannerConfig{
			Provider: "nginx", Listen: "443 ssl", CertFile: "/etc/nginx/tls/fullchain.pem", KeyFile: "/etc/nginx/tls/privkey.pem",
			ConfigHash: "sha256:internal", Zones: []string{"example.com"},
		},
	}, routing.StaticResolver{"cloudflare": routeAttachBackend{}})
	if err != nil {
		t.Fatalf("configure public route planner: %v", err)
	}
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, publicRoutes: planner, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
		logger:     zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)

	compactParams, _ := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, Compact: true,
		PublicRoute: &domain.PublicRouteRequest{Hostname: "summary.example.com", UpstreamScheme: "http", UpstreamPort: 8080, HealthPath: "/ready", TLS: "managed"},
	})
	compactResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: compactParams},
	})
	if err != nil {
		t.Fatalf("compact previewDeploy: %v", err)
	}
	payload := compactResult.(map[string]any)
	summary, ok := payload["desired_state_summary"].(*desiredStateSummary)
	if !ok || summary == nil {
		t.Fatalf("desired_state_summary not present or wrong type: %T", payload["desired_state_summary"])
	}

	hasVolume := false
	for _, v := range summary.Volumes {
		if v == "/srv/app/data:/data" {
			hasVolume = true
			break
		}
	}
	if !hasVolume {
		t.Fatalf("volume /srv/app/data:/data not found in summary.Volumes: %v", summary.Volumes)
	}

	if summary.Healthcheck == nil || !summary.Healthcheck.Enabled || summary.Healthcheck.Path != "/ready" {
		t.Fatalf("healthcheck evidence missing: %#v", summary.Healthcheck)
	}

	if summary.PublicRoute == nil {
		t.Fatal("public route summary missing")
	}
	if summary.PublicRoute.Hostname != "summary.example.com" {
		t.Fatalf("public route hostname: %q", summary.PublicRoute.Hostname)
	}
	if summary.PublicRoute.DNSName != "summary.example.com" {
		t.Fatalf("DNS name: %q", summary.PublicRoute.DNSName)
	}
	if summary.PublicRoute.DNSType != "CNAME" {
		t.Fatalf("DNS type: %q", summary.PublicRoute.DNSType)
	}
	if summary.PublicRoute.DNSTTL != 300 {
		t.Fatalf("DNS TTL: %d", summary.PublicRoute.DNSTTL)
	}
	if !summary.PublicRoute.Proxied {
		t.Fatal("expected proxied=true")
	}
	if summary.PublicRoute.TLSMode != "managed" {
		t.Fatalf("TLS mode: %q", summary.PublicRoute.TLSMode)
	}
	if summary.PublicRoute.TunnelOriginURL == "" {
		t.Fatal("tunnel origin URL missing")
	}
	if summary.PublicRoute.ProxyUpstream == "" {
		t.Fatal("proxy upstream missing")
	}
	if summary.PublicRoute.ProxyHealthPath != "/ready" {
		t.Fatalf("proxy health path: %q", summary.PublicRoute.ProxyHealthPath)
	}
	if summary.PublicRoute.OperationsCount < 1 {
		t.Fatalf("operations count %d < 1", summary.PublicRoute.OperationsCount)
	}
	if summary.PublicRoute.RollbackCount < 1 {
		t.Fatalf("rollback count %d < 1", summary.PublicRoute.RollbackCount)
	}

	if summary.InternalHTTPS == nil || !summary.InternalHTTPS.Enabled {
		t.Fatal("internal_https not reflected")
	}
	if summary.InternalHTTPS.Listen == "" {
		t.Fatal("internal_https listen missing")
	}
	if summary.InternalHTTPS.CertPath == "" || summary.InternalHTTPS.KeyPath == "" {
		t.Fatal("internal_https cert/key paths missing")
	}

	if summary.RestartPolicy != "unless-stopped" {
		t.Fatalf("restart_policy: %q", summary.RestartPolicy)
	}
	if summary.PullPolicy != "always" {
		t.Fatalf("pull_policy: %q", summary.PullPolicy)
	}
	if summary.ImageRef == "" {
		t.Fatal("image_ref missing")
	}
	if _, ok := payload["policy"]; !ok {
		t.Fatal("policy not present in compact response")
	}
	if summary.EnvKeyCount != 2 || len(summary.EnvKeys) != 2 {
		t.Fatalf("env keys: count=%d keys=%v", summary.EnvKeyCount, summary.EnvKeys)
	}
}

func TestCompactDeployPreviewExcludesEnvValues(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID, artifactID, unitID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	const sentinelValue = "prod-secret-key-must-not-leak-2026"
	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "secret-test", Ports: []string{"127.0.0.1:3000:3000"},
		Environment: map[string]string{
			"NODE_ENV":  "production",
			"API_TOKEN": sentinelValue,
			"DB_URL":    "postgres://admin:" + sentinelValue + "@db.internal:5432/app",
		},
		RestartPolicy: "always", PullPolicy: "always",
	}
	managed = domain.NormalizeManagedRuntimeConfig(managed)
	environment := &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "secret-env", DeployStrategy: domain.DeployStrategyReplace,
		RuntimeConfig: map[string]any{"management_mode": "direct_runtime"},
	}
	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "secret-test", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: environment}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID, ImageRepo: "secret-repo", ImageTag: "v1",
		ImageDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{}, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "secret-unit", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	handlers := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policy, deploymentUnits: unitRepo,
		authorizer: encryptedTenantAuthorizer{services: svcRepo, environments: registry, rbac: encryptedAdminRBAC(t, orgID)},
		logger:     zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, `{}`)

	compactParams, _ := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed, Compact: true,
	})
	compactResult, err := handlers.previewDeploy(ctx, ContextVMRequest{
		Event: requestEvent,
		RPC:   ContextVMJSONRPCRequest{Method: ContextVMMethodServiceDeployPreview, Params: compactParams},
	})
	if err != nil {
		t.Fatalf("compact previewDeploy: %v", err)
	}
	compactJSON, err := json.Marshal(compactResult)
	if err != nil {
		t.Fatalf("marshal compact result: %v", err)
	}

	if strings.Contains(string(compactJSON), sentinelValue) {
		t.Fatalf("compact response leaked sentinel env value: %s", sentinelValue)
	}

	payload := compactResult.(map[string]any)
	summary, _ := payload["desired_state_summary"].(*desiredStateSummary)
	if summary == nil {
		t.Fatal("desired_state_summary missing")
	}
	for _, k := range summary.EnvKeys {
		if k == "API_TOKEN" {
			return
		}
	}
	t.Fatalf("API_TOKEN key not present in compact summary env_keys: %v", summary.EnvKeys)
}
