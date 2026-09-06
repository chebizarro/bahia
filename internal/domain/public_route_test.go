package domain

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validPublicRoutePlan() *DesiredPublicRoutePlan {
	serviceID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	environmentID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	unitID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	return &DesiredPublicRoutePlan{
		SchemaVersion: PublicRouteSchemaVersion,
		ServiceID:     serviceID, EnvironmentID: environmentID, DeploymentUnitID: unitID,
		Hostname: "arcana.example.com", Zone: "example.com", BackendRef: "edge",
		Provider: "cloudflare_tunnel", ProviderConfigHash: "sha256:config",
		DNS: DesiredPublicRouteDNS{
			Type: "CNAME", Name: "arcana.example.com", Value: "tunnel.example.net",
			TTL: 1, Proxied: true, SourceCoordinate: PublicRouteCoordinate(serviceID, environmentID, unitID),
		},
		Tunnel: DesiredPublicRouteTunnel{TunnelRef: "tunnel-1", Hostname: "arcana.example.com", OriginURL: "http://edge-01.internal:8080"},
		Proxy: DesiredPublicRouteProxy{
			HostMatch: "arcana.example.com", UpstreamScheme: "http",
			UpstreamHost: "edge-01.internal", UpstreamPort: 8080, HealthPath: "/healthz",
		},
		TLS:        DesiredPublicRouteTLS{Mode: "managed", Provider: "cloudflare"},
		Operations: []DesiredPublicRouteChange{{Order: 1, Resource: "application", Action: "apply"}, {Order: 2, Resource: "edge", Action: "upsert"}},
		Rollback:   []DesiredPublicRouteChange{{Order: 1, Resource: "edge", Action: "restore"}},
	}
}

func TestNormalizePublicRouteRequest(t *testing.T) {
	got, err := NormalizePublicRouteRequest(PublicRouteRequest{
		Hostname: " Arcana.Example.COM. ", UpstreamScheme: "HTTP",
		UpstreamPort: 8080, HealthPath: "/healthz", TLS: "MANAGED",
	})
	if err != nil {
		t.Fatalf("NormalizePublicRouteRequest: %v", err)
	}
	if got.Hostname != "arcana.example.com" || got.UpstreamScheme != "http" || got.TLS != "managed" {
		t.Fatalf("unexpected canonical route request: %#v", got)
	}

	for _, request := range []PublicRouteRequest{
		{Hostname: "*.example.com", UpstreamScheme: "http", UpstreamPort: 8080, HealthPath: "/healthz", TLS: "managed"},
		{Hostname: "arcana.example.com", UpstreamScheme: "https", UpstreamPort: 8080, HealthPath: "/healthz", TLS: "managed"},
		{Hostname: "arcana.example.com", UpstreamScheme: "http", UpstreamPort: 0, HealthPath: "/healthz", TLS: "managed"},
		{Hostname: "arcana.example.com", UpstreamScheme: "http", UpstreamPort: 8080, HealthPath: "/healthz?token=x", TLS: "managed"},
		{Hostname: "arcana.example.com", UpstreamScheme: "http", UpstreamPort: 8080, HealthPath: "/healthz", TLS: "passthrough"},
	} {
		if _, err := NormalizePublicRouteRequest(request); err == nil {
			t.Fatalf("expected invalid public route request: %#v", request)
		}
	}
}

