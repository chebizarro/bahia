package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testServicePubkey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	testClientSecret  = "2222222222222222222222222222222222222222222222222222222222222222"
	testBunkerURI     = "bunker://0000000000000000000000000000000000000000000000000000000000000003?relay=wss%3A%2F%2Frelay.example&secret=pairing-secret"
)

func TestLoadServiceSignerFromEnv(t *testing.T) {
	t.Setenv("BAHIA_NOSTR_PUBLIC_KEY", strings.ToUpper(testServicePubkey))
	t.Setenv("BAHIA_NOSTR_SIGNER_METHOD", "NIP46")
	t.Setenv("BAHIA_NOSTR_SIGNER_BUNKER_URI", testBunkerURI)
	t.Setenv("BAHIA_NOSTR_SIGNER_CLIENT_SECRET_KEY", testClientSecret)
	t.Setenv("BAHIA_NOSTR_SIGNER_TIMEOUT", "5s")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	signer := cfg.Nostr.Signer
	if cfg.Nostr.PublicKey != testServicePubkey || signer.Method != NostrSignerNIP46 || signer.BunkerURI != testBunkerURI || signer.ClientSecretKey != testClientSecret || signer.Timeout != 5*time.Second {
		t.Fatalf("loaded signer config = %q %+v", cfg.Nostr.PublicKey, signer)
	}
	if cfg.Nostr.ServiceSignerMethod() != NostrSignerNIP46 {
		t.Fatalf("method = %q", cfg.Nostr.ServiceSignerMethod())
	}
}

func TestLoadNIP55LSignerFromEnvDefaultsAppID(t *testing.T) {
	t.Setenv("BAHIA_NOSTR_PUBLIC_KEY", testServicePubkey)
	t.Setenv("BAHIA_NOSTR_SIGNER_METHOD", "nip55l")
	t.Setenv("BAHIA_NOSTR_SIGNER_NIP55L_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got := cfg.Nostr.Signer
	if got.NIP55L.BusAddress != "unix:path=/run/user/1000/bus" || got.NIP55L.AppID != "bahia" || got.Timeout != 330*time.Second {
		t.Fatalf("nip55l config = %+v", got)
	}
}

func TestServiceSignerValidation(t *testing.T) {
	local := "1111111111111111111111111111111111111111111111111111111111111111"
	nip46 := func(n *NostrConfig) {
		n.PublicKey = testServicePubkey
		n.Signer = NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}
	}
	for name, tc := range map[string]struct {
		mutate func(*NostrConfig)
		want   string
	}{
		"empty is no identity": {mutate: func(*NostrConfig) {}},
		"implicit local":       {mutate: func(n *NostrConfig) { n.PrivateKey = local }},
		"explicit local":       {mutate: func(n *NostrConfig) { n.PrivateKey = local; n.Signer.Method = "Local" }},
		"nip46":                {mutate: nip46},
		"nip46 key file": {mutate: func(n *NostrConfig) {
			nip46(n)
			n.Signer.ClientSecretKey = ""
			n.Signer.ClientSecretKeyFile = "/run/secrets/bahia-nip46"
		}},
		"nip55l":                   {mutate: func(n *NostrConfig) { n.PublicKey = testServicePubkey; n.Signer.Method = NostrSignerNIP55L }},
		"unknown method":           {mutate: func(n *NostrConfig) { n.Signer.Method = "signet" }, want: "must be one of local, nip46, nip55l"},
		"local without key":        {mutate: func(n *NostrConfig) { n.Signer.Method = NostrSignerLocal }, want: "requires nostr.private_key"},
		"nip46 mixed with raw key": {mutate: func(n *NostrConfig) { nip46(n); n.PrivateKey = local }, want: "nostr.private_key must not be set"},
		"nip55l mixed with raw key": {mutate: func(n *NostrConfig) {
			n.PublicKey = testServicePubkey
			n.Signer.Method = NostrSignerNIP55L
			n.PrivateKey = local
		}, want: "nostr.private_key must not be set"},
		"nip46 without pubkey":     {mutate: func(n *NostrConfig) { nip46(n); n.PublicKey = "" }, want: "requires nostr.public_key"},
		"nip46 without bunker":     {mutate: func(n *NostrConfig) { nip46(n); n.Signer.BunkerURI = "" }, want: "requires nostr.signer.bunker_uri"},
		"nip46 without client key": {mutate: func(n *NostrConfig) { nip46(n); n.Signer.ClientSecretKey = "" }, want: "exactly one of"},
		"nip46 with both keys":     {mutate: func(n *NostrConfig) { nip46(n); n.Signer.ClientSecretKeyFile = "/run/secrets/k" }, want: "exactly one of"},
		"nip46 relative key file":  {mutate: func(n *NostrConfig) { nip46(n); n.Signer.ClientSecretKey = ""; n.Signer.ClientSecretKeyFile = "k" }, want: "absolute path"},
		"nip46 malformed key":      {mutate: func(n *NostrConfig) { nip46(n); n.Signer.ClientSecretKey = "nsec1x" }, want: "client_secret_key must be 64 hex"},
		"nip46 with nip55l fields": {mutate: func(n *NostrConfig) { nip46(n); n.Signer.NIP55L.AppID = "x" }, want: "require nostr.signer.method=nip55l"},
		"nip55l with bunker": {mutate: func(n *NostrConfig) {
			n.PublicKey = testServicePubkey
			n.Signer.Method = NostrSignerNIP55L
			n.Signer.BunkerURI = testBunkerURI
		}, want: "require nostr.signer.method=nip46"},
		"remote fields, no method": {mutate: func(n *NostrConfig) { n.PrivateKey = local; n.Signer.BunkerURI = testBunkerURI }, want: "require nostr.signer.method nip46 or nip55l"},
		"malformed pubkey":         {mutate: func(n *NostrConfig) { nip46(n); n.PublicKey = "npub1x" }, want: "nostr.public_key must be 64 hex"},
		"nip55l short timeout": {mutate: func(n *NostrConfig) {
			n.PublicKey = testServicePubkey
			n.Signer.Method = NostrSignerNIP55L
			n.Signer.Timeout = 30 * time.Second
		}, want: "at least 5m0s for nostr.signer.method=nip55l"},
		"negative timeout": {mutate: func(n *NostrConfig) { nip46(n); n.Signer.Timeout = -time.Second }, want: "must not be negative"},
	} {
		t.Run(name, func(t *testing.T) {
			var n NostrConfig
			tc.mutate(&n)
			err := n.validateServiceSigner()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateServiceSigner() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateServiceSigner() = %v, want %q", err, tc.want)
			}
			for _, secret := range []string{testClientSecret, "pairing-secret", local} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("validation error leaked a secret")
				}
			}
		})
	}
}

