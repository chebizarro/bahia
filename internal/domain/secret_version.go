package domain

import (
	"time"

	"github.com/google/uuid"
)

// SecretVersion records one encrypted version of a service secret. EncryptedValue
// is intentionally omitted from JSON so read models and status/result payloads
// can expose version metadata without leaking ciphertext or plaintext.
type SecretVersion struct {
	ID               uuid.UUID        `json:"id"`
	SecretID         uuid.UUID        `json:"secret_id"`
	Version          int              `json:"version"`
	EncryptedValue   []byte           `json:"-"`
	EncryptionMethod EncryptionMethod `json:"encryption_method"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
}

// SecretAccessOutcome describes whether a secret access attempt completed.
type SecretAccessOutcome string

const (
	SecretAccessOutcomeSuccess SecretAccessOutcome = "success"
	SecretAccessOutcomeFailure SecretAccessOutcome = "failure"
)

// SecretAccessOperation identifies why a secret was accessed.
type SecretAccessOperation string

const (
	SecretAccessOperationResolve      SecretAccessOperation = "resolve"
	SecretAccessOperationRuntimeApply SecretAccessOperation = "runtime_apply"
)

// SecretAccessAudit records one secret access attempt without plaintext.
type SecretAccessAudit struct {
	ID            uuid.UUID             `json:"id"`
	SecretID      uuid.UUID             `json:"secret_id"`
	VersionID     uuid.UUID             `json:"secret_version_id"`
	Version       int                   `json:"version"`
	ServiceID     uuid.UUID             `json:"service_id"`
	EnvironmentID *uuid.UUID            `json:"environment_id,omitempty"`
	Operation     SecretAccessOperation `json:"operation"`
	Outcome       SecretAccessOutcome   `json:"outcome"`
	Actor         string                `json:"actor,omitempty"`
	Reason        string                `json:"reason,omitempty"`
	RequestID     string                `json:"request_id,omitempty"`
	Error         string                `json:"error,omitempty"`
	AccessedAt    time.Time             `json:"accessed_at"`
}

// SecretAccessManifest is safe metadata returned alongside resolved secret
// values. It intentionally contains no ciphertext or plaintext.
type SecretAccessManifest struct {
	SecretID      uuid.UUID             `json:"secret_id"`
	VersionID     uuid.UUID             `json:"secret_version_id"`
	Version       int                   `json:"version"`
	ServiceID     uuid.UUID             `json:"service_id"`
	EnvironmentID *uuid.UUID            `json:"environment_id,omitempty"`
	Name          string                `json:"name"`
	Operation     SecretAccessOperation `json:"operation"`
	Outcome       SecretAccessOutcome   `json:"outcome"`
	AccessedAt    time.Time             `json:"accessed_at"`
}

// SecretResolveOptions captures safe audit context for a resolve attempt.
type SecretResolveOptions struct {
	Operation SecretAccessOperation
	Actor     string
	Reason    string
	RequestID string
}
