package domain

import (
	"time"

	"github.com/google/uuid"
)

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
	ID        uuid.UUID `json:"id"`
	SecretID  uuid.UUID `json:"secret_id"`
	VersionID uuid.UUID `json:"secret_version_id"`
	Version   int       `json:"version"`
	// RecordVersion is the revision of the canonical secret record the value
	// was resolved through; re-pointing a secret increments it.
	RecordVersion int                   `json:"record_version,omitempty"`
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
	SecretID  uuid.UUID `json:"secret_id"`
	VersionID uuid.UUID `json:"secret_version_id"`
	Version   int       `json:"version"`
	// RecordVersion is the revision of the canonical secret record the value
	// was resolved through; re-pointing a secret increments it.
	RecordVersion int                   `json:"record_version,omitempty"`
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
