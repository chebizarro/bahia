package client

import (
	"encoding/hex"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
)

// NormalizeNostrPrivateKey returns a 64-character hex private key from hex or nsec input.
func NormalizeNostrPrivateKey(privateKey string) (string, error) {
	key := strings.TrimSpace(privateKey)
	if key == "" {
		return "", fmt.Errorf("nostr private key is required")
	}
	if strings.HasPrefix(key, "nsec") {
		prefix, value, err := nip19.Decode(key)
		if err != nil {
			return "", fmt.Errorf("decode nsec: %w", err)
		}
		if prefix != "nsec" {
			return "", fmt.Errorf("expected nsec key, got %s", prefix)
		}
		sk, ok := value.(nostr.SecretKey)
		if !ok {
			return "", fmt.Errorf("decoded nsec did not contain a private key")
		}
		key = sk.Hex()
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("nostr private key must be 32 bytes of hex or nsec")
	}
	if _, err := nostr.SecretKeyFromHex(key); err != nil {
		return "", fmt.Errorf("parse Nostr private key: %w", err)
	}
	return strings.ToLower(key), nil
}
