package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/openagentsinc/bahia/internal/adapters/signet"
	"github.com/openagentsinc/bahia/internal/service"
)

const assistantWrappedKeySchema = "bahia.assistant.wrapped-keys.v1"

// assistantKeyWrapper keeps the crypto transport replaceable only inside this
// package's tests. Exported entry points require the fenced Signet signer.
type assistantKeyWrapper interface {
	GetPublicKey(context.Context) (nostr.PubKey, error)
	Encrypt(context.Context, string, nostr.PubKey) (string, error)
	Decrypt(context.Context, string, nostr.PubKey) (string, error)
}

// AssistantWrappedKeyManifest contains no plaintext key material. The active
// random key is for new events; the legacy key preserves reads of immutable
// relay events encrypted under the former private-key-derived key.
type AssistantWrappedKeyManifest struct {
	Schema        string                     `json:"schema"`
	ServicePubkey string                     `json:"service_pubkey"`
	Active        AssistantWrappedKeyRecord  `json:"active"`
	Legacy        *AssistantWrappedKeyRecord `json:"legacy,omitempty"`
}

type AssistantWrappedKeyRecord struct {
	Ref        string `json:"ref"`
	Version    string `json:"version"`
	Rotation   string `json:"rotation"`
	Ciphertext string `json:"ciphertext"`
}

type assistantWrappedKeyPlaintext struct {
	Schema        string `json:"schema"`
	ServicePubkey string `json:"service_pubkey"`
	Ref           string `json:"ref"`
	Version       string `json:"version"`
	Rotation      string `json:"rotation"`
	Key           string `json:"key"`
}

// CreateAssistantWrappedKeyManifest wraps a fresh random active key and an
// optional legacy key. The returned manifest must be durably stored before a
// caller selects its active key; this function does not change daemon wiring.
func CreateAssistantWrappedKeyManifest(ctx context.Context, wrapper *signet.EpochSigner, servicePubkey nostr.PubKey, legacy *service.AssistantTranscriptKey) (AssistantWrappedKeyManifest, error) {
	return createAssistantWrappedKeyManifest(ctx, wrapper, servicePubkey, legacy)
}

func createAssistantWrappedKeyManifest(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, legacy *service.AssistantTranscriptKey) (AssistantWrappedKeyManifest, error) {
	if err := checkAssistantWrapper(ctx, wrapper, servicePubkey); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	key := service.AssistantTranscriptKey{Ref: "assistant-transcript/service-data-key", Version: "v2", Rotation: "signet-wrapped-random", Key: make([]byte, chacha20poly1305.KeySize)}
	if _, err := rand.Read(key.Key); err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("generate assistant data key: %w", err)
	}
	active, err := wrapAssistantKey(ctx, wrapper, servicePubkey, key)
	if err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	manifest := AssistantWrappedKeyManifest{Schema: assistantWrappedKeySchema, ServicePubkey: servicePubkey.Hex(), Active: active}
	if legacy != nil {
		old, err := validateWrappedAssistantKey(*legacy)
		if err != nil {
			return AssistantWrappedKeyManifest{}, fmt.Errorf("validate legacy assistant key: %w", err)
		}
		if old.Ref != "assistant-transcript/service-nostr-key" || old.Version != "v1" || old.Rotation != "service-nostr-key" {
			return AssistantWrappedKeyManifest{}, errors.New("legacy assistant key identity differs from the deployed v1 key")
		}
		record, err := wrapAssistantKey(ctx, wrapper, servicePubkey, old)
		if err != nil {
			return AssistantWrappedKeyManifest{}, err
		}
		manifest.Legacy = &record
	}
	return manifest, nil
}

func wrapAssistantKey(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, key service.AssistantTranscriptKey) (AssistantWrappedKeyRecord, error) {
	plain := assistantWrappedKeyPlaintext{Schema: assistantWrappedKeySchema, ServicePubkey: servicePubkey.Hex(), Ref: key.Ref, Version: key.Version, Rotation: key.Rotation, Key: base64.RawStdEncoding.EncodeToString(key.Key)}
	encoded, err := json.Marshal(plain)
	if err != nil {
		return AssistantWrappedKeyRecord{}, err
	}
	ciphertext, err := wrapper.Encrypt(ctx, string(encoded), servicePubkey)
	if err != nil {
		return AssistantWrappedKeyRecord{}, fmt.Errorf("wrap assistant key %s/%s: %w", key.Ref, key.Version, err)
	}
	if ciphertext == "" {
		return AssistantWrappedKeyRecord{}, errors.New("wrapper returned empty assistant key ciphertext")
	}
	return AssistantWrappedKeyRecord{Ref: key.Ref, Version: key.Version, Rotation: key.Rotation, Ciphertext: ciphertext}, nil
}

// OpenAssistantWrappedKeyManifest resolves only the keys listed in a pinned
// manifest. It never derives a key from the service nsec or falls back to raw
// local signing. Both transcript and checkpoint stores can use this provider.
func OpenAssistantWrappedKeyManifest(ctx context.Context, wrapper *signet.EpochSigner, servicePubkey nostr.PubKey, manifest AssistantWrappedKeyManifest) (service.AssistantTranscriptKeyProvider, error) {
	return openAssistantWrappedKeyManifest(ctx, wrapper, servicePubkey, manifest)
}

