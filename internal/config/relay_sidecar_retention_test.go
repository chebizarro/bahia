package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelaySidecarRetentionDefaults(t *testing.T) {
	sidecar := Defaults().Nostr.Sidecar
	if sidecar.EventRetention != 0 {
		t.Errorf("EventRetention = %s, want 0 (regular events durable)", sidecar.EventRetention)
	}
	if sidecar.RequestRetention != 24*time.Hour {
		t.Errorf("RequestRetention = %s, want 24h", sidecar.RequestRetention)
	}
	if got := sidecar.RequestRetentionKinds; len(got) != 3 || got[0] != 25910 || got[1] != 1059 || got[2] != 21059 {
		t.Errorf("RequestRetentionKinds = %v, want [25910 1059 21059]", got)
	}
	if sidecar.NegentropyMaxEvents != DefaultRelaySidecarNegentropyMaxEvents {
		t.Errorf("NegentropyMaxEvents = %d", sidecar.NegentropyMaxEvents)
	}
	cfg := Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaults with the sidecar enabled must validate: %v", err)
	}
}

func TestRelaySidecarRetentionValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*RelaySidecarConfig)
		want   string
	}{
		"negative event retention":  {func(s *RelaySidecarConfig) { s.EventRetention = -time.Hour }, "nostr.sidecar.event_retention"},
		"unit-less event retention": {func(s *RelaySidecarConfig) { s.EventRetention = 168 }, "nostr.sidecar.event_retention"},
		"zero request retention":    {func(s *RelaySidecarConfig) { s.RequestRetention = 0 }, "nostr.sidecar.request_retention"},
		"sub-minute request":        {func(s *RelaySidecarConfig) { s.RequestRetention = 30 * time.Second }, "nostr.sidecar.request_retention"},
		"addressable request kind":  {func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{30900} }, "replaceable or addressable"},
		"replaceable request kind":  {func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{0} }, "replaceable or addressable"},
		"deletion request kind":     {func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{5} }, "kind 5"},
		"out of range kind":         {func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{70000} }, "outside 0-65535"},
		"duplicate kind":            {func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{1059, 1059} }, "duplicate kind 1059"},
		"zero negentropy bound":     {func(s *RelaySidecarConfig) { s.NegentropyMaxEvents = 0 }, "nostr.sidecar.negentropy_max_events"},
		"huge negentropy bound":     {func(s *RelaySidecarConfig) { s.NegentropyMaxEvents = MaxRelaySidecarNegentropyMaxEvents + 1 }, "nostr.sidecar.negentropy_max_events"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Nostr.Sidecar.Enabled = true
			tc.mutate(&cfg.Nostr.Sidecar)
			if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() error = %v, want %q", err, tc.want)
			}
		})
	}
	for name, mutate := range map[string]func(*RelaySidecarConfig){
		"event retention cap":  func(s *RelaySidecarConfig) { s.EventRetention = 90 * 24 * time.Hour },
		"no request kinds":     func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{} },
		"regular request kind": func(s *RelaySidecarConfig) { s.RequestRetentionKinds = []int{1059, 5961} },
	} {
		t.Run("accepts "+name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Nostr.Sidecar.Enabled = true
			mutate(&cfg.Nostr.Sidecar)
			if err := cfg.validate(); err != nil {
				t.Fatalf("validate() error = %v", err)
			}
		})
	}
}

func TestLoadRelaySidecarRetentionKeysFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`nostr:
  sidecar:
    enabled: true
    listen_addr: "127.0.0.1:3334"
    public_url: "ws://127.0.0.1:3334"
    backend_url: "ws://127.0.0.1:3334"
    data_dir: "/tmp/bahia-relay"
    event_retention: 2160h
    request_retention: 6h
    request_retention_kinds: [1059, 5961]
    negentropy_max_events: 50000
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	sidecar := cfg.Nostr.Sidecar
	if sidecar.EventRetention != 90*24*time.Hour || sidecar.RequestRetention != 6*time.Hour {
		t.Errorf("retention = %s / %s", sidecar.EventRetention, sidecar.RequestRetention)
	}
	if got := sidecar.RequestRetentionKinds; len(got) != 2 || got[0] != 1059 || got[1] != 5961 {
		t.Errorf("RequestRetentionKinds = %v", got)
	}
	if sidecar.NegentropyMaxEvents != 50000 {
		t.Errorf("NegentropyMaxEvents = %d", sidecar.NegentropyMaxEvents)
	}
}
