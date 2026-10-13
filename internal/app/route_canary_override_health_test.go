package app

import "testing"

func TestRouteCanaryOverrideHealth(t *testing.T) {
	if check := routeCanaryOverrideHealth(nil, false); check.Status != HealthStatusWarn {
		t.Fatalf("pending enumeration: %+v", check)
	}
	if check := routeCanaryOverrideHealth([]string{"typo.example.com"}, true); check.Status != HealthStatusWarn || check.Details["hostnames"] != "typo.example.com" {
		t.Fatalf("unmatched override: %+v", check)
	}
	if check := routeCanaryOverrideHealth(nil, true); check.Status != HealthStatusPass {
		t.Fatalf("matched override: %+v", check)
	}
}
