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
