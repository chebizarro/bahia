package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
)

// Confidential cp-state envelope schema and algorithm constants.
const (
	ConfidentialSchema    = "bahia.confidential.aead.v1"
	ConfidentialAlgorithm = "xchacha20-poly1305"
	OCKWrapSchema         = "bahia.ock-wrap.v1"
)

// OrgContentKey is a per-org symmetric content key used to encrypt
// confidential cp-state records. Members who hold a wrapped copy of this
// key can decrypt org-visible fields. Service-only fields use an additional
// NIP-44 inner layer.
type OrgContentKey struct {
	OrgID   string
	Version int
	Key     [32]byte
}

// KeyRef returns the stable key reference string for this OCK.
func (k OrgContentKey) KeyRef() string {
	return "ock:" + k.OrgID
}

// KeyVersion returns the version string for this OCK.
func (k OrgContentKey) KeyVersion() string {
	return "v" + strconv.Itoa(k.Version)
}

// ConfidentialRecordContext captures the signed event's record identity for
// AEAD associated data binding. Values must come from the verified signed
// event tags (on the decrypt path) or from the publisher-built tags (on the
// encrypt path), never from untrusted envelope self-description.
type ConfidentialRecordContext struct {
	LegacyKind int
	DTag       string
	Topic      string
}

// ConfidentialEnvelope is the JSON content shape for confidential cp-state
// records encrypted under a per-org content key (OCK).
type ConfidentialEnvelope struct {
	Schema         string            `json:"schema"`
	Algorithm      string            `json:"algorithm"`
	KeyOrg         string            `json:"key_org"`
	KeyRef         string            `json:"key_ref"`
	KeyVersion     string            `json:"key_version"`
	Nonce          string            `json:"nonce"`
	Ciphertext     string            `json:"ciphertext"`
	AssociatedData map[string]string `json:"associated_data"`
	ServiceInner   string            `json:"service_inner,omitempty"`
}

// EncryptConfidentialContent encrypts plaintext with the OCK, binding the
// record context as AEAD associated data. If serviceOnlyPlaintext is non-nil,
// it is NIP-44-encrypted to the service pubkey (via the provided encrypt
// function) and embedded as service_inner.
func EncryptConfidentialContent(
	ctx context.Context,
	key OrgContentKey,
	plaintext []byte,
	recordCtx ConfidentialRecordContext,
	serviceOnlyPlaintext []byte,
	serviceEncrypt func(ctx context.Context, plaintext string) (string, error),
) (string, error) {
	aead, err := chacha20poly1305.NewX(key.Key[:])
	if err != nil {
		return "", fmt.Errorf("create confidential AEAD: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate confidential nonce: %w", err)
	}
	ad := confidentialAssociatedData(key, recordCtx)
	adBytes, err := json.Marshal(ad)
	if err != nil {
		return "", fmt.Errorf("marshal confidential associated data: %w", err)
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, adBytes)

	envelope := ConfidentialEnvelope{
		Schema:         ConfidentialSchema,
		Algorithm:      ConfidentialAlgorithm,
		KeyOrg:         key.OrgID,
		KeyRef:         key.KeyRef(),
		KeyVersion:     key.KeyVersion(),
		Nonce:          base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawStdEncoding.EncodeToString(ciphertext),
		AssociatedData: ad,
	}

	if len(serviceOnlyPlaintext) > 0 && serviceEncrypt != nil {
		inner, err := serviceEncrypt(ctx, string(serviceOnlyPlaintext))
		if err != nil {
			return "", fmt.Errorf("encrypt service-inner: %w", err)
		}
		envelope.ServiceInner = inner
	}

	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal confidential envelope: %w", err)
	}
	return string(envelopeJSON), nil
}

// DecryptConfidentialContent decrypts a confidential envelope's org-visible
// content using the provided OCK. The record context is verified against the
// envelope's associated data.
func DecryptConfidentialContent(key OrgContentKey, content string, recordCtx ConfidentialRecordContext) ([]byte, error) {
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal confidential envelope: %w", err)
	}
	if envelope.Schema != ConfidentialSchema {
		return nil, fmt.Errorf("unknown confidential schema: %s", envelope.Schema)
	}
	if envelope.Algorithm != ConfidentialAlgorithm {
		return nil, fmt.Errorf("unknown confidential algorithm: %s", envelope.Algorithm)
	}

	// Verify associated data matches the signed event context.
	expectedAD := confidentialAssociatedData(key, recordCtx)
	if err := verifyConfidentialAD(envelope.AssociatedData, expectedAD); err != nil {
		return nil, fmt.Errorf("confidential AD mismatch: %w", err)
	}

	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("decode confidential nonce: %w", err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode confidential ciphertext: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key.Key[:])
	if err != nil {
		return nil, fmt.Errorf("create confidential AEAD: %w", err)
	}
	adBytes, err := json.Marshal(envelope.AssociatedData)
	if err != nil {
		return nil, fmt.Errorf("marshal confidential associated data: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, adBytes)
	if err != nil {
		return nil, fmt.Errorf("decrypt confidential content: %w", err)
	}
	return plaintext, nil
}

// DecryptConfidentialServiceInner decrypts the service-only inner layer of a
// confidential envelope using the provided NIP-44 decrypt function.
func DecryptConfidentialServiceInner(
	content string,
	serviceDecrypt func(ciphertext string) (string, error),
) ([]byte, error) {
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal confidential envelope: %w", err)
	}
	if envelope.ServiceInner == "" {
		return nil, nil
	}
	plaintext, err := serviceDecrypt(envelope.ServiceInner)
	if err != nil {
		return nil, fmt.Errorf("decrypt service-inner: %w", err)
	}
	return []byte(plaintext), nil
}

