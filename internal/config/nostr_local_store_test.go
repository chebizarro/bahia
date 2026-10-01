package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNostrLocalStoreDefaultsValidate(t *testing.T) {
	store := Defaults().Nostr.LocalStore
	if store.Path != DefaultNostrLocalStorePath || store.ResumeOverlap != 10*time.Minute || store.RegularLookback != 24*time.Hour || store.NegentropyUpload {
		t.Fatalf("unexpected defaults: %+v", store)
	}
	cfg := Defaults()
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	cfg.Nostr.LocalStore.RegularLookback = 0
	if err := cfg.validate(); err != nil {
		t.Fatalf("a zero lookback (whole history) must validate: %v", err)
	}
}

func TestNostrLocalStoreValidation(t *testing.T) {
	dir := t.TempDir()
	sidecarDir := filepath.Join(dir, "sidecar")
	for name, tc := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"empty path":         {func(c *Config) { c.Nostr.LocalStore.Path = "  " }, "nostr.local_store.path is required"},
		"directory path":     {func(c *Config) { c.Nostr.LocalStore.Path = dir }, "is a directory"},
		"zero overlap":       {func(c *Config) { c.Nostr.LocalStore.ResumeOverlap = 0 }, "nostr.local_store.resume_overlap"},
		"unit-less overlap":  {func(c *Config) { c.Nostr.LocalStore.ResumeOverlap = 600 }, "nostr.local_store.resume_overlap"},
		"huge overlap":       {func(c *Config) { c.Nostr.LocalStore.ResumeOverlap = 48 * time.Hour }, "nostr.local_store.resume_overlap"},
		"negative lookback":  {func(c *Config) { c.Nostr.LocalStore.RegularLookback = -time.Hour }, "nostr.local_store.regular_lookback"},
		"unit-less lookback": {func(c *Config) { c.Nostr.LocalStore.RegularLookback = 86400 }, "nostr.local_store.regular_lookback"},
		"sidecar store reuse": {func(c *Config) {
			c.Nostr.Sidecar.Enabled = true
			c.Nostr.Sidecar.DataDir = sidecarDir
			c.Nostr.LocalStore.Path = filepath.Join(sidecarDir, "events.bolt")
		}, "relay sidecar's event store"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Defaults()
			tc.mutate(cfg)
			err := cfg.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestNostrLocalStoreLoadsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bahia.yaml")
	storePath := filepath.Join(t.TempDir(), "cache.bolt")
	yaml := "nostr:\n  local_store:\n    path: " + storePath + "\n    resume_overlap: 2m\n    regular_lookback: 0s\n    negentropy_upload: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	store := cfg.Nostr.LocalStore
	if store.Path != storePath || store.ResumeOverlap != 2*time.Minute || store.RegularLookback != 0 || !store.NegentropyUpload {
		t.Fatalf("loaded %+v", store)
	}
}

func TestNostrClosedRetryBudgetDefaultsAndValidation(t *testing.T) {
	cfg := Defaults()
	if cfg.Nostr.ClosedRetryBudget != ClosedRetryBudgetDefault {
		t.Fatalf("budget = %d, want %d", cfg.Nostr.ClosedRetryBudget, ClosedRetryBudgetDefault)
	}
	for _, budget := range []int{0, 1, ClosedRetryBudgetMax} {
		cfg.Nostr.ClosedRetryBudget = budget
		if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "closed_retry_budget") {
			t.Fatalf("budget %d rejected: %v", budget, err)
		}
	}
	for _, budget := range []int{-1, ClosedRetryBudgetMax + 1} {
		cfg.Nostr.ClosedRetryBudget = budget
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "nostr.closed_retry_budget") {
			t.Fatalf("budget %d validation = %v", budget, err)
		}
	}
}
