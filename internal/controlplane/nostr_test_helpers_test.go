package controlplane

import (
	"crypto/sha256"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func testNostrID(label string) nostr.ID {
	sum := sha256.Sum256([]byte(label))
	return nostr.ID(sum)
}

func testNostrPubKeyFromHex(t *testing.T, pubkey string) nostr.PubKey {
	t.Helper()
	parsed, err := nostr.PubKeyFromHex(strings.TrimSpace(pubkey))
	if err != nil {
		t.Fatalf("parse nostr pubkey %q: %v", pubkey, err)
	}
	return parsed
}

func testNostrKeypair() (privateKeyHex string, pubkeyHex string) {
	secret := nostr.Generate()
	return secret.Hex(), secret.Public().Hex()
}

func testNostrPubKeyHexFromPrivateKey(t *testing.T, privateKeyHex string) string {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(privateKeyHex))
	if err != nil {
		t.Fatalf("parse nostr secret key: %v", err)
	}
	return secret.Public().Hex()
}
