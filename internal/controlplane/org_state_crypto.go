package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// OrgStateCryptoSchema is the outer JSON schema of an encrypted org state record.
const OrgStateCryptoSchema = "bahia.org-state.aead.v1"

// OrgStateCryptoAlgorithm is the AEAD algorithm used for org state encryption.
const OrgStateCryptoAlgorithm = "xchacha20-poly1305"

// OrgStateCryptoEnvelope is the envelope type.
const OrgStateCryptoEnvelope = "service-held-aead"

// OrgStateKey is the service-held symmetric key for org state encryption.
// Follows the AssistantTranscriptKey pattern from assistant_transcript_store.go.
type OrgStateKey struct {
	Ref     string
	Version string
	Key     []byte // Must be chacha20poly1305.KeySize (32) bytes.
}

// OrgStateKeyProvider resolves the service-held symmetric key for org state
// encryption and decryption. In production, the key is loaded from daemon
// config or a secret manager at startup.
type OrgStateKeyProvider interface {
	ActiveOrgStateKey(ctx context.Context) (OrgStateKey, error)
	OrgStateKey(ctx context.Context, keyRef, keyVersion string) (OrgStateKey, error)
}

// StaticOrgStateKeyProvider serves one static key. Production-safe when the
// key is loaded from config/secrets before constructing the provider.
type StaticOrgStateKeyProvider struct {
	Key OrgStateKey
}

func (p StaticOrgStateKeyProvider) ActiveOrgStateKey(context.Context) (OrgStateKey, error) {
	return validateOrgStateKey(p.Key)
}

func (p StaticOrgStateKeyProvider) OrgStateKey(_ context.Context, keyRef, keyVersion string) (OrgStateKey, error) {
	key, err := validateOrgStateKey(p.Key)
	if err != nil {
		return OrgStateKey{}, err
	}
	if key.Ref != keyRef {
		return OrgStateKey{}, fmt.Errorf("org state key %q is not available", keyRef)
	}
	if keyVersion != "" && key.Version != keyVersion {
		return OrgStateKey{}, fmt.Errorf("org state key %q version %q is not available", keyRef, keyVersion)
	}
	return key, nil
}

func validateOrgStateKey(key OrgStateKey) (OrgStateKey, error) {
	if len(key.Key) != chacha20poly1305.KeySize {
		return OrgStateKey{}, fmt.Errorf("org state key must be %d bytes, got %d", chacha20poly1305.KeySize, len(key.Key))
	}
	if key.Ref == "" {
		return OrgStateKey{}, fmt.Errorf("org state key ref is required")
	}
	return key, nil
}

// OrgStateAEADEnvelope is the JSON content shape for encrypted org state
// records. Follows the AssistantTranscriptAEADEnvelope pattern.
type OrgStateAEADEnvelope struct {
	Schema         string            `json:"schema"`
	Envelope       string            `json:"envelope"`
	Algorithm      string            `json:"algorithm"`
	KeyRef         string            `json:"key_ref"`
	KeyVersion     string            `json:"key_version,omitempty"`
	Nonce          string            `json:"nonce"`
	Ciphertext     string            `json:"ciphertext"`
	AssociatedData map[string]string `json:"associated_data,omitempty"`
}

// encryptOrgState encrypts the plaintext content with the service-held key,
// binding the d-tag and topic as associated data for authentication.
func encryptOrgState(ctx context.Context, key OrgStateKey, plaintext []byte, dTag, topic string) (string, error) {
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return "", fmt.Errorf("create org state AEAD: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate org state nonce: %w", err)
	}
	ad := map[string]string{"d": dTag, "t": topic}
	adBytes, err := json.Marshal(ad)
	if err != nil {
		return "", fmt.Errorf("marshal org state associated data: %w", err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, adBytes)
	envelope := OrgStateAEADEnvelope{
		Schema:         OrgStateCryptoSchema,
		Envelope:       OrgStateCryptoEnvelope,
		Algorithm:      OrgStateCryptoAlgorithm,
		KeyRef:         key.Ref,
		KeyVersion:     key.Version,
		Nonce:          base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawStdEncoding.EncodeToString(ciphertext),
		AssociatedData: ad,
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal org state envelope: %w", err)
	}
	return string(envelopeJSON), nil
}

// decryptOrgState decrypts an OrgStateAEADEnvelope using the matching key.
func decryptOrgState(key OrgStateKey, content string) ([]byte, error) {
	var envelope OrgStateAEADEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal org state envelope: %w", err)
	}
	if envelope.Schema != OrgStateCryptoSchema {
		return nil, fmt.Errorf("unknown org state schema: %s", envelope.Schema)
	}
	if envelope.Algorithm != OrgStateCryptoAlgorithm {
		return nil, fmt.Errorf("unknown org state algorithm: %s", envelope.Algorithm)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode org state nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode org state ciphertext: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key.Key)
	if err != nil {
		return nil, fmt.Errorf("create org state AEAD: %w", err)
	}
	adBytes, err := json.Marshal(envelope.AssociatedData)
	if err != nil {
		return nil, fmt.Errorf("marshal org state associated data: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, adBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt org state: %w", err)
	}
	return plaintext, nil
}

// OrgStateEncryptorImpl adapts the OrgStateKeyProvider for legacy O1
// decryption during migration. It satisfies the LegacyOrgStateDecryptor
// interface (DecryptOrgState only). The EncryptOrgState method is retained
// for migration tests but is not part of any production interface.
//
// DEPRECATED(bahia-irsry.64): This type and the DecryptOrgState method are
// retained for one release so that the LegacyOCKMigrator can re-publish
// O1-era records under the OCK scheme at startup. Once all deployments
// have run the warm-start migration, remove OrgStateEncryptorImpl,
// OrgStateKeyProvider, StaticOrgStateKeyProvider, OrgStateCryptoSchema,
// the encryptOrgState/decryptOrgState functions, and the
// LegacyOrgStateDecryptor interface in confidential_state.go. Condition
// for deletion: no legacy-format (O1 sha256-derived AEAD) records remain
// in any deployment's local event store or relay history.
// Follow-up issue: bahia-irsry.65 (file after soak).
type OrgStateEncryptorImpl struct {
	keyProvider OrgStateKeyProvider
}

// NewOrgStateEncryptor creates an encryptor backed by the given key provider.
func NewOrgStateEncryptor(keyProvider OrgStateKeyProvider) *OrgStateEncryptorImpl {
	return &OrgStateEncryptorImpl{keyProvider: keyProvider}
}

// DecryptOrgState decrypts an encrypted org state envelope.
func (e *OrgStateEncryptorImpl) DecryptOrgState(content string) ([]byte, error) {
	// Parse envelope to get key ref/version.
	var envelope OrgStateAEADEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal org state envelope for key lookup: %w", err)
	}
	key, err := e.keyProvider.OrgStateKey(context.Background(), envelope.KeyRef, envelope.KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("resolve org state key %q: %w", envelope.KeyRef, err)
	}
	return decryptOrgState(key, content)
}
