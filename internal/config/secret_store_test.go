package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validSignetSecretStore() SecretStoreConfig {
	return SecretStoreConfig{Backend: " Signet ", Signet: SecretStoreSignetConfig{
		Pubkey:        strings.Repeat("AB", 32),
		Relays:        []string{"wss://signet.example/relay, wss://signet.example/relay", " ws://10.0.0.2:7777 "},
		ClientKeyFile: "/run/secrets/bahia-secret-store.key",
		Owner:         " bahia-vault ",
	}}
}

func TestSecretStoreSignetConfigNormalizes(t *testing.T) {
	cfg := validSignetSecretStore()
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Backend != SecretStoreBackendSignet || cfg.Signet.Pubkey != strings.Repeat("ab", 32) || cfg.Signet.Owner != "bahia-vault" {
		t.Fatalf("not normalized: %+v", cfg)
	}
	if got := strings.Join(cfg.Signet.Relays, " "); got != "wss://signet.example/relay ws://10.0.0.2:7777" {
		t.Fatalf("relays = %q", got)
	}
	if cfg.Signet.RequestTimeout != 30*time.Second {
		t.Fatalf("default timeout = %v", cfg.Signet.RequestTimeout)
	}
}

func TestSecretStoreConfigRejects(t *testing.T) {
	for name, mutate := range map[string]func(*SecretStoreConfig){
		"unknown backend":      func(c *SecretStoreConfig) { c.Backend = "vault" },
		"settings w/o backend": func(c *SecretStoreConfig) { c.Backend = "" },
		"short pubkey":         func(c *SecretStoreConfig) { c.Signet.Pubkey = "abcd" },
		"no relays":            func(c *SecretStoreConfig) { c.Signet.Relays = []string{" , "} },
		"http relay":           func(c *SecretStoreConfig) { c.Signet.Relays = []string{"https://signet.example"} },
		"relative key file":    func(c *SecretStoreConfig) { c.Signet.ClientKeyFile = "secrets/key" },
		"no key file":          func(c *SecretStoreConfig) { c.Signet.ClientKeyFile = "" },
		"no owner":             func(c *SecretStoreConfig) { c.Signet.Owner = " " },
		"oversized owner":      func(c *SecretStoreConfig) { c.Signet.Owner = strings.Repeat("o", 129) },
		"negative timeout":     func(c *SecretStoreConfig) { c.Signet.RequestTimeout = -time.Second },
	} {
		cfg := validSignetSecretStore()
		mutate(&cfg)
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "secret_store") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	var none SecretStoreConfig
	if err := none.validate(); err != nil {
		t.Fatalf("an unconfigured store must validate: %v", err)
	}
}

func TestLoadSecretStoreFromYAMLAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`mode: full
secret_store:
  backend: signet
  signet:
    pubkey: "` + strings.Repeat("a", 64) + `"
    relays: ["wss://signet.example"]
    client_key_file: /run/secrets/bahia-secret-store.key
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BAHIA_SECRET_STORE_SIGNET_OWNER", "bahia-vault")
	t.Setenv("BAHIA_SECRET_STORE_SIGNET_REQUEST_TIMEOUT", "12s")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got := cfg.SecretStore
	if got.Backend != SecretStoreBackendSignet || got.Signet.Owner != "bahia-vault" || got.Signet.RequestTimeout != 12*time.Second ||
		got.Signet.ClientKeyFile != "/run/secrets/bahia-secret-store.key" || len(got.Signet.Relays) != 1 {
		t.Fatalf("secret_store = %+v", got)
	}
}
