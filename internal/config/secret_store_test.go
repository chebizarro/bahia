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
		"public relay": func(c *SecretStoreConfig) {
			c.Signet.Relays = []string{"wss://signet.example", "wss://relay." + "dam" + "us.io"}
		},
		"relative key file": func(c *SecretStoreConfig) { c.Signet.ClientKeyFile = "secrets/key" },
		"no key file":       func(c *SecretStoreConfig) { c.Signet.ClientKeyFile = "" },
		"no owner":          func(c *SecretStoreConfig) { c.Signet.Owner = " " },
		"oversized owner":   func(c *SecretStoreConfig) { c.Signet.Owner = strings.Repeat("o", 129) },
		"negative timeout":  func(c *SecretStoreConfig) { c.Signet.RequestTimeout = -time.Second },
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

func systemKey(id string, c byte) SystemKeyConfig {
	return SystemKeyConfig{KeyID: id, CredentialID: "cred_" + strings.Repeat(string(c), 64), Version: 1}
}

func TestSystemKeysConfig(t *testing.T) {
	cfg := validSignetSecretStore()
	cfg.SystemKeys = SystemKeysConfig{
		AssistantTranscript: []SystemKeyConfig{{KeyID: " k2 ", CredentialID: " CRED_" + strings.Repeat("A", 64), Version: 2}, systemKey("k1", 'b')},
		ConfidentialDedupe:  []SystemKeyConfig{systemKey("k1", 'c')},
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.SystemKeys.AssistantTranscript[0]; got.KeyID != "k2" || got.CredentialID != "cred_"+strings.Repeat("a", 64) {
		t.Fatalf("not normalized: %+v", got)
	}
	for name, mutate := range map[string]func(*SecretStoreConfig){
		"no backend": func(c *SecretStoreConfig) { *c = SecretStoreConfig{SystemKeys: c.SystemKeys} },
		"two dedupe keys": func(c *SecretStoreConfig) {
			c.SystemKeys.ConfidentialDedupe = append(c.SystemKeys.ConfidentialDedupe, systemKey("k2", 'd'))
		},
		"duplicate key id":  func(c *SecretStoreConfig) { c.SystemKeys.AssistantTranscript[1].KeyID = "k2" },
		"bad key id":        func(c *SecretStoreConfig) { c.SystemKeys.AssistantTranscript[0].KeyID = "K/2" },
		"empty key id":      func(c *SecretStoreConfig) { c.SystemKeys.ConfidentialDedupe[0].KeyID = "" },
		"bad credential id": func(c *SecretStoreConfig) { c.SystemKeys.ConfidentialDedupe[0].CredentialID = "cred_xyz" },
		"zero version":      func(c *SecretStoreConfig) { c.SystemKeys.ConfidentialDedupe[0].Version = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := validSignetSecretStore()
			bad.SystemKeys = SystemKeysConfig{
				AssistantTranscript: []SystemKeyConfig{systemKey("k2", 'a'), systemKey("k1", 'b')},
				ConfidentialDedupe:  []SystemKeyConfig{systemKey("k1", 'c')},
			}
			mutate(&bad)
			if err := bad.validate(); err == nil || !strings.Contains(err.Error(), "secret_store.system_keys") {
				t.Fatalf("validate() = %v", err)
			}
		})
	}
}

func TestSystemKeysConfigLoadsFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `secret_store:
  backend: signet
  signet:
    pubkey: ` + strings.Repeat("ab", 32) + `
    relays: [wss://signet.example/relay]
    client_key_file: /run/secrets/bahia-secret-store.key
    owner: bahia-vault
  system_keys:
    assistant_transcript:
      - {key_id: k2, credential_id: cred_` + strings.Repeat("a", 64) + `, version: 3}
      - {key_id: k1, credential_id: cred_` + strings.Repeat("b", 64) + `, version: 1}
    confidential_dedupe:
      - {key_id: k1, credential_id: cred_` + strings.Repeat("c", 64) + `, version: 1}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := cfg.SecretStore.SystemKeys
	if len(keys.AssistantTranscript) != 2 || keys.AssistantTranscript[0].KeyID != "k2" || keys.AssistantTranscript[0].Version != 3 || len(keys.ConfidentialDedupe) != 1 {
		t.Fatalf("system keys = %+v", keys)
	}
}

// assistantTranscriptKeyYAML configures the Signet store and the assistant
// transcript system key that assistant.enabled requires.
const assistantTranscriptKeyYAML = `secret_store:
  backend: signet
  signet:
    pubkey: abababababababababababababababababababababababababababababababab
    relays: [wss://signet.example/relay]
    client_key_file: /run/secrets/bahia-secret-store.key
    owner: bahia-vault
  system_keys:
    assistant_transcript:
      - {key_id: k1, credential_id: cred_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, version: 1}
`

func withAssistantTranscriptKey(cfg *Config) {
	cfg.SecretStore = validSignetSecretStore()
	cfg.SecretStore.SystemKeys.AssistantTranscript = []SystemKeyConfig{systemKey("k1", 'a')}
}
