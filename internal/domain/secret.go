package domain

import (
	"time"

	"github.com/google/uuid"
)

// EncryptionMethod identifies how a secret value is encrypted at rest.
type EncryptionMethod string

const (
	EncryptionNIP44    EncryptionMethod = "nip44"
	EncryptionAES256   EncryptionMethod = "aes256gcm"
	EncryptionAES256V2 EncryptionMethod = "aes256gcm-v2"
)

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
