package app

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func backgroundScanAppConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Adoption.Enabled = true
	cfg.Adoption.AllowedPubkeys = []string{strings.Repeat("ab", 32)}
	cfg.Runtime.Endpoints = map[string]config.RuntimeEndpointConfig{"Edge_01": {DockerHost: "tcp://edge-01:2376"}}
	cfg.Runtime.Environments = map[string]config.RuntimeTargetConfig{"Production": {EndpointRef: "Edge_01"}}
	return cfg
}

func backgroundScanAdoptionService(cfg *config.Config) *service.AdoptionService {
	return service.NewAdoptionService(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop(), service.WithAdoptionRuntimeConfig(cfg.Runtime, false))
}

func TestAdoptionBackgroundScanRunnerIsNotBuiltWhenDisabled(t *testing.T) {
	off := false
	cases := map[string]func(*config.Config){
		"adoption disabled":   func(c *config.Config) { c.Adoption.Enabled = false },
		"explicitly disabled": func(c *config.Config) { c.Adoption.BackgroundScan.Enabled = &off },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := backgroundScanAppConfig()
			mutate(cfg)
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			runner, err := newAdoptionBackgroundScanRunner(cfg, backgroundScanAdoptionService(cfg), zap.NewNop())
			if err != nil || runner != nil {
				t.Fatalf("runner=%v err=%v, want nothing built", runner, err)
			}
		})
	}
	t.Run("no adoption service", func(t *testing.T) {
		cfg := backgroundScanAppConfig()
		if runner, err := newAdoptionBackgroundScanRunner(cfg, nil, zap.NewNop()); err != nil || runner != nil {
			t.Fatalf("runner=%v err=%v", runner, err)
		}
	})
}

func TestAdoptionBackgroundScanRunnerUsesNormalizedEndpointTargets(t *testing.T) {
	cfg := backgroundScanAppConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	runner, err := newAdoptionBackgroundScanRunner(cfg, backgroundScanAdoptionService(cfg), zap.NewNop())
	if err != nil || runner == nil {
		t.Fatalf("runner=%v err=%v", runner, err)
	}
	targets := runner.Status().Targets
	// Names and environments are normalized exactly as the adoption scan
	// normalizes them, so published coordinates and the retirement scope agree.
	if len(targets) != 1 || targets[0].Target != "edge-01" || targets[0].Environment != "production" || targets[0].Outcome != service.BackgroundScanOutcomePending {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestAdoptionBackgroundScanHealthWarnsOnFailingTargets(t *testing.T) {
	healthy := adoptionBackgroundScanHealth(service.AdoptionBackgroundScanStatus{
		Targets: []service.BackgroundScanTargetStatus{{Target: "edge-01", Outcome: service.BackgroundScanOutcomeOK}},
		Cycles:  3,
	}, Tier3)
	if healthy.Status != HealthStatusPass || healthy.Details["target.edge-01"] != "ok" || healthy.Details["cycles"] != "3" {
		t.Fatalf("healthy check = %+v", healthy)
	}
	failing := adoptionBackgroundScanHealth(service.AdoptionBackgroundScanStatus{
		Targets: []service.BackgroundScanTargetStatus{
			{Target: "edge-01", Outcome: service.BackgroundScanOutcomeOK},
			{Target: "edge-02", Outcome: service.BackgroundScanOutcomeTimeout, ConsecutiveFailures: 2},
		},
		Failing: 1,
	}, Tier3)
	if failing.Status != HealthStatusWarn || failing.Details["target.edge-02"] != "timeout" || failing.Details["failing"] != "1" {
		t.Fatalf("failing check = %+v", failing)
	}
	// Warn degrades readiness status but never fails readiness.
	if !checksPass([]HealthCheck{failing}) {
		t.Fatal("failing background scan target must not fail readiness")
	}
}

func TestAdoptionBackgroundScanRunnerSkipsCollidingDerivedAliasesButRejectsExplicitOnes(t *testing.T) {
	cfg := backgroundScanAppConfig()
	cfg.Runtime.Endpoints["edge-01"] = config.RuntimeEndpointConfig{DockerHost: "tcp://edge-01-b:2376"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	runner, err := newAdoptionBackgroundScanRunner(cfg, backgroundScanAdoptionService(cfg), zap.NewNop())
	if err != nil || runner == nil {
		t.Fatalf("runner=%v err=%v, want a successful start", runner, err)
	}
	status := runner.Status()
	if len(status.Targets) != 1 || len(status.SkippedTargets) != 1 {
		t.Fatalf("status = %+v, want one target and one skipped alias", status)
	}
	if check := adoptionBackgroundScanHealth(status, Tier3); check.Status != HealthStatusWarn || check.Details["skipped"] == "" {
		t.Fatalf("skipped alias must surface as a warning: %+v", check)
	}

	cfg.Adoption.BackgroundScan.Targets = []config.AdoptionBackgroundScanTarget{{Name: "edge_01", EndpointRef: "Edge_01"}, {Name: "edge-01", EndpointRef: "edge-01"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := newAdoptionBackgroundScanRunner(cfg, backgroundScanAdoptionService(cfg), zap.NewNop()); err == nil || !strings.Contains(err.Error(), "normalize to the same target") {
		t.Fatalf("explicit colliding targets err=%v, want a configuration error", err)
	}
}
