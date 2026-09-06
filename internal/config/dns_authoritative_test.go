package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDNSZoneAuthoritativeFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`dev_mode: true
dns:
  enabled: false
  zones:
    - name: sharegap.net
      visibility: internal
      backend: core-01
      ttl: 300
      authoritative: true
      allow_empty_authoritative: true
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(cfg.DNS.Zones) != 1 || !cfg.DNS.Zones[0].Authoritative || !cfg.DNS.Zones[0].AllowEmptyAuthoritative {
		t.Fatalf("DNS zones = %#v, want one authoritative zone with empty-sync opt-out", cfg.DNS.Zones)
	}
}
