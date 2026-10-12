package service

import (
	"context"
	"crypto/cipher"
	"fmt"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/secretstore"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeyringTranscriptKeys serves assistant transcript and checkpoint keys from
// the assistant-transcript system keys (secret_store.system_keys). Ciphertext
// names its key by the credential's label (key_ref,
// "bahia-system/assistant-transcript/<key-id>") and Signet credential version
// (key_version), so a key is never used for data sealed under another one.
// The first configured key seals new events; the others only open old ones.
type KeyringTranscriptKeys struct {
	Ring *secretstore.Keyring
}

const assistantTranscriptKeyRefPrefix = secretstore.SystemLabelNamespace + "/" + secretstore.PurposeAssistantTranscript + "/"

// WithActiveKey lends the cipher of the key that seals new events.
func (k KeyringTranscriptKeys) WithActiveKey(ctx context.Context, fn func(AssistantTranscriptKeyRef, cipher.AEAD) error) error {
	return k.use(ctx, "", fn)
}

// WithKey lends the cipher of the key an event names by key_ref and
// key_version.
func (k KeyringTranscriptKeys) WithKey(ctx context.Context, keyRef, keyVersion string, fn func(cipher.AEAD) error) error {
	keyID, ok := strings.CutPrefix(strings.TrimSpace(keyRef), assistantTranscriptKeyRefPrefix)
	if !ok || keyID == "" {
		return fmt.Errorf("assistant transcript key %q is not a Signet system key; data sealed under it is readable only after operator re-encryption", keyRef)
	}
	return k.use(ctx, keyID, func(ref AssistantTranscriptKeyRef, aead cipher.AEAD) error {
		if ref.Version != strings.TrimSpace(keyVersion) {
			return fmt.Errorf("assistant transcript key %q version %q is not available (configured version %s)", keyRef, keyVersion, ref.Version)
		}
		return fn(aead)
	})
}

// use builds the cipher from the keyring's own buffer, under the keyring's
// lock: no copy of the key exists outside the keyring except the one the
// XChaCha20-Poly1305 implementation keeps for the cipher's lifetime.
func (k KeyringTranscriptKeys) use(ctx context.Context, keyID string, fn func(AssistantTranscriptKeyRef, cipher.AEAD) error) error {
	if k.Ring == nil {
		return fmt.Errorf("%w: no assistant transcript system key", secretstore.ErrNotConfigured)
	}
	return k.Ring.Use(ctx, secretstore.PurposeAssistantTranscript, keyID, func(ref secretstore.SystemKeyRef, raw []byte) error {
		aead, err := chacha20poly1305.NewX(raw)
		if err != nil {
			return err
		}
		return fn(AssistantTranscriptKeyRef{Ref: ref.Label(), Version: strconv.Itoa(ref.Credential.Version)}, aead)
	})
}
