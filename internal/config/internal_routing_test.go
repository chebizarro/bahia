package config

import (
	"fmt"
	"os"
	"path/filepath"
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
		{"command env missing equals", func(c *Config) { c.InternalRouting.CommandEnv = []string{"DOCKER_HOST"} }, "command_env[0]"},
		{"command env empty key", func(c *Config) { c.InternalRouting.CommandEnv = []string{"=tcp://docker:2375"} }, "non-empty valid key"},
		{"command env invalid key", func(c *Config) { c.InternalRouting.CommandEnv = []string{"DOCKER-HOST=tcp://docker:2375"} }, "non-empty valid key"},
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

func TestValidateInternalRoutingPreservesCommandEnvironmentValues(t *testing.T) {
	cfg := validInternalRoutingConfig()
	cfg.InternalRouting.CommandEnv = []string{"DOCKER_HOST=tcp://docker:2375", "TOKEN= value with spaces "}
	if err := cfg.validateInternalRouting(); err != nil {
		t.Fatalf("validateInternalRouting: %v", err)
	}
	want := []string{"DOCKER_HOST=tcp://docker:2375", "TOKEN= value with spaces "}
	if !reflect.DeepEqual(cfg.InternalRouting.CommandEnv, want) {
		t.Fatalf("command_env = %#v, want %#v", cfg.InternalRouting.CommandEnv, want)
	}
}

func TestLoadCommandEnvironmentValidationFollowsEnabledState(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		wantErr bool
	}{
		{name: "disabled", enabled: false, wantErr: false},
		{name: "enabled", enabled: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := fmt.Sprintf("internal_routing:\n  enabled: %t\n  command_env: [DOCKER-HOST=value]\n", tt.enabled)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "internal_routing.command_env[0]") {
					t.Fatalf("Load error = %v, want command_env rejection", err)
				}
			} else if err != nil {
				t.Fatalf("Load error = %v, want disabled malformed command_env to remain inert", err)
			}
		})
	}
}

func TestLoadRejectsUnknownInternalRoutingKeyWithHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("internal_routing:\n  check_command: [nginx, -t]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load error = nil, want unknown internal_routing key rejection")
	}
	want := `unknown internal_routing key "check_command" (did you mean "test_command"?)`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Load error = %q, want %q", err, want)
	}
}
