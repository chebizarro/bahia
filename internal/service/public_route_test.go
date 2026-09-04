package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/domain"
)

type routeBackendStub struct {
	checkErr error
	applyErr error
	checked  *domain.DesiredPublicRoutePlan
	applied  *domain.DesiredPublicRoutePlan
}

func (s *routeBackendStub) Check(_ context.Context, plan *domain.DesiredPublicRoutePlan) error {
	s.checked = plan
	return s.checkErr
}
func (s *routeBackendStub) Apply(_ context.Context, plan *domain.DesiredPublicRoutePlan) error {
	s.applied = plan
	return s.applyErr
}

func publicRouteFixture(t *testing.T) (*PublicRoutePlanner, *routeBackendStub, *domain.Service, *domain.Environment, *domain.DesiredServiceSpec) {
	t.Helper()
	orgID := uuid.New()
	serviceID := uuid.New()
	environmentID := uuid.New()
	unitID := uuid.New()
	backend := &routeBackendStub{}
	planner, err := NewPublicRoutePlanner(PublicRoutePlannerConfig{
		Provider: "cloudflare_tunnel", TunnelRef: "tunnel-1",
		DNSTarget: "tunnel.example.net", ConfigHash: "sha256:config",
		Zones:   []PublicRouteZone{{Name: "example.com", BackendRef: "edge", AllowedOrgIDs: []uuid.UUID{orgID}, Protected: true, TTL: 1}},
		Origins: []PublicRouteOrigin{{DeploymentUnitID: unitID, Host: "edge-01.internal", AllowedPorts: []int{8080}}},
	}, routing.StaticResolver{"edge": backend})
	if err != nil {
		t.Fatalf("NewPublicRoutePlanner: %v", err)
	}
	return planner, backend,
		&domain.Service{ID: serviceID, OrgID: orgID},
		&domain.Environment{ID: environmentID, OrgID: orgID, Protected: true},
		&domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID, Ports: []string{"127.0.0.1:8080:8080"}}
}

func routeRequest() domain.PublicRouteRequest {
	return domain.PublicRouteRequest{
		Hostname: "arcana.example.com", UpstreamScheme: "http",
		UpstreamPort: 8080, HealthPath: "/healthz", TLS: "managed",
	}
}

func TestPublicRoutePlannerPlansExactProtectedRoute(t *testing.T) {
	planner, backend, svc, env, desired := publicRouteFixture(t)
	plan, protected, err := planner.Plan(context.Background(), svc, env, desired, routeRequest())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !protected || backend.checked != plan {
		t.Fatal("protected route was not checked by its configured backend")
	}
	if plan.DNS.Name != "arcana.example.com" || plan.DNS.Value != "tunnel.example.net" || plan.Tunnel.OriginURL != "http://edge-01.internal:8080" {
		t.Fatalf("unexpected exact route plan: %#v", plan)
	}
	if len(plan.Operations) != 4 || plan.Operations[0].Resource != "application" || len(plan.Rollback) != 3 {
		t.Fatalf("ordering/rollback not represented in plan: %#v", plan)
	}
}

func TestPublicRoutePlannerPolicyAndCollisionValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(*routeBackendStub, *domain.Service, *domain.Environment, *domain.DesiredServiceSpec, *domain.PublicRouteRequest)
		want string
	}{
		{"zone ownership", func(_ *routeBackendStub, svc *domain.Service, _ *domain.Environment, _ *domain.DesiredServiceSpec, _ *domain.PublicRouteRequest) {
			svc.OrgID = uuid.New()
		}, "does not own"},
		{"protected zone", func(_ *routeBackendStub, _ *domain.Service, env *domain.Environment, _ *domain.DesiredServiceSpec, _ *domain.PublicRouteRequest) {
			env.Protected = false
		}, "requires a protected"},
		{"target port allowlist", func(_ *routeBackendStub, _ *domain.Service, _ *domain.Environment, _ *domain.DesiredServiceSpec, req *domain.PublicRouteRequest) {
			req.UpstreamPort = 9090
		}, "not allowed"},
		{"port exposure", func(_ *routeBackendStub, _ *domain.Service, _ *domain.Environment, desired *domain.DesiredServiceSpec, _ *domain.PublicRouteRequest) {
			desired.Ports = nil
		}, "not exposed"},
		{"provider collision", func(backend *routeBackendStub, _ *domain.Service, _ *domain.Environment, _ *domain.DesiredServiceSpec, _ *domain.PublicRouteRequest) {
			backend.checkErr = errors.New("hostname collision")
		}, "hostname collision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			planner, backend, svc, env, desired := publicRouteFixture(t)
			request := routeRequest()
			tt.edit(backend, svc, env, desired, &request)
			_, _, err := planner.Plan(context.Background(), svc, env, desired, request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDesiredExposesPortUsesPublishedHostPort(t *testing.T) {
	tests := []struct {
		name        string
		ports       []string
		healthcheck *domain.HealthcheckConfig
		target      int
		want        bool
	}{
		{name: "ip host container", ports: []string{"192.168.40.10:19090:9090"}, target: 19090, want: true},
		{name: "host container", ports: []string{"19090:9090"}, target: 19090, want: true},
		{name: "bare container port", ports: []string{"9090"}, target: 9090, want: false},
		{name: "Astillero published host port", ports: []string{"192.168.40.104:18088:8080"}, target: 18088, want: true},
		{name: "Astillero container port", ports: []string{"192.168.40.104:18088:8080"}, target: 8080, want: false},
		{
			name:        "container healthcheck port does not prove host reachability",
			healthcheck: &domain.HealthcheckConfig{Port: 8080},
			target:      8080,
			want:        false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			desired := &domain.DesiredServiceSpec{Ports: test.ports, Healthcheck: test.healthcheck}
			if got := desiredExposesPort(desired, test.target); got != test.want {
				t.Fatalf("desiredExposesPort(%v, %d) = %t, want %t", test.ports, test.target, got, test.want)
			}
		})
	}
}

func TestPublicRoutePlannerAstilleroUsesPublishedHostPort(t *testing.T) {
	planner, _, svc, env, desired := publicRouteFixture(t)
	desired.Ports = []string{"192.168.40.104:18088:8080"}
	planner.cfg.Origins[0].AllowedPorts = []int{18088, 8080}

	request := routeRequest()
	request.UpstreamPort = 18088
	plan, _, err := planner.Plan(context.Background(), svc, env, desired, request)
	if err != nil {
		t.Fatalf("published host port rejected: %v", err)
	}
	if plan.Tunnel.OriginURL != "http://edge-01.internal:18088" {
		t.Fatalf("origin URL = %q", plan.Tunnel.OriginURL)
	}

	request.UpstreamPort = 8080
	if _, _, err := planner.Plan(context.Background(), svc, env, desired, request); err == nil || !strings.Contains(err.Error(), "not exposed") {
		t.Fatalf("container-only port error = %v", err)
	}
}

func TestPublicRoutePlannerApplyFailsClosedOnConfigurationChange(t *testing.T) {
	planner, backend, svc, env, desired := publicRouteFixture(t)
	plan, _, err := planner.Plan(context.Background(), svc, env, desired, routeRequest())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	plan.ProviderConfigHash = "sha256:changed"
	if err := planner.Apply(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("Apply error = %v", err)
	}
	if backend.applied != nil {
		t.Fatal("backend received a stale signed plan")
	}
}