func openAssistantWrappedKeyManifest(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, manifest AssistantWrappedKeyManifest) (service.AssistantTranscriptKeyProvider, error) {
	if err := checkAssistantWrapper(ctx, wrapper, servicePubkey); err != nil {
		return nil, err
	}
	if manifest.Schema != assistantWrappedKeySchema || manifest.ServicePubkey != servicePubkey.Hex() {
		return nil, errors.New("assistant key manifest schema or service pubkey mismatch")
	}
	active, err := unwrapAssistantKey(ctx, wrapper, servicePubkey, manifest.Active)
	if err != nil {
		return nil, fmt.Errorf("open active assistant key: %w", err)
	}
	provider := &wrappedAssistantTranscriptKeyProvider{active: active, keys: map[string]service.AssistantTranscriptKey{assistantKeyID(active.Ref, active.Version): active}}
	if manifest.Legacy != nil {
		old, err := unwrapAssistantKey(ctx, wrapper, servicePubkey, *manifest.Legacy)
		if err != nil {
			return nil, fmt.Errorf("open legacy assistant key: %w", err)
		}
		id := assistantKeyID(old.Ref, old.Version)
		if _, exists := provider.keys[id]; exists {
			return nil, errors.New("assistant key manifest has duplicate key identity")
		}
		provider.keys[id] = old
	}
	return provider, nil
}

func unwrapAssistantKey(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, record AssistantWrappedKeyRecord) (service.AssistantTranscriptKey, error) {
	if strings.TrimSpace(record.Ref) == "" || strings.TrimSpace(record.Version) == "" || record.Ciphertext == "" || len(record.Ciphertext) > 1<<20 {
		return service.AssistantTranscriptKey{}, errors.New("invalid assistant key manifest record")
	}
	decoded, err := wrapper.Decrypt(ctx, record.Ciphertext, servicePubkey)
	if err != nil {
		return service.AssistantTranscriptKey{}, fmt.Errorf("unwrap assistant key: %w", err)
	}
	if len(decoded) > 4096 {
		return service.AssistantTranscriptKey{}, errors.New("oversized assistant key plaintext")
	}
	var plain assistantWrappedKeyPlaintext
	if err := json.Unmarshal([]byte(decoded), &plain); err != nil {
		return service.AssistantTranscriptKey{}, fmt.Errorf("decode assistant key plaintext: %w", err)
	}
	if plain.Schema != assistantWrappedKeySchema || plain.ServicePubkey != servicePubkey.Hex() || plain.Ref != record.Ref || plain.Version != record.Version || plain.Rotation != record.Rotation {
		return service.AssistantTranscriptKey{}, errors.New("assistant key wrap binding mismatch")
	}
	bytes, err := base64.RawStdEncoding.Strict().DecodeString(plain.Key)
	if err != nil {
		return service.AssistantTranscriptKey{}, fmt.Errorf("decode assistant key: %w", err)
	}
	return validateWrappedAssistantKey(service.AssistantTranscriptKey{Ref: plain.Ref, Version: plain.Version, Rotation: plain.Rotation, Key: bytes})
}

func checkAssistantWrapper(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey) error {
	if wrapper == nil || servicePubkey == nostr.ZeroPK {
		return errors.New("assistant key wrapper and service pubkey are required")
	}
	actual, err := wrapper.GetPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("read assistant key wrapper pubkey: %w", err)
	}
	if actual != servicePubkey {
		return errors.New("assistant key wrapper pubkey differs from service pubkey")
	}
	return nil
}

type wrappedAssistantTranscriptKeyProvider struct {
	active service.AssistantTranscriptKey
	keys   map[string]service.AssistantTranscriptKey
}

func (p *wrappedAssistantTranscriptKeyProvider) ActiveTranscriptKey(context.Context) (service.AssistantTranscriptKey, error) {
	return validateWrappedAssistantKey(p.active)
}

func (p *wrappedAssistantTranscriptKeyProvider) TranscriptKey(_ context.Context, ref, version string) (service.AssistantTranscriptKey, error) {
	key, exists := p.keys[assistantKeyID(ref, version)]
	if !exists {
		return service.AssistantTranscriptKey{}, errors.New("assistant transcript key identity is not in wrapped manifest")
	}
	return validateWrappedAssistantKey(key)
}

func assistantKeyID(ref, version string) string { return ref + "\x00" + version }

func validateWrappedAssistantKey(key service.AssistantTranscriptKey) (service.AssistantTranscriptKey, error) {
	key.Ref = strings.TrimSpace(key.Ref)
	key.Version = strings.TrimSpace(key.Version)
	key.Rotation = strings.TrimSpace(key.Rotation)
	if key.Ref == "" || key.Version == "" || len(key.Key) != chacha20poly1305.KeySize {
		return service.AssistantTranscriptKey{}, errors.New("invalid assistant key identity or length")
	}
	key.Key = append([]byte(nil), key.Key...)
	return key, nil
}