// ParseConfidentialEnvelope parses a confidential envelope to extract key
// metadata without decrypting. Used for key lookups during decryption.
func ParseConfidentialEnvelope(content string) (*ConfidentialEnvelope, error) {
	var envelope ConfidentialEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, fmt.Errorf("parse confidential envelope: %w", err)
	}
	if envelope.Schema != ConfidentialSchema {
		return nil, fmt.Errorf("unknown schema: %s", envelope.Schema)
	}
	return &envelope, nil
}

// confidentialAssociatedData builds the AEAD associated data map that binds
// the ciphertext to the record's coordinate identity.
func confidentialAssociatedData(key OrgContentKey, ctx ConfidentialRecordContext) map[string]string {
	return map[string]string{
		"schema":      ConfidentialSchema,
		"key_org":     key.OrgID,
		"key_ref":     key.KeyRef(),
		"key_version": key.KeyVersion(),
		"legacy_kind": strconv.Itoa(ctx.LegacyKind),
		"d":           ctx.DTag,
		"t":           ctx.Topic,
	}
}

func verifyConfidentialAD(got, want map[string]string) error {
	for k, wantV := range want {
		if gotV, ok := got[k]; !ok || gotV != wantV {
			return fmt.Errorf("key %q: got %q, want %q", k, gotV, wantV)
		}
	}
	return nil
}

// GenerateOrgContentKey creates a new random OCK for the given org.
func GenerateOrgContentKey(orgID string, version int) (OrgContentKey, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return OrgContentKey{}, fmt.Errorf("generate OCK: %w", err)
	}
	return OrgContentKey{
		OrgID:   orgID,
		Version: version,
		Key:     key,
	}, nil
}

// OCKWrapPayload is the plaintext JSON inside a key-envelope record.
// The recipient decrypts the NIP-44 ciphertext and validates this payload.
type OCKWrapPayload struct {
	Schema          string `json:"schema"`
	OrgID           string `json:"org_id"`
	KeyRef          string `json:"key_ref"`
	Version         int    `json:"version"`
	Key             string `json:"key"` // base64 encoded 32-byte key
	RecipientPubkey string `json:"recipient_pubkey"`
}

// MarshalOCKWrap serializes an OCK wrap payload for NIP-44 encryption.
func MarshalOCKWrap(key OrgContentKey, recipientPubkey string) ([]byte, error) {
	payload := OCKWrapPayload{
		Schema:          OCKWrapSchema,
		OrgID:           key.OrgID,
		KeyRef:          key.KeyRef(),
		Version:         key.Version,
		Key:             base64.RawStdEncoding.EncodeToString(key.Key[:]),
		RecipientPubkey: recipientPubkey,
	}
	return json.Marshal(payload)
}

// UnmarshalOCKWrap deserializes and validates an OCK wrap payload.
func UnmarshalOCKWrap(data []byte) (OrgContentKey, string, error) {
	var payload OCKWrapPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return OrgContentKey{}, "", fmt.Errorf("unmarshal OCK wrap: %w", err)
	}
	if payload.Schema != OCKWrapSchema {
		return OrgContentKey{}, "", fmt.Errorf("unknown OCK wrap schema: %s", payload.Schema)
	}
	if payload.OrgID == "" {
		return OrgContentKey{}, "", fmt.Errorf("OCK wrap missing org_id")
	}
	keyBytes, err := base64.RawStdEncoding.DecodeString(payload.Key)
	if err != nil {
		return OrgContentKey{}, "", fmt.Errorf("decode OCK key: %w", err)
	}
	if len(keyBytes) != 32 {
		return OrgContentKey{}, "", fmt.Errorf("OCK key must be 32 bytes, got %d", len(keyBytes))
	}
	var key [32]byte
	copy(key[:], keyBytes)
	return OrgContentKey{
		OrgID:   payload.OrgID,
		Version: payload.Version,
		Key:     key,
	}, payload.RecipientPubkey, nil
}
