package nostrutil

import (
	"context"
	"errors"
	"strings"
	"testing"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
)

func TestLocalKeyerKeepsIdentityAndMaterial(t *testing.T) {
	secret := canonicalnostr.Generate()
	configured := strings.ToUpper(secret.Hex()) + "\n"
	local, err := NewLocalKeyer(configured)
	if err != nil {
		t.Fatal(err)
	}
	pubkey, _ := local.GetPublicKey(context.Background())
	if pubkey != secret.Public() {
		t.Fatalf("pubkey = %s, want %s", pubkey.Hex(), secret.Public().Hex())
	}
	material, err := RequireServiceKeyMaterial(local, "test")
	if err != nil || material != configured {
		t.Fatalf("material = %q, %v; want the configured text verbatim", material, err)
	}
}

func TestRemoteSignerHasNoServiceKeyMaterial(t *testing.T) {
	remote := keyer.NewPlainKeySigner(canonicalnostr.Generate())
	for name, signer := range map[string]canonicalnostr.User{"remote": remote, "absent": nil} {
		_, err := RequireServiceKeyMaterial(signer, "legacy derivation")
		if !errors.Is(err, ErrServiceKeyMaterialRequired) || !strings.Contains(err.Error(), "legacy derivation") {
			t.Fatalf("%s: err = %v, want named ErrServiceKeyMaterialRequired", name, err)
		}
	}
}

func TestKeyParseErrorsNeverEchoKeyText(t *testing.T) {
	bad := strings.Repeat("ab", 31) + "zz"
	if _, err := NewLocalKeyer(bad); err == nil || strings.Contains(err.Error(), bad) {
		t.Fatalf("NewLocalKeyer error = %v; must fail without echoing the key", err)
	}
	if _, err := SecretKeyFromHex(bad); err == nil || strings.Contains(err.Error(), bad) {
		t.Fatalf("SecretKeyFromHex error = %v; must fail without echoing the key", err)
	}
}
