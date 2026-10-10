package servicesigner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
)

func TestSameSigner(t *testing.T) {
	service := nostr.Generate()
	local := config.NostrConfig{PrivateKey: service.Hex(), PublicKey: service.Public().Hex()}
	nip46 := config.NostrConfig{PublicKey: service.Public().Hex(), Signer: config.NostrSignerConfig{
		Method: config.NostrSignerNIP46, BunkerURI: "bunker://abc?relay=wss://r", ClientSecretKey: nostr.Generate().Hex(), Timeout: 30 * time.Second,
	}}
	change := func(base config.NostrConfig, edit func(*config.NostrConfig)) config.NostrConfig {
		edit(&base)
		return base
	}
	for name, tc := range map[string]struct {
		a, b config.NostrConfig
		want bool
	}{
		"no identity":                {config.NostrConfig{}, config.NostrConfig{}, true},
		"identical local":            {local, local, true},
		"explicit local method":      {local, change(local, func(c *config.NostrConfig) { c.Signer.Method = " Local " }), true},
		"private key whitespace":     {local, change(local, func(c *config.NostrConfig) { c.PrivateKey = " " + c.PrivateKey + "\n" }), true},
		"public key case":            {local, change(local, func(c *config.NostrConfig) { c.PublicKey = strings.ToUpper(c.PublicKey) }), true},
		"unrelated nostr settings":   {nip46, change(nip46, func(c *config.NostrConfig) { c.Relays = []string{"wss://other"} }), true},
		"rotated service key":        {local, change(local, func(c *config.NostrConfig) { c.PrivateKey = nostr.Generate().Hex() }), false},
		"pinned pubkey":              {local, change(local, func(c *config.NostrConfig) { c.PublicKey = "" }), false},
		"method":                     {local, nip46, false},
		"identity added":             {config.NostrConfig{}, local, false},
		"bunker uri":                 {nip46, change(nip46, func(c *config.NostrConfig) { c.Signer.BunkerURI += "&relay=wss://s" }), false},
		"client key":                 {nip46, change(nip46, func(c *config.NostrConfig) { c.Signer.ClientSecretKey = nostr.Generate().Hex() }), false},
		"timeout":                    {nip46, change(nip46, func(c *config.NostrConfig) { c.Signer.Timeout = time.Minute }), false},
		"unresolved client key file": {change(nip46, func(c *config.NostrConfig) { c.Signer.ClientSecretKeyFile = "/k" }), change(nip46, func(c *config.NostrConfig) { c.Signer.ClientSecretKeyFile = "/k" }), false},
	} {
		if got := SameSigner(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameSigner = %v, want %v", name, got, tc.want)
		}
		if got := SameSigner(tc.b, tc.a); got != tc.want {
			t.Errorf("%s (swapped): SameSigner = %v, want %v", name, got, tc.want)
		}
	}
}

func TestResolveClientKeyFileComparesKeyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.key")
	write := func(key nostr.SecretKey) {
		if err := os.WriteFile(path, []byte(strings.ToUpper(key.Hex())+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.NostrConfig{PublicKey: nostr.Generate().Public().Hex(), Signer: config.NostrSignerConfig{
		Method: config.NostrSignerNIP46, BunkerURI: "bunker://abc?relay=wss://r", ClientSecretKeyFile: path,
	}}
	first := nostr.Generate()
	write(first)
	opened, err := ResolveClientKeyFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Signer.ClientSecretKeyFile != "" || opened.Signer.ClientSecretKey != first.Hex() {
		t.Fatalf("resolved signer = file %q key match %v", opened.Signer.ClientSecretKeyFile, opened.Signer.ClientSecretKey == first.Hex())
	}
	again, _ := ResolveClientKeyFile(cfg)
	if !SameSigner(opened, again) {
		t.Fatal("unchanged key file content must be the same signer")
	}
	inline := cfg
	inline.Signer.ClientSecretKeyFile, inline.Signer.ClientSecretKey = "", first.Hex()
	if !SameSigner(opened, inline) {
		t.Fatal("moving the same client key inline must be the same signer")
	}
	write(nostr.Generate())
	rotated, _ := ResolveClientKeyFile(cfg)
	if SameSigner(opened, rotated) {
		t.Fatal("a rotated key file under the same path must be a different signer")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveClientKeyFile(cfg); err == nil || !strings.Contains(err.Error(), "client_secret_key_file") {
		t.Fatalf("missing key file error = %v", err)
	}
}
