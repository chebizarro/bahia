package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/reconcile"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestManagedDNSAndHTTPSRouteFlow(t *testing.T) {
	ctx := context.Background()
	svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo := newTestCoordinatorDeps()
	svc, env, art := createCoordinatorTestServiceEnvArtifact(t, svcRepo, envRepo, artRepo)
	svc.Name = "astillero"
	env.Name = "edge-01-production"
	art.ImageRepo = "registry.example/astillero"
	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: env.ID, Key: "astillero-compose", RuntimeType: domain.RuntimeTypeCompose,
		EndpointRef: "astillero-managed", ComposeDir: "/srv/bahia/astillero",
		ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &stubDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{unit.ID: unit}}
	registry := newTestRegistry(svcRepo, envRepo, artRepo, intentRepo, runRepo, stateRepo)
	deployedState := &domain.DesiredServiceSpec{
		SchemaVersion: domain.DesiredStateSchemaVersion, ServiceID: svc.ID, EnvironmentID: env.ID,
		DeploymentUnitID: &unit.ID, DeploymentUnitKey: unit.Key, UnitRuntimeType: unit.RuntimeType,
		ArtifactID: art.ID, StableServiceKey: "astillero", Ports: []string{"192.168.40.104:18088:8080"},
	}
	deployedState.ComputeDesiredHash()
	deployIntent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindManual,
		DesiredState: deployedState, DesiredHash: deployedState.DesiredHash,
	}
	if err := registry.CreateDeploymentIntent(ctx, deployIntent); err != nil {
		t.Fatalf("create deploy intent: %v", err)
	}
	runtimeLifecycle := &stubDeploymentRuntimeLifecycle{}
	deployCoordinator := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, runtimeLifecycle))
	if err := deployCoordinator.ExecuteDeployment(ctx, deployIntent.ID); err != nil {
		t.Fatalf("deploy Astillero artifact: %v", err)
	}
	if runtimeLifecycle.calls != 1 {
		t.Fatalf("runtime deploy calls = %d, want 1", runtimeLifecycle.calls)
	}
	state, err := stateRepo.Get(ctx, svc.ID, env.ID)
	if err != nil || state == nil {
		t.Fatalf("deployed service state = %#v, err=%v", state, err)
	}
	state.DriftStatus = domain.DriftStatusInSync
	if err := stateRepo.Upsert(ctx, state); err != nil {
		t.Fatalf("record converged service state: %v", err)
	}

	projector := reconcile.NewDNSProjector(
		svcRepo, envRepo, stateRepo,
		managedRouteObservationRepo{observation: &domain.RuntimeObservation{
			ServiceID: svc.ID, EnvironmentID: env.ID, ObservedHost: "10.20.0.88", HealthStatus: domain.HealthStatusHealthy,
		}},
		nil, nil, nil,
		config.DNSConfig{
			Enabled: true, DefaultTTL: 300,
			Zones:      []config.DNSZoneConfig{{Name: "sharegap.net", Visibility: "internal", Backend: "lan", TTL: 300}},
			Projection: config.DNSProjectionConfig{Services: true, EnvironmentZones: map[string]string{"edge-01-production": "sharegap.net"}},
		},
		zap.NewNop(),
	)
	dnsBackend := &managedRouteDNSBackend{}
	dnsReconciler := reconcile.NewDNSReconciler(
		projector,
		[]domain.DNSZone{{Name: "sharegap.net", Visibility: domain.ZoneVisibilityInternal, BackendRef: "lan", TTL: 300}},
		managedRouteDNSResolver{backend: dnsBackend}, 0, zap.NewNop(),
	)
	if err := dnsReconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("project internal DNS after deploy: %v", err)
	}
	if len(dnsBackend.records) != 1 || dnsBackend.records[0].FQDN != "astillero.sharegap.net" || dnsBackend.records[0].Value != "10.20.0.88" {
		t.Fatalf("projected Astillero DNS records = %#v", dnsBackend.records)
	}

	routingBackend := &managedRouteBackend{current: "previous.sharegap.net"}
	planner, err := service.NewPublicRoutePlanner(service.PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1", DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []service.PublicRouteZone{{Name: "sharegap.net", BackendRef: "cloudflare", AllowedOrgIDs: []uuid.UUID{svc.OrgID}, TTL: 300}},
		Origins: []service.PublicRouteOrigin{{DeploymentUnitID: unit.ID, Host: "127.0.0.1", AllowedPorts: []int{18088}}},
	}, routing.StaticResolver{"cloudflare": routingBackend})
	if err != nil {
		t.Fatalf("configure public route planner: %v", err)
	}
	plan, approvalRequired, err := planner.Plan(ctx, svc, env, deployedState, domain.PublicRouteRequest{
		Hostname: "astillero.sharegap.net", UpstreamScheme: "http", UpstreamPort: 18088, HealthPath: "/health", TLS: "managed",
	})
	if err != nil || approvalRequired {
		t.Fatalf("plan route attachment: approval=%v err=%v", approvalRequired, err)
	}
	routeState := *deployedState
	routeState.PublicRoute = plan
	routeState.ComputeDesiredHash()
	routeIntent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindEventTriggered,
		DesiredState: &routeState, DesiredHash: routeState.DesiredHash,
		Metadata: map[string]any{"contextvm_method": "service/route-attach"},
	}
	if routeIntent.DesiredHash == deployIntent.DesiredHash {
		t.Fatal("route attachment was not included in the signed desired-state hash")
	}
	if routeIntent.ArtifactID != deployIntent.ArtifactID {
		t.Fatal("route attachment did not preserve the deployed artifact")
	}
	if err := registry.CreateDeploymentIntent(ctx, routeIntent); err != nil {
		t.Fatalf("create route-only intent: %v", err)
	}
	routeCoordinator := NewCoordinator(registry, nil, &events.NoopPublisher{}, zap.NewNop(), WithDeploymentUnitRouting(unitRepo, runtimeLifecycle), WithPublicRoutes(planner))
	if err := routeCoordinator.ExecuteDeployment(ctx, routeIntent.ID); err != nil {
		t.Fatalf("apply route-only intent: %v", err)
	}
	if routingBackend.current != "astillero.sharegap.net" || routingBackend.calls != 1 || runtimeLifecycle.calls != 1 {
		t.Fatalf("route apply state=%q route calls=%d runtime calls=%d", routingBackend.current, routingBackend.calls, runtimeLifecycle.calls)
	}

	routingBackend.fail = true
	routingBackend.current = "previous.sharegap.net"
	failedRouteIntent := &domain.DeploymentIntent{
		ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: &unit.ID, ArtifactID: art.ID,
		RequestedBy: "operator", SourceKind: domain.SourceKindEventTriggered,
		DesiredState: &routeState, DesiredHash: routeState.DesiredHash,
		Metadata: map[string]any{"contextvm_method": "service/route-attach"},
	}
	if err := registry.CreateDeploymentIntent(ctx, failedRouteIntent); err != nil {
		t.Fatalf("create failing route-only intent: %v", err)
	}
	err = routeCoordinator.ExecuteDeployment(ctx, failedRouteIntent.ID)
	if err == nil || !strings.Contains(err.Error(), "previous public route restored") {
		t.Fatalf("compensated route failure = %v", err)
	}
	if routingBackend.current != "previous.sharegap.net" || routingBackend.calls != 2 || runtimeLifecycle.calls != 1 {
		t.Fatalf("failed route compensation state=%q route calls=%d runtime calls=%d", routingBackend.current, routingBackend.calls, runtimeLifecycle.calls)
	}
}

