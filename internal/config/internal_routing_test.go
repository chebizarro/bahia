package config

import (
	"reflect"
	"strings"
	"testing"
)

func validInternalRoutingConfig() Config {
	cfg := validEdgeRoutingConfig()
	cfg.InternalRouting = InternalRoutingConfig{
		Enabled:    true,
		Provider:   " NGINX ",
		IncludeDir: "/etc/nginx/conf.d",
		CertFile:   "/etc/letsencrypt/live/sharegap.net/fullchain.pem",
		KeyFile:    "/etc/letsencrypt/live/sharegap.net/privkey.pem",
		Zones:      []string{" ShareGap.NET. "},
	}
	return cfg
}

func TestValidateInternalRoutingAppliesSafeDefaultsWithoutReadingHostFiles(t *testing.T) {
	cfg := validInternalRoutingConfig()
	if err := cfg.validateInternalRouting(); err != nil {
		t.Fatalf("validateInternalRouting: %v", err)
	}
	got := cfg.InternalRouting
	if got.Provider != "nginx" || got.FilePrefix != "bahia-" || got.Zones[0] != "sharegap.net" {
		t.Fatalf("configuration was not canonicalized: %#v", got)
	}
	if !reflect.DeepEqual(got.TestCommand, []string{"nginx", "-t"}) || !reflect.DeepEqual(got.ReloadCommand, []string{"nginx", "-s", "reload"}) {
		t.Fatalf("command defaults = test %#v reload %#v", got.TestCommand, got.ReloadCommand)
	}
}

func TestValidateInternalRoutingFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"edge routing disabled", func(c *Config) { c.EdgeRouting.Enabled = false }, "edge_routing.enabled"},
		{"wrong provider", func(c *Config) { c.InternalRouting.Provider = "caddy" }, "must be nginx"},
		{"relative include dir", func(c *Config) { c.InternalRouting.IncludeDir = "nginx/conf.d" }, "include_dir"},
		{"relative certificate", func(c *Config) { c.InternalRouting.CertFile = "fullchain.pem" }, "absolute paths"},
		{"path prefix", func(c *Config) { c.InternalRouting.FilePrefix = "../bahia-" }, "filename prefix"},
		{"empty test argv", func(c *Config) { c.InternalRouting.TestCommand = []string{"nginx", ""} }, "test_command"},
		{"missing zones", func(c *Config) { c.InternalRouting.Zones = nil }, "at least one zone"},
		{"duplicate zones", func(c *Config) { c.InternalRouting.Zones = []string{"sharegap.net", "SHAREGAP.NET."} }, "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validInternalRoutingConfig()
			tt.edit(&cfg)
			err := cfg.validateInternalRouting()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