func TestValidateDesiredPublicRouteRejectsCrossResourceMismatch(t *testing.T) {
	plan := validPublicRoutePlan()
	if err := ValidateDesiredPublicRoute(plan); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*DesiredPublicRoutePlan)
		want string
	}{
		{"tunnel hostname", func(p *DesiredPublicRoutePlan) { p.Tunnel.Hostname = "other.example.com" }, "match DNS"},
		{"origin port", func(p *DesiredPublicRoutePlan) { p.Tunnel.OriginURL = "http://edge-01.internal:9090" }, "match the proxy"},
		{"missing ownership", func(p *DesiredPublicRoutePlan) { p.DNS.SourceCoordinate = "" }, "owned proxied"},
		{"operation ordering", func(p *DesiredPublicRoutePlan) { p.Operations[1].Order = 3 }, "sequential"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := validPublicRoutePlan()
			tt.edit(plan)
			err := ValidateDesiredPublicRoute(plan)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPublicRouteIsPartOfDesiredStateHash(t *testing.T) {
	spec := &DesiredServiceSpec{SchemaVersion: DesiredStateSchemaVersion, ServiceID: uuid.New(), EnvironmentID: uuid.New()}
	withoutRoute := spec.ComputeDesiredHash()
	spec.PublicRoute = validPublicRoutePlan()
	withRoute := spec.ComputeDesiredHash()
	spec.PublicRoute.Hostname = "changed.example.com"
	changedRoute := spec.ComputeDesiredHash()

	if withoutRoute == withRoute || withRoute == changedRoute {
		t.Fatalf("public route did not affect desired hash: %s %s %s", withoutRoute, withRoute, changedRoute)
	}
}

func TestRuntimeConvergenceHashesIncludeRouteFreeHashWithoutMutation(t *testing.T) {
	spec := &DesiredServiceSpec{
		SchemaVersion: DesiredStateSchemaVersion,
		ServiceID:     uuid.New(), EnvironmentID: uuid.New(), ArtifactID: uuid.New(),
		Ports: []string{"9090:9090", "8080:8080"}, PublicRoute: validPublicRoutePlan(),
	}
	originalPorts := append([]string(nil), spec.Ports...)
	withoutRoute := *spec
	withoutRoute.Ports = append([]string(nil), spec.Ports...)
	withoutRoute.PublicRoute = nil
	wantRuntimeHash := withoutRoute.ComputeDesiredHash()

	hashes := spec.RuntimeConvergenceHashes()
	if len(hashes) != 2 || hashes[1] != wantRuntimeHash || hashes[0] == hashes[1] {
		t.Fatalf("runtime convergence hashes = %#v, want distinct full and route-free hashes", hashes)
	}
	if spec.DesiredHash != "" || spec.PublicRoute == nil || !slices.Equal(spec.Ports, originalPorts) {
		t.Fatalf("RuntimeConvergenceHashes mutated original spec: %#v", spec)
	}
	if !spec.MatchesRuntimeConvergenceHash(wantRuntimeHash) {
		t.Fatal("route-free runtime hash was not accepted")
	}
}

func TestRuntimeConvergenceHashesDeduplicateSchemaWithoutRouteHashing(t *testing.T) {
	spec := &DesiredServiceSpec{
		SchemaVersion: "3", ServiceID: uuid.New(), EnvironmentID: uuid.New(), ArtifactID: uuid.New(),
		PublicRoute: validPublicRoutePlan(),
	}
	spec.ComputeDesiredHash()
	if hashes := spec.RuntimeConvergenceHashes(); len(hashes) != 1 || hashes[0] != spec.DesiredHash {
		t.Fatalf("schema v3 convergence hashes = %#v, want one deduplicated hash", hashes)
	}
}

func TestInternalHTTPSPlanIsValidatedAndCoveredByDesiredHash(t *testing.T) {
	plan := validPublicRoutePlan()
	spec := &DesiredServiceSpec{SchemaVersion: DesiredStateSchemaVersion, ServiceID: uuid.New(), EnvironmentID: uuid.New(), PublicRoute: plan}
	withoutInternal := spec.ComputeDesiredHash()
	plan.InternalHTTPS = &DesiredInternalHTTPSPlan{
		SchemaVersion: InternalHTTPSPlanSchemaVersion,
		Hostname:      plan.Hostname,
		Listen:        "443 ssl",
		UpstreamURL:   plan.Tunnel.OriginURL,
		CertFile:      "/etc/letsencrypt/live/example/fullchain.pem",
		KeyFile:       "/etc/letsencrypt/live/example/privkey.pem",
		ConfigHash:    "sha256:internal",
		Apply:         []DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "upsert_and_reload", Summary: "install the internal vhost"}},
		Rollback:      []DesiredPublicRouteChange{{Order: 1, Resource: "nginx_vhost", Action: "restore_or_remove_and_reload", Summary: "restore the prior internal vhost"}},
	}
	if err := ValidateDesiredPublicRoute(plan); err != nil {
		t.Fatalf("valid internal HTTPS plan rejected: %v", err)
	}
	withInternal := spec.ComputeDesiredHash()
	plan.InternalHTTPS.UpstreamURL = "http://edge-01.internal:9090"
	changedInternal := spec.ComputeDesiredHash()
	if withoutInternal == withInternal || withInternal == changedInternal {
		t.Fatalf("internal HTTPS stanza did not affect desired hash: %s %s %s", withoutInternal, withInternal, changedInternal)
	}
	if err := ValidateDesiredPublicRoute(plan); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("mismatched internal upstream error = %v", err)
	}
}
