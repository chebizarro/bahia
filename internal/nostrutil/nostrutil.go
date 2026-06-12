package nostrutil

import (
	"encoding/hex"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
)

// SecretKeyFromHex decodes Bahia's configured 32-byte hex Nostr secret into the
// canonical fiatjaf.com/nostr key type.
func SecretKeyFromHex(privateKeyHex string) (canonicalnostr.SecretKey, error) {
	privateKeyHex = strings.TrimSpace(privateKeyHex)
	secret, err := canonicalnostr.SecretKeyFromHex(privateKeyHex)
	if err != nil {
		return canonicalnostr.SecretKey{}, fmt.Errorf("decode nostr private key: %w", err)
	}
	return secret, nil
}

// SignEventWithHexKey signs a canonical Nostr event with Bahia's configured
// 32-byte hex private key.
func SignEventWithHexKey(ev *canonicalnostr.Event, privateKeyHex string) error {
	if ev == nil {
		return fmt.Errorf("nostr event is nil")
	}
	secret, err := SecretKeyFromHex(privateKeyHex)
	if err != nil {
		return err
	}
	return ev.Sign(secret)
}

// PublicKeyHexFromPrivateKeyHex derives the canonical public key hex from a
// configured 32-byte private key hex value.
func PublicKeyHexFromPrivateKeyHex(privateKeyHex string) (string, error) {
	secret, err := SecretKeyFromHex(privateKeyHex)
	if err != nil {
		return "", err
	}
	return secret.Public().Hex(), nil
}

// SignatureHex returns the lowercase hex representation of a Nostr Schnorr signature.
func SignatureHex(sig [64]byte) string {
	return canonicalnostr.HexEncodeToString(sig[:])
}

// DecodeSignatureHex decodes a 64-byte Schnorr signature hex string.
func DecodeSignatureHex(signatureHex string) ([64]byte, error) {
	var sig [64]byte
	signatureHex = strings.TrimSpace(signatureHex)
	if len(signatureHex) != 128 {
		return sig, fmt.Errorf("signature must be 128 hex characters")
	}
	if _, err := hex.Decode(sig[:], []byte(signatureHex)); err != nil {
		return sig, fmt.Errorf("signature must be valid hex: %w", err)
	}
	return sig, nil
}

// KindsFromInts converts Bahia kind constants into canonical Nostr filter kinds.
func KindsFromInts(kinds []int) []canonicalnostr.Kind {
	if len(kinds) == 0 {
		return nil
	}
	out := make([]canonicalnostr.Kind, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, canonicalnostr.Kind(kind))
	}
	return out
}

// PubKeysFromHex decodes valid 32-byte hex pubkeys for canonical filter authors.
func PubKeysFromHex(values []string) ([]canonicalnostr.PubKey, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]canonicalnostr.PubKey, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		pubkey, err := canonicalnostr.PubKeyFromHex(value)
		if err != nil {
			return nil, fmt.Errorf("decode pubkey %q: %w", raw, err)
		}
		out = append(out, pubkey)
	}
	return out, nil
}
