package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func enabledPolicy() RouteCanaryPolicy {
	return RouteCanaryPolicy{
		Enabled:             true,
		ProbeTimeout:        10 * time.Second,
		ExpectedStatusMin:   200,
		ExpectedStatusMax:   299,
		TLSMinDaysRemaining: 14,
		PublicResolverAddr:  "1.1.1.1:53",
		InternalDialAddresses: map[string]string{
			"sharegap.net": "192.168.40.10",
		},
		Thresholds: RouteCanaryThresholds{FailureThreshold: 3, SuccessThreshold: 2},
	}
}

func planWithInternal(internal bool) *DesiredPublicRoutePlan {
	plan := &DesiredPublicRoutePlan{
		SchemaVersion:    "1",
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		DeploymentUnitID: uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		Hostname:         "git.sharegap.net",
		Zone:             "sharegap.net",
		Proxy:            DesiredPublicRouteProxy{HealthPath: "/healthz"},
	}
	if internal {
		plan.InternalHTTPS = &DesiredInternalHTTPSPlan{
			SchemaVersion: "1",
			Hostname:      "git.sharegap.net",
			Listen:        "443 ssl",
			UpstreamURL:   "http://nginx-git:3000",
		}
	}
	return plan
}

func TestDeriveRouteCanaryTargetsPublicOnly(t *testing.T) {
	targets, err := DeriveRouteCanaryTargets(planWithInternal(false), enabledPolicy())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	target := targets[0]
	if target.Perspective != RouteCanaryPerspectivePublicEdge {
		t.Fatalf("got perspective %q", target.Perspective)
	}
	if target.ResolverAddr != "1.1.1.1:53" {
		t.Fatalf("public target must use the configured public resolver, got %q", target.ResolverAddr)
	}
	if target.ResolveTo != "" {
		t.Fatal("public target must not pin a dial address")
	}
	if target.URL() != "https://git.sharegap.net/healthz" {
		t.Fatalf("got URL %q", target.URL())
	}
}

// TestDeriveRouteCanaryTargetsAddsInternalLANTarget proves that a route with an
// internal vhost gets a second, split-DNS perspective, which is the check that
// would have caught the stale nginx upstream.
func TestDeriveRouteCanaryTargetsAddsInternalLANTarget(t *testing.T) {
	targets, err := DeriveRouteCanaryTargets(planWithInternal(true), enabledPolicy())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	internal := targets[1]
	if internal.Perspective != RouteCanaryPerspectiveInternalLAN {
		t.Fatalf("got perspective %q", internal.Perspective)
	}
	if internal.ResolveTo != "192.168.40.10" {
		t.Fatalf("internal target must pin the configured LAN address, got %q", internal.ResolveTo)
	}
	if internal.ResolverAddr != "" {
		t.Fatal("internal target must not also set a resolver; the modes are exclusive")
	}
	if internal.Hostname != "git.sharegap.net" {
		t.Fatalf("internal target must keep the canonical hostname for TLS and Host, got %q", internal.Hostname)
	}
	if internal.Port != 443 {
		t.Fatalf("internal port should come from the listen directive, got %d", internal.Port)
	}
}

// TestDeriveRouteCanaryTargetsSkipsInternalWhenZoneUnmapped proves derivation
// never invents a LAN address. An unmapped zone yields the public target only,
// rather than a target pointed at a guessed host.
func TestDeriveRouteCanaryTargetsSkipsInternalWhenZoneUnmapped(t *testing.T) {
	policy := enabledPolicy()
	policy.InternalDialAddresses = map[string]string{"other.example": "10.0.0.1"}
	targets, err := DeriveRouteCanaryTargets(planWithInternal(true), policy)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected only the public target, got %d", len(targets))
	}
}

func TestDeriveRouteCanaryTargetsParsesNonDefaultInternalPort(t *testing.T) {
	plan := planWithInternal(true)
	plan.InternalHTTPS.Listen = "8443 ssl http2"
	targets, err := DeriveRouteCanaryTargets(plan, enabledPolicy())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if targets[1].Port != 8443 {
		t.Fatalf("got port %d, want 8443", targets[1].Port)
	}
	if targets[1].URL() != "https://git.sharegap.net:8443/healthz" {
		t.Fatalf("got URL %q", targets[1].URL())
	}
}

func TestDeriveRouteCanaryTargetsDisabledPolicyYieldsNoTargets(t *testing.T) {
	policy := enabledPolicy()
	policy.Enabled = false
	targets, err := DeriveRouteCanaryTargets(planWithInternal(true), policy)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected no targets when disabled, got %d", len(targets))
	}
}

func TestDeriveRouteCanaryTargetsDefaultsEmptyHealthPath(t *testing.T) {
	plan := planWithInternal(false)
	plan.Proxy.HealthPath = ""
	targets, err := DeriveRouteCanaryTargets(plan, enabledPolicy())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if targets[0].Path != "/" {
		t.Fatalf("got path %q, want /", targets[0].Path)
	}
}

func TestRouteCanaryPolicyValidateRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RouteCanaryPolicy)
	}{
		{"non positive timeout", func(p *RouteCanaryPolicy) { p.ProbeTimeout = 0 }},
		{"inverted status range", func(p *RouteCanaryPolicy) { p.ExpectedStatusMin, p.ExpectedStatusMax = 500, 200 }},
		{"negative tls window", func(p *RouteCanaryPolicy) { p.TLSMinDaysRemaining = -1 }},
		{"resolver without port", func(p *RouteCanaryPolicy) { p.PublicResolverAddr = "1.1.1.1" }},
		{"non ip dial address", func(p *RouteCanaryPolicy) { p.InternalDialAddresses = map[string]string{"z": "nginx-git"} }},
		{"empty zone key", func(p *RouteCanaryPolicy) { p.InternalDialAddresses = map[string]string{"": "10.0.0.1"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := enabledPolicy()
			test.mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestRouteCanaryPolicyAllowsSystemResolver(t *testing.T) {
	policy := enabledPolicy()
	policy.PublicResolverAddr = "system"
	if err := policy.Validate(); err != nil {
		t.Fatalf("system resolver should be valid: %v", err)
	}
}

func TestRouteCanaryKeyForPlanMatchesInstanceCoordinate(t *testing.T) {
	plan := planWithInternal(false)
	key := RouteCanaryKeyForPlan(plan)
	if key.ServiceID != plan.ServiceID || key.EnvironmentID != plan.EnvironmentID {
		t.Fatal("key must carry the plan's service and environment identity")
	}
	if key.DeploymentUnitID == nil || *key.DeploymentUnitID != plan.DeploymentUnitID {
		t.Fatal("key must carry the deployment unit so route state can be contrasted with instance health")
	}
	if key.Hostname != "git.sharegap.net" {
		t.Fatalf("got hostname %q", key.Hostname)
	}
}
