package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func adoptionEnabledConfig() *Config {
	cfg := Defaults()
	cfg.Auth.Enabled = true
	cfg.Nostr.PrivateKey = "test-secret-key"
	cfg.Adoption.Enabled = true
	cfg.Adoption.AllowedPubkeys = []string{strings.Repeat("ab", 32)}
	cfg.Runtime.Endpoints = map[string]RuntimeEndpointConfig{
		"edge-01-docker": {DockerHost: "tcp://edge-01:2376"},
		"shared-docker":  {DockerHost: "tcp://shared:2376"},
	}
	return cfg
}

func TestAdoptionBackgroundScanDefaultsFollowAdoption(t *testing.T) {
	cfg := Defaults()
	if cfg.Adoption.BackgroundScanEnabled() {
		t.Fatal("background scans must be off while adoption is disabled")
	}
	bg := cfg.Adoption.BackgroundScan
	if bg.Interval != 5*time.Minute || bg.Jitter != 30*time.Second || bg.Timeout != time.Minute || bg.Concurrency != 2 || bg.MaxBackoff != time.Hour {
		t.Fatalf("defaults = %+v", bg)
	}

	enabled := adoptionEnabledConfig()
	if err := enabled.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if !enabled.Adoption.BackgroundScanEnabled() {
		t.Fatal("background scans must default on where adoption is enabled")
	}

	off := false
	enabled.Adoption.BackgroundScan.Enabled = &off
	if err := enabled.Validate(); err != nil || enabled.Adoption.BackgroundScanEnabled() {
		t.Fatalf("explicit disable: enabled=%v err=%v", enabled.Adoption.BackgroundScanEnabled(), err)
	}
}

func TestAdoptionBackgroundScanValidation(t *testing.T) {
	on := true
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"explicit enable requires adoption", func(c *Config) {
			c.Adoption.Enabled = false
			c.Adoption.BackgroundScan.Enabled = &on
		}, "requires adoption.enabled=true"},
		{"interval too short", func(c *Config) { c.Adoption.BackgroundScan.Interval = 30 * time.Second }, "interval must be between"},
		{"interval too long", func(c *Config) { c.Adoption.BackgroundScan.Interval = 48 * time.Hour }, "interval must be between"},
		{"jitter above half interval", func(c *Config) { c.Adoption.BackgroundScan.Jitter = 3 * time.Minute }, "jitter must be between"},
		{"negative jitter", func(c *Config) { c.Adoption.BackgroundScan.Jitter = -time.Second }, "jitter must be between"},
		{"timeout not below interval", func(c *Config) { c.Adoption.BackgroundScan.Timeout = 5 * time.Minute }, "timeout must be at least"},
		{"timeout too short", func(c *Config) { c.Adoption.BackgroundScan.Timeout = time.Second }, "timeout must be at least"},
		{"concurrency too high", func(c *Config) { c.Adoption.BackgroundScan.Concurrency = 9 }, "concurrency must be between"},
		{"max backoff below interval", func(c *Config) { c.Adoption.BackgroundScan.MaxBackoff = time.Minute }, "max_backoff must be between"},
		{"target without endpoint", func(c *Config) {
			c.Adoption.BackgroundScan.Targets = []AdoptionBackgroundScanTarget{{Name: "edge-01"}}
		}, "requires name and endpoint_ref"},
		{"target with unknown endpoint", func(c *Config) {
			c.Adoption.BackgroundScan.Targets = []AdoptionBackgroundScanTarget{{Name: "edge-01", EndpointRef: "missing"}}
		}, "is not configured in runtime.endpoints"},
		{"duplicate target", func(c *Config) {
			c.Adoption.BackgroundScan.Targets = []AdoptionBackgroundScanTarget{{Name: "edge", EndpointRef: "edge-01-docker"}, {Name: "EDGE", EndpointRef: "shared-docker"}}
		}, "duplicate name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := adoptionEnabledConfig()
			tc.mutate(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("disabled scans skip bound checks", func(t *testing.T) {
		cfg := adoptionEnabledConfig()
		off := false
		cfg.Adoption.BackgroundScan.Enabled = &off
		cfg.Adoption.BackgroundScan.Interval = time.Second
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() = %v", err)
		}
	})
}

func TestAdoptionBackgroundScanTargetsDeriveFromEndpoints(t *testing.T) {
	cfg := adoptionEnabledConfig()
	cfg.Runtime.Environments = map[string]RuntimeTargetConfig{
		"production": {EndpointRef: "edge-01-docker"},
		"staging":    {EndpointRef: "shared-docker"},
		"qa":         {EndpointRef: "shared-docker"},
	}
	got := cfg.BackgroundScanTargets()
	want := []AdoptionBackgroundScanTarget{
		{Name: "edge-01-docker", EndpointRef: "edge-01-docker", Environment: "production"},
		// Shared by two environments: the environment defaults to the alias.
		{Name: "shared-docker", EndpointRef: "shared-docker"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("derived targets = %+v, want %+v", got, want)
	}

	cfg.Adoption.BackgroundScan.Targets = []AdoptionBackgroundScanTarget{{Name: "only", EndpointRef: "edge-01-docker", Environment: "production"}}
	if got := cfg.BackgroundScanTargets(); len(got) != 1 || got[0].Name != "only" {
		t.Fatalf("explicit scope = %+v", got)
	}
}

func TestLoadAdoptionBackgroundScanFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`auth:
  enabled: true
nostr:
  private_key: test-secret-key
runtime:
  endpoints:
    edge-01-docker:
      docker_host: tcp://edge-01:2376
adoption:
  enabled: true
  allowed_pubkeys:
    - abcdef01abcdef01abcdef01abcdef01abcdef01abcdef01abcdef01abcdef01
  background_scan:
    enabled: false
    interval: 10m
    jitter: 1m
    timeout: 2m
    concurrency: 3
    max_backoff: 2h
    targets:
      - name: edge-01
        endpoint_ref: edge-01-docker
        environment: production
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	bg := cfg.Adoption.BackgroundScan
	if cfg.Adoption.BackgroundScanEnabled() || bg.Enabled == nil || *bg.Enabled {
		t.Fatalf("explicit disable not loaded: %+v", bg)
	}
	if bg.Interval != 10*time.Minute || bg.Jitter != time.Minute || bg.Timeout != 2*time.Minute || bg.Concurrency != 3 || bg.MaxBackoff != 2*time.Hour {
		t.Fatalf("bounds not loaded: %+v", bg)
	}
	if len(bg.Targets) != 1 || bg.Targets[0] != (AdoptionBackgroundScanTarget{Name: "edge-01", EndpointRef: "edge-01-docker", Environment: "production"}) {
		t.Fatalf("targets not loaded: %+v", bg.Targets)
	}
}