type managedRouteObservationRepo struct {
	observation *domain.RuntimeObservation
}

func (r managedRouteObservationRepo) Create(context.Context, *domain.RuntimeObservation) error {
	return nil
}
func (r managedRouteObservationRepo) GetLatest(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return r.observation, nil
}
func (r managedRouteObservationRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

type managedRouteDNSBackend struct {
	records []domain.DNSRecord
}

func (b *managedRouteDNSBackend) ListRecords(context.Context, domain.DNSZone) ([]domain.DNSRecord, error) {
	return append([]domain.DNSRecord(nil), b.records...), nil
}
func (b *managedRouteDNSBackend) SyncZone(_ context.Context, _ domain.DNSZone, records []domain.DNSRecord) error {
	b.records = append([]domain.DNSRecord(nil), records...)
	return nil
}

type managedRouteDNSResolver struct {
	backend reconcile.DNSBackend
}

func (r managedRouteDNSResolver) Resolve(ref string) (reconcile.DNSBackend, bool) {
	return r.backend, ref == "lan"
}

type managedRouteBackend struct {
	current string
	fail    bool
	calls   int
}

func (b *managedRouteBackend) Check(context.Context, *domain.DesiredPublicRoutePlan) error {
	return nil
}
func (b *managedRouteBackend) Apply(_ context.Context, plan *domain.DesiredPublicRoutePlan) error {
	b.calls++
	previous := b.current
	b.current = plan.Hostname
	if b.fail {
		b.current = previous
		return fmt.Errorf("TLS verification failed; previous public route restored")
	}
	return nil
}
