package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/secretstore"
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

// ActiveTranscriptKey returns the key that seals new events.
func (k KeyringTranscriptKeys) ActiveTranscriptKey(ctx context.Context) (AssistantTranscriptKey, error) {
	return k.key(ctx, "")
}

// TranscriptKey returns the key named by an event's key_ref and key_version.
func (k KeyringTranscriptKeys) TranscriptKey(ctx context.Context, keyRef, keyVersion string) (AssistantTranscriptKey, error) {
	keyID, ok := strings.CutPrefix(strings.TrimSpace(keyRef), assistantTranscriptKeyRefPrefix)
	if !ok || keyID == "" {
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key %q is not a Signet system key; data sealed under it is readable only after operator re-encryption", keyRef)
	}
	key, err := k.key(ctx, keyID)
	if err != nil {
		return AssistantTranscriptKey{}, err
	}
	if key.Version != strings.TrimSpace(keyVersion) {
		clear(key.Key)
		return AssistantTranscriptKey{}, fmt.Errorf("assistant transcript key %q version %q is not available (configured version %s)", keyRef, keyVersion, key.Version)
	}
	return key, nil
}

func (k KeyringTranscriptKeys) key(ctx context.Context, keyID string) (AssistantTranscriptKey, error) {
	if k.Ring == nil {
		return AssistantTranscriptKey{}, fmt.Errorf("%w: no assistant transcript system key", secretstore.ErrNotConfigured)
	}
	var out AssistantTranscriptKey
	err := k.Ring.Use(ctx, secretstore.PurposeAssistantTranscript, keyID, func(ref secretstore.SystemKeyRef, raw []byte) error {
		out = AssistantTranscriptKey{Ref: ref.Label(), Version: strconv.Itoa(ref.Credential.Version), Key: append([]byte(nil), raw...)}
		return nil
	})
	return out, err
}
