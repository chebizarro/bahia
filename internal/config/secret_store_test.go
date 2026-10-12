package config

import (
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
