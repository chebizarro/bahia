package nostrutil

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
)

// ErrServiceKeyMaterialRequired is returned by every feature that derives
// keys from the raw service private key (HKDF, HMAC or SHA-256 over the nsec)
// when the configured service signer keeps that key outside this process.
// No signer can serve those derivations; each is blocked until its
// bahia-cd0wr.4.x migration (4.7 service secrets, 4.8 assistant transcripts,
// legacy O1 OCK, confidential-state dedupe) replaces the raw-key dependency.
var ErrServiceKeyMaterialRequired = errors.New("service key material required: the configured service signer holds no in-process private key, and this feature derives keys from the raw nsec (blocked on the bahia-cd0wr.4.x raw-key migrations)")

// ServiceKeyMaterialHolder is the optional capability of a service Keyer
// whose private key lives in this process. Only local mode provides it.
type ServiceKeyMaterialHolder interface {
	// ServiceKeyMaterial returns the private key text exactly as configured,
	// so legacy derivations reproduce their deployed keys byte for byte.
	ServiceKeyMaterial() string
}

// LocalKeyer is the local-mode service identity: in-process signing and
// NIP-44, plus the raw key material that legacy derivations still need.
type LocalKeyer struct {
	keyer.KeySigner
	material string
}

var (
	_ canonicalnostr.Keyer     = LocalKeyer{}
	_ ServiceKeyMaterialHolder = LocalKeyer{}
)

// NewLocalKeyer builds the local service Keyer from a configured 32-byte hex
// private key. Errors never echo the key text.
func NewLocalKeyer(privateKeyHex string) (LocalKeyer, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(privateKeyHex))
	if err != nil {
		return LocalKeyer{}, errors.New("decode nostr private key: not valid hex")
	}
	if len(decoded) != 32 {
		return LocalKeyer{}, fmt.Errorf("nostr private key must be 32 bytes, got %d", len(decoded))
	}
	var secret [32]byte
	copy(secret[:], decoded)
	return LocalKeyer{KeySigner: keyer.NewPlainKeySigner(secret), material: privateKeyHex}, nil
}

// ServiceKeyMaterial implements ServiceKeyMaterialHolder.
func (k LocalKeyer) ServiceKeyMaterial() string { return k.material }

// RequireServiceKeyMaterial returns the in-process service key material of
// signer, or an error wrapping ErrServiceKeyMaterialRequired that names
// feature when the signer is absent or keeps its key elsewhere.
func RequireServiceKeyMaterial(signer canonicalnostr.User, feature string) (string, error) {
	if holder, ok := signer.(ServiceKeyMaterialHolder); ok {
		if material := holder.ServiceKeyMaterial(); strings.TrimSpace(material) != "" {
			return material, nil
		}
	}
	return "", fmt.Errorf("%s: %w", feature, ErrServiceKeyMaterialRequired)
}
