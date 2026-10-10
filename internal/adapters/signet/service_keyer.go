package signet

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

var _ nostr.Keyer = (*ServiceSigner)(nil)

var errServiceNIP04Unsupported = errors.New("NIP-04 is unsupported for the fenced Signet service key")

// OCK wraps and service-only fields are small. Bound untrusted payloads
// before base64 decoding, which otherwise allocates proportional to input.
const maxNIP44PayloadBase64 = 16 << 20

// Encrypt implements nostr.Keyer for OCK wraps and service-only NIP-44 layers
// with the standard NIP-46 nip44_encrypt [peer_pubkey, plaintext] request. The
// service key never leaves Signet.
func (s *ServiceSigner) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	if !utf8.ValidString(plaintext) || strings.IndexByte(plaintext, 0) >= 0 {
		return "", errors.New("NIP-44 text plaintext must be NUL-free UTF-8; use EncryptBytes for binary")
	}
	return s.nip44(ctx, "nip44_encrypt", recipient, plaintext, true)
}

// Decrypt implements nostr.Keyer for service-wrapped OCK recovery with the
// standard NIP-46 nip44_decrypt [peer_pubkey, ciphertext] request.
func (s *ServiceSigner) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	return s.nip44(ctx, "nip44_decrypt", sender, ciphertext, false)
}

// EncryptBytes uses Signet's binary-safe nip44_encrypt_b64
// [peer_pubkey, base64(plaintext)] request. NIP-46 JSON string params cannot
// carry arbitrary bytes, so only the transport is base64; the result is an
// ordinary NIP-44 v2 payload over the exact bytes.
func (s *ServiceSigner) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("NIP-44 plaintext must not be empty")
	}
	return s.nip44(ctx, signetMethodNIP44EncryptBinary, recipient, base64.StdEncoding.EncodeToString(plaintext), true)
}

func (s *ServiceSigner) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errServiceNIP04Unsupported
}

func (s *ServiceSigner) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errServiceNIP04Unsupported
}

func (s *ServiceSigner) nip44(ctx context.Context, method string, peer nostr.PubKey, input string, ciphertextResult bool) (string, error) {
	if s == nil {
		return "", ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if peer == nostr.ZeroPK || input == "" {
		return "", errors.New("NIP-44 peer pubkey and input are required")
	}
	if !ciphertextResult && !validNIP44Payload(input) {
		return "", errors.New("invalid NIP-44 ciphertext")
	}
	session, err := s.checkedSession(ctx)
	if err != nil {
		return "", err
	}
	result, err := session.rpc(ctx, method, []string{peer.Hex(), input})
	if err != nil {
		return "", fmt.Errorf("Signet %s: %w", method, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !session.alive() {
		return "", ErrNotConnected
	}
	if result == "" {
		return "", fmt.Errorf("Signet %s returned no result", method)
	}
	if ciphertextResult && !validNIP44Payload(result) {
		return "", errors.New("Signet returned malformed NIP-44 ciphertext")
	}
	if !ciphertextResult && (!utf8.ValidString(result) || strings.IndexByte(result, 0) >= 0) {
		return "", errors.New("Signet returned invalid or NUL-containing UTF-8 NIP-44 plaintext")
	}
	return result, nil
}

func validNIP44Payload(payload string) bool {
	if len(payload) < 132 || len(payload) > maxNIP44PayloadBase64 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	return err == nil && len(decoded) >= 99 && decoded[0] == 2
}
