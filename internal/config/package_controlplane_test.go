package config

import (
	"strings"
	"testing"
	"time"
)

func TestPackageConfigDefaultsDisabled(t *testing.T) {
	cfg := Defaults()
	if cfg.Packages.Enabled {
		t.Fatal("packages should be disabled by default")
	}
	if cfg.Packages.Backends == nil {
		t.Fatal("packages backends map should be initialized")
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestPackageConfigEnabledRequiresBackend(t *testing.T) {
	cfg := Defaults()
	cfg.Packages.Enabled = true
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "packages.backends") {
		t.Fatalf("expected packages.backends validation error, got %v", err)
	}
}

func TestPackageConfigValidatesRequiredBackendsAndSecretRefs(t *testing.T) {
	cfg := Defaults()
	cfg.Packages.Enabled = true
	cfg.Packages.AllowedSourceHosts = []string{" Packages.Example.COM ", "packages.example.com", "blob.example.com"}
	cfg.Packages.Backends = map[string]PackageBackendConfig{
		"nexus-prod": {
			Type:          "nexus",
			BaseURL:       "https://nexus.example.com",
			AuthSecretRef: "secrets/package/nexus-prod",
			SecretRefs: map[string]string{
				"client_ca": "secrets/package/nexus-prod-ca",
			},
		},
		"pulp-stage": {
			Type:          "pulp",
			BaseURL:       "https://pulp.example.com",
			AuthSecretRef: "secrets/package/pulp-stage",
			Timeout:       45 * time.Second,
		},
		"mock": {
			Type:          "filesystem_mock",
			RootDir:       "/tmp/bahia-packages",
			PublicBaseURL: "https://packages.local.test",
		},
	}

	if err := cfg.validate(); err != nil {
		t.Fatalf("valid package config should pass: %v", err)
	}
	if got := cfg.Packages.Backends["nexus-prod"].Timeout; got != 30*time.Second {
		t.Fatalf("expected default timeout 30s, got %s", got)
	}
	if got := cfg.Packages.Backends["pulp-stage"].Timeout; got != 45*time.Second {
		t.Fatalf("expected configured timeout 45s, got %s", got)
	}
	if got := cfg.Packages.AllowedSourceHosts; len(got) != 2 || got[0] != "packages.example.com" || got[1] != "blob.example.com" {
		t.Fatalf("unexpected normalized source hosts: %#v", got)
	}
	if backend, ok := cfg.PackageBackend(" nexus-prod "); !ok || backend.Type != "nexus" {
		t.Fatalf("PackageBackend lookup failed: %#v ok=%v", backend, ok)
	}
}

func TestPackageConfigRejectsInvalidBackends(t *testing.T) {
	tests := []struct {
		name     string
		backend  PackageBackendConfig
		contains string
	}{
		{"unsupported", PackageBackendConfig{Type: "s3"}, "unsupported"},
		{"nexus_base_url", PackageBackendConfig{Type: "nexus"}, "base_url"},
		{"pulp_base_url", PackageBackendConfig{Type: "pulp", BaseURL: "ftp://pulp.example.com"}, "http or https"},
		{"filesystem_root", PackageBackendConfig{Type: "filesystem_mock"}, "root_dir"},
		{"bad_public_url", PackageBackendConfig{Type: "filesystem_mock", RootDir: "/tmp/pkgs", PublicBaseURL: "not a url"}, "public_base_url"},
		{"bad_secret_ref", PackageBackendConfig{Type: "filesystem_mock", RootDir: "/tmp/pkgs", SecretRefs: map[string]string{"": "secret"}}, "secret_refs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Packages.Enabled = true
			cfg.Packages.Backends = map[string]PackageBackendConfig{"bad": tt.backend}
			err := cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("expected error containing %q, got %v", tt.contains, err)
			}
		})
	}
}

func TestPackageConfigRejectsSourceHostsAsURLs(t *testing.T) {
	cfg := Defaults()
	cfg.Packages.AllowedSourceHosts = []string{"https://packages.example.com"}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "hostnames, not URLs") {
		t.Fatalf("expected source host URL validation error, got %v", err)
	}
}
