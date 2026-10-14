package config

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validEdgeRoutingConfig() Config {
	return Config{
		DirectRuntime: DirectRuntimeConfig{Enabled: true},
		EdgeRouting: EdgeRoutingConfig{
			Enabled: true, Provider: "cloudflare_tunnel", BackendRef: "public-edge",
			APIBaseURL: "https://api.cloudflare.com/client/v4", APITokenRef: uuid.NewString(),
			AccountID: "account", TunnelID: uuid.NewString(), VerifyTimeout: time.Second,
			Zones: []EdgeRoutingZoneConfig{{
				Name: "Example.COM.", ZoneID: "zone", AllowedOrgIDs: []string{uuid.NewString()},
			}},
			Origins: []EdgeRoutingOriginConfig{{
				DeploymentUnitID: uuid.NewString(), Host: "Edge-01.Internal.", AllowedPorts: []int{8080},
			}},
		},
	}
}

func TestValidateEdgeRoutingCanonicalizesSafeConfiguration(t *testing.T) {
	cfg := validEdgeRoutingConfig()
	if err := cfg.validateEdgeRouting(); err != nil {
		t.Fatalf("validateEdgeRouting: %v", err)
	}
	if cfg.EdgeRouting.Zones[0].Name != "example.com" || cfg.EdgeRouting.Zones[0].TTL != 1 || cfg.EdgeRouting.Origins[0].Host != "edge-01.internal" {
		t.Fatalf("configuration was not canonicalized: %#v", cfg.EdgeRouting)
	}
}

func TestValidateEdgeRoutingVerifyResolver(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "public resolver", value: "1.1.1.1:53", want: "1.1.1.1:53"},
		{name: "system resolver", value: "system", want: "system"},
		{name: "empty uses public default", value: "", want: "1.1.1.1:53"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validEdgeRoutingConfig()
			cfg.EdgeRouting.VerifyResolver = tt.value
			if err := cfg.validateEdgeRouting(); err != nil {
				t.Fatalf("validateEdgeRouting: %v", err)
			}
			if cfg.EdgeRouting.VerifyResolver != tt.want {
				t.Fatalf("VerifyResolver = %q, want %q", cfg.EdgeRouting.VerifyResolver, tt.want)
			}
		})
	}
}

func TestValidateEdgeRoutingRejectsInvalidVerifyResolver(t *testing.T) {
	for _, resolver := range []string{"nonsense", "1.1.1.1"} {
		t.Run(resolver, func(t *testing.T) {
			cfg := validEdgeRoutingConfig()
			cfg.EdgeRouting.VerifyResolver = resolver
			err := cfg.validateEdgeRouting()
			if err == nil || !strings.Contains(err.Error(), "verify_resolver") {
				t.Fatalf("error = %v, want verify_resolver validation error", err)
			}
		})
	}
}

func TestValidateEdgeRoutingFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"direct runtime disabled", func(c *Config) { c.DirectRuntime.Enabled = false }, "direct_runtime_actions.enabled"},
		{"token is not opaque UUID", func(c *Config) { c.EdgeRouting.APITokenRef = "plaintext-token" }, "secret UUID"},
		{"insecure provider API", func(c *Config) { c.EdgeRouting.APIBaseURL = "http://api.cloudflare.test" }, "must use HTTPS"},
		{"origin has path", func(c *Config) { c.EdgeRouting.Origins[0].Host = "edge.internal/path" }, "fully qualified DNS"},
		{"invalid port", func(c *Config) { c.EdgeRouting.Origins[0].AllowedPorts = []int{0} }, "invalid port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validEdgeRoutingConfig()
			tt.edit(&cfg)
			err := cfg.validateEdgeRouting()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidateEdgeRoutingCaddyAndPerZoneSelection(t *testing.T) {
	cfg := validEdgeRoutingConfig()
	cfg.EdgeRouting.Provider = "caddy"
	cfg.EdgeRouting.BackendRef = "cloudflare-edge"
	cfg.EdgeRouting.CaddyBackendRef = "caddy-edge"
	cfg.EdgeRouting.CaddyAdminURL = "http://127.0.0.1:2019"
	cfg.EdgeRouting.CaddyServerID = "public"
	cfg.EdgeRouting.Zones[0].ZoneID = ""
	cfg.EdgeRouting.Zones = append(cfg.EdgeRouting.Zones, EdgeRoutingZoneConfig{
		Name: "other.example.com", Provider: "cloudflare_tunnel", ZoneID: "other-zone",
		AllowedOrgIDs: []string{uuid.NewString()},
	})
	if err := cfg.validateEdgeRouting(); err != nil {
		t.Fatal(err)
	}
	if cfg.EdgeRouting.Zones[0].Provider != "caddy" || cfg.EdgeRouting.Zones[0].BackendRef != "caddy-edge" ||
		cfg.EdgeRouting.Zones[1].Provider != "cloudflare_tunnel" || cfg.EdgeRouting.Zones[1].BackendRef != "cloudflare-edge" {
		t.Fatalf("zone providers not resolved: %#v", cfg.EdgeRouting.Zones)
	}
}

func TestValidateEdgeRoutingCaddyFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"unknown provider", func(c *Config) { c.EdgeRouting.Zones[0].Provider = "unknown" }},
		{"missing admin URL", func(c *Config) { c.EdgeRouting.CaddyAdminURL = "" }},
		{"remote HTTP", func(c *Config) { c.EdgeRouting.CaddyAdminURL = "http://caddy.example.com:2019" }},
		{"remote HTTP in dev mode", func(c *Config) { c.DevMode = true; c.EdgeRouting.CaddyAdminURL = "http://caddy.example.com:2019" }},
		{"inline credential", func(c *Config) { c.EdgeRouting.CaddyCredentialRef = "plaintext" }},
		{"wrong backend ref", func(c *Config) { c.EdgeRouting.Zones[0].BackendRef = "foreign" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validEdgeRoutingConfig()
			cfg.EdgeRouting.Provider = "caddy"
			cfg.EdgeRouting.CaddyBackendRef = "caddy-edge"
			cfg.EdgeRouting.CaddyAdminURL = "http://127.0.0.1:2019"
			cfg.EdgeRouting.CaddyServerID = "public"
			cfg.EdgeRouting.Zones[0].ZoneID = ""
			tt.edit(&cfg)
			if err := cfg.validateEdgeRouting(); err == nil {
				t.Fatal("invalid Caddy configuration accepted")
			}
		})
	}
}