func TestRemoteSignerAssistantRequiresWrappedKeys(t *testing.T) {
	cfg := &Config{Nostr: NostrConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}}}
	cfg.Assistant = AssistantConfig{Enabled: true, LLMBaseURL: "https://llm.example", LLMModel: "m"}
	if err := cfg.validateAssistant(); err == nil || !strings.Contains(err.Error(), "wrapped_read_only is required") {
		t.Fatalf("remote signer with legacy_v1 assistant keys = %v", err)
	}
}

func TestRemovedWrappedKeySignerSettingsAreRejected(t *testing.T) {
	for _, key := range []string{"signet_bunker_uri", "owner_client_secret_key", "connect_timeout"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := fmt.Sprintf("assistant:\n  wrapped_keys:\n    %s: x\n", key)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "assistant.wrapped_keys."+key+" has been removed") || !strings.Contains(err.Error(), "nostr.signer") {
				t.Fatalf("Load() = %v", err)
			}
		})
	}
}

func TestServiceSignerConfigRedactsSecrets(t *testing.T) {
	n := NostrConfig{PublicKey: testServicePubkey, Signer: NostrSignerConfig{Method: NostrSignerNIP46, BunkerURI: testBunkerURI, ClientSecretKey: testClientSecret}}
	for _, rendered := range []string{fmt.Sprint(n), fmt.Sprintf("%+v", n), fmt.Sprint(n.Signer), fmt.Sprintf("%#v", n.Signer)} {
		if strings.Contains(rendered, testClientSecret) || strings.Contains(rendered, "pairing-secret") {
			t.Fatalf("rendered signer config leaked a secret: %s", rendered)
		}
	}
}
