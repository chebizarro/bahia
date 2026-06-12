package nostrutil

import (
	"encoding/hex"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip44"
)

// GeneratePrivateKeyHex returns a new canonical Nostr secret key as lowercase hex.
func GeneratePrivateKeyHex() string {
	return canonicalnostr.Generate().Hex()
}

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

// PubKeyFromHex decodes a valid 32-byte hex pubkey.
func PubKeyFromHex(value string) (canonicalnostr.PubKey, error) {
	return canonicalnostr.PubKeyFromHex(strings.ToLower(strings.TrimSpace(value)))
}

// PubKeyHex returns the lowercase hex representation of a canonical pubkey.
func PubKeyHex(pubkey canonicalnostr.PubKey) string {
	return pubkey.Hex()
}

// EventIDHex returns the lowercase hex ID of a canonical Nostr event.
func EventIDHex(ev *canonicalnostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.ID.Hex()
}

// EventPubKeyHex returns the lowercase hex author pubkey of a canonical Nostr event.
func EventPubKeyHex(ev *canonicalnostr.Event) string {
	if ev == nil {
		return ""
	}
	return ev.PubKey.Hex()
}

// EventSignatureHex returns the lowercase hex signature of a canonical Nostr event.
func EventSignatureHex(ev *canonicalnostr.Event) string {
	if ev == nil {
		return ""
	}
	return SignatureHex(ev.Sig)
}

// NIP44ConversationKey derives a canonical NIP-44 conversation key from hex inputs.
func NIP44ConversationKey(recipientPubkeyHex string, privateKeyHex string) ([32]byte, error) {
	pubkey, err := PubKeyFromHex(recipientPubkeyHex)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode recipient pubkey: %w", err)
	}
	secret, err := SecretKeyFromHex(privateKeyHex)
	if err != nil {
		return [32]byte{}, err
	}
	key, err := nip44.GenerateConversationKey(pubkey, secret)
	if err != nil {
		return [32]byte{}, err
	}
	return key, nil
}

// EncodeNpubFromHex encodes a hex pubkey as an npub bech32 value.
func EncodeNpubFromHex(pubkeyHex string) (string, error) {
	pubkey, err := PubKeyFromHex(pubkeyHex)
	if err != nil {
		return "", err
	}
	return nip19.EncodeNpub(pubkey), nil
}

// DecodeNpubToHex decodes an npub bech32 value to lowercase hex.
func DecodeNpubToHex(value string) (string, error) {
	prefix, decoded, err := nip19.Decode(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if prefix != "npub" {
		return "", fmt.Errorf("invalid npub prefix %q", prefix)
	}
	pubkey, ok := decoded.(canonicalnostr.PubKey)
	if !ok {
		return "", fmt.Errorf("decoded npub has unexpected type %T", decoded)
	}
	return pubkey.Hex(), nil
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
