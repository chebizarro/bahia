package signet

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

var _ nostr.Keyer = (*EpochSigner)(nil)

var errEpochNIP04Unsupported = errors.New("NIP-04 is unsupported for the fenced Signet service key")

// OCK wraps and service-only fields are small. Bound untrusted payloads
// before base64 decoding, which otherwise allocates proportional to input.
const maxEpochNIP44PayloadBase64 = 16 << 20

// Encrypt implements nostr.Keyer for OCK wraps and service-only NIP-44 layers.
// The service key never leaves Signet; the request carries the current lease
// epoch as a third NIP-46 parameter. It has no legacy no-epoch fallback.
func (s *EpochSigner) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	if !utf8.ValidString(plaintext) || strings.IndexByte(plaintext, 0) >= 0 {
		return "", errors.New("NIP-44 text plaintext must be NUL-free UTF-8; use EncryptBytes for binary")
	}
	return s.fencedNIP44(ctx, "nip44_encrypt", recipient, plaintext, true)
}

// Decrypt implements nostr.Keyer for service-wrapped OCK recovery.
func (s *EpochSigner) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	return s.fencedNIP44(ctx, "nip44_decrypt", sender, ciphertext, false)
}

// EncryptBytes uses Signet's binary-safe extension. NIP-46 JSON string params
// cannot carry arbitrary bytes without corruption, so this RPC base64-encodes
// the input before transport and still returns ordinary NIP-44 ciphertext.
func (s *EpochSigner) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("NIP-44 plaintext must not be empty")
	}
	return s.fencedNIP44(ctx, "nip44_encrypt_b64", recipient, base64.StdEncoding.EncodeToString(plaintext), true)
}

// decryptBytes uses Signet's binary-safe extension; an empty or malformed
// base64 response is an error, never a successful zero-value plaintext.
func (s *EpochSigner) decryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error) {
	encoded, err := s.fencedNIP44(ctx, "nip44_decrypt_b64", sender, ciphertext, false)
	if err != nil {
		return nil, err
	}
	plaintext, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(plaintext) == 0 {
		return nil, errors.New("Signet returned malformed or empty NIP-44 binary plaintext")
	}
	return plaintext, nil
}

func (s *EpochSigner) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errEpochNIP04Unsupported
}

func (s *EpochSigner) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", errEpochNIP04Unsupported
}

func (s *EpochSigner) fencedNIP44(ctx context.Context, method string, peer nostr.PubKey, input string, ciphertextResult bool) (string, error) {
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
	lease, err := s.lease(ctx)
	if err != nil {
		return "", fmt.Errorf("read Signet writer lease: %w", err)
	}
	if err := s.checkLease(lease); err != nil {
		return "", err
	}
	if !session.alive() {
		return "", ErrNotConnected
	}
	result, err := session.rpc(ctx, method, []string{peer.Hex(), input, strconv.FormatUint(lease.Epoch, 10)})
	if err != nil {
		return "", fmt.Errorf("Signet epoch %s: %w", method, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !session.alive() {
		return "", ErrNotConnected
	}
	if result == "" {
		return "", fmt.Errorf("Signet epoch %s returned no result", method)
	}
	if ciphertextResult && !validNIP44Payload(result) {
		return "", errors.New("Signet returned malformed NIP-44 ciphertext")
	}
	if !ciphertextResult && method == "nip44_decrypt" && (!utf8.ValidString(result) || strings.IndexByte(result, 0) >= 0) {
		return "", errors.New("Signet returned invalid or NUL-containing UTF-8 NIP-44 plaintext")
	}
	current, err := s.lease(ctx)
	if err != nil {
		return "", fmt.Errorf("recheck Signet writer lease: %w", err)
	}
	if current.Epoch != lease.Epoch || current.OwnerPubkey != lease.OwnerPubkey {
		return "", errors.New("Signet writer lease changed during NIP-44 operation")
	}
	if err := s.checkLease(current); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease.Epoch < s.highestEpoch || !s.now().Before(current.ExpiresAt) {
		return "", errors.New("Signet writer lease became stale before NIP-44 result acceptance")
	}
	if !session.alive() {
		return "", ErrNotConnected
	}
	return result, nil
}

func validNIP44Payload(payload string) bool {
	if len(payload) < 132 || len(payload) > maxEpochNIP44PayloadBase64 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(payload)
	return err == nil && len(decoded) >= 99 && decoded[0] == 2
}
