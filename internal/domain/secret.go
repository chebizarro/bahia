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

// SecretRef is a safe reference to a secret, without the encrypted value.
// Used in API responses and logging.
type SecretRef struct {
	ID               uuid.UUID        `json:"id"`
	ServiceID        uuid.UUID        `json:"service_id"`
	EnvironmentID    *uuid.UUID       `json:"environment_id,omitempty"`
	Name             string           `json:"name"`
	EncryptionMethod EncryptionMethod `json:"encryption_method"`
	Version          int              `json:"version"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
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
