package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// EncryptionMethod identifies how a secret value is encrypted at rest.
type EncryptionMethod string

const (
	EncryptionNIP44    EncryptionMethod = "nip44"
	EncryptionAES256   EncryptionMethod = "aes256gcm"
	EncryptionAES256V2 EncryptionMethod = "aes256gcm-v2"
	// EncryptionStoreRef marks a PostgreSQL index row (and its retained
	// versions) of a secret whose value lives in the external secret store:
	// the row holds no value.
	EncryptionStoreRef EncryptionMethod = "store-ref"
)

// ErrLegacySecretValue is returned for a secret whose value Bahia sealed
// itself (into PostgreSQL, under a key derived from the retired service key,
// or NIP-44'd to it in an intent). Such values are compromised and Bahia
// never decrypts them: every secret value lives in the secret store
// (docs/architecture/secret-store.md).
var ErrLegacySecretValue = errors.New("secret holds a legacy value sealed with the retired service key, which Bahia no longer reads: rotate the value at its source, import it into the secret store labelled <org-id>/<service-id>/<name>, then re-point the secret with `bahia secrets set <service> <name> --credential-id <id> --credential-version <n>`")

// LegacySecretValueError names the legacy secret a consumer refused.
func LegacySecretValueError(id uuid.UUID, name string) error {
	if name == "" {
		return fmt.Errorf("secret %s: %w", id, ErrLegacySecretValue)
	}
	return fmt.Errorf("secret %s (%s): %w", id, name, ErrLegacySecretValue)
}

// ServiceSecret represents an encrypted secret bound to a service (and optionally an environment).
type ServiceSecret struct {
	ID               uuid.UUID        `json:"id"`
	ServiceID        uuid.UUID        `json:"service_id"`
	EnvironmentID    *uuid.UUID       `json:"environment_id,omitempty"` // nil = applies to all environments
	Name             string           `json:"name"`
	EncryptedValue   []byte           `json:"-"` // never serialized to API responses
	EncryptionMethod EncryptionMethod `json:"encryption_method"`
	Version          int              `json:"version"`
	CreatedBy        string           `json:"created_by"` // pubkey or user ID of the creator
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// SecretCredentialRef names a secret value held by the external secret store
// (docs/architecture/secret-store.md): the store backend, the store's
// credential id and the credential version the secret is pinned to. It never
// carries the value.
type SecretCredentialRef struct {
	Store        string `json:"store"`
	CredentialID string `json:"credential_id"`
	Version      int    `json:"version"`
}

// SecretRef is a safe reference to a secret, without the encrypted value.
// It is the content of the canonical secret record. CredentialRef is set when
// the value lives in the external secret store; EncryptionMethod is set only
// for secrets whose value Bahia sealed itself.
type SecretRef struct {
	ID               uuid.UUID            `json:"id"`
	ServiceID        uuid.UUID            `json:"service_id"`
	EnvironmentID    *uuid.UUID           `json:"environment_id,omitempty"`
	Name             string               `json:"name"`
	EncryptionMethod EncryptionMethod     `json:"encryption_method,omitempty"`
	CredentialRef    *SecretCredentialRef `json:"credential_ref,omitempty"`
	Version          int                  `json:"version"`
	CreatedBy        string               `json:"created_by"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

// ToRef converts a ServiceSecret to a SecretRef (strips the encrypted value).
func (s *ServiceSecret) ToRef() SecretRef {
	return SecretRef{
		ID:               s.ID,
		ServiceID:        s.ServiceID,
		EnvironmentID:    s.EnvironmentID,
		Name:             s.Name,
		EncryptionMethod: s.EncryptionMethod,
		Version:          s.Version,
		CreatedBy:        s.CreatedBy,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

// ErrSecretInStore is returned by PostgreSQL value readers for store-backed
// secrets.
var ErrSecretInStore = errors.New("secret value is held by the secret store and this consumer still reads PostgreSQL (moves to the secret store in bahia-cd0wr.4.7)")

// SecretInStoreError names the store-backed secret a PostgreSQL consumer
// could not read.
func SecretInStoreError(id uuid.UUID, name string) error {
	return fmt.Errorf("secret %s (%s): %w", id, name, ErrSecretInStore)
}
