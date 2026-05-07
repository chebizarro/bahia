package domain

import (
	"time"

	"github.com/google/uuid"
)

// PackageBackendType identifies a configured package repository backend connector.
type PackageBackendType string

const (
	PackageBackendNexus          PackageBackendType = "nexus"
	PackageBackendPulp           PackageBackendType = "pulp"
	PackageBackendFilesystemMock PackageBackendType = "filesystem_mock"
)

// IsValid reports whether the backend type is one Bahia can register.
func (t PackageBackendType) IsValid() bool {
	switch t {
	case PackageBackendNexus, PackageBackendPulp, PackageBackendFilesystemMock:
		return true
	default:
		return false
	}
}

// PackageRepositoryFormat is the ecosystem format served by a package repository.
//
// These values intentionally preserve the user-requested package formats as
// first-class domain values in the foundation phase, even though ecosystem index
// generation and format-specific backend behavior will be implemented later.
type PackageRepositoryFormat string

const (
	PackageRepositoryFormatNPM       PackageRepositoryFormat = "npm"
	PackageRepositoryFormatPyPI      PackageRepositoryFormat = "pypi"
	PackageRepositoryFormatConan     PackageRepositoryFormat = "conan"
	PackageRepositoryFormatDeb       PackageRepositoryFormat = "deb"
	PackageRepositoryFormatRPM       PackageRepositoryFormat = "rpm"
	PackageRepositoryFormatPub       PackageRepositoryFormat = "pub"
	PackageRepositoryFormatGoModules PackageRepositoryFormat = "go_modules"
	PackageRepositoryFormatGradle    PackageRepositoryFormat = "gradle"
)

// IsValid reports whether the package format is a first-class supported value.
func (f PackageRepositoryFormat) IsValid() bool {
	switch f {
	case PackageRepositoryFormatNPM,
		PackageRepositoryFormatPyPI,
		PackageRepositoryFormatConan,
		PackageRepositoryFormatDeb,
		PackageRepositoryFormatRPM,
		PackageRepositoryFormatPub,
		PackageRepositoryFormatGoModules,
		PackageRepositoryFormatGradle:
		return true
	default:
		return false
	}
}

// PackageRepositoryStatus is projection state for a repository derived from Nostr events/backend observations.
type PackageRepositoryStatus string

const (
	PackageRepositoryStatusPending  PackageRepositoryStatus = "pending"
	PackageRepositoryStatusReady    PackageRepositoryStatus = "ready"
	PackageRepositoryStatusDisabled PackageRepositoryStatus = "disabled"
	PackageRepositoryStatusDeleting PackageRepositoryStatus = "deleting"
	PackageRepositoryStatusDeleted  PackageRepositoryStatus = "deleted"
	PackageRepositoryStatusFailed   PackageRepositoryStatus = "failed"
)

// PackageArtifactStatus is projection state for an artifact derived from Nostr events/backend observations.
type PackageArtifactStatus string

const (
	PackageArtifactStatusPending   PackageArtifactStatus = "pending"
	PackageArtifactStatusAvailable PackageArtifactStatus = "available"
	PackageArtifactStatusDeleting  PackageArtifactStatus = "deleting"
	PackageArtifactStatusDeleted   PackageArtifactStatus = "deleted"
	PackageArtifactStatusFailed    PackageArtifactStatus = "failed"
)

// PackagePublicationStatus captures publication/promotion read-model state.
type PackagePublicationStatus string

const (
	PackagePublicationStatusPending    PackagePublicationStatus = "pending"
	PackagePublicationStatusPublished  PackagePublicationStatus = "published"
	PackagePublicationStatusPromoting  PackagePublicationStatus = "promoting"
	PackagePublicationStatusPromoted   PackagePublicationStatus = "promoted"
	PackagePublicationStatusRejected   PackagePublicationStatus = "rejected"
	PackagePublicationStatusRolledBack PackagePublicationStatus = "rolled_back"
	PackagePublicationStatusFailed     PackagePublicationStatus = "failed"
)

// PackagePolicyDecision records policy evaluation results for package publication/promotion.
type PackagePolicyDecision string

const (
	PackagePolicyDecisionUnknown          PackagePolicyDecision = "unknown"
	PackagePolicyDecisionAllowed          PackagePolicyDecision = "allowed"
	PackagePolicyDecisionDenied           PackagePolicyDecision = "denied"
	PackagePolicyDecisionRequiresApproval PackagePolicyDecision = "requires_approval"
)

// PackageOperation identifies durable package control-plane intent operations.
type PackageOperation string

const (
	PackageOperationRepositoryApply  PackageOperation = "repository_apply"
	PackageOperationRepositoryDelete PackageOperation = "repository_delete"
	PackageOperationArtifactPublish  PackageOperation = "artifact_publish"
	PackageOperationArtifactDelete   PackageOperation = "artifact_delete"
	PackageOperationPromote          PackageOperation = "promote"
)

// PackageIntentStatus is durable request-event processing state used for idempotent replay/recovery.
type PackageIntentStatus string

const (
	PackageIntentStatusAccepted   PackageIntentStatus = "accepted"
	PackageIntentStatusExecuting  PackageIntentStatus = "executing"
	PackageIntentStatusSucceeded  PackageIntentStatus = "succeeded"
	PackageIntentStatusFailed     PackageIntentStatus = "failed"
	PackageIntentStatusSuperseded PackageIntentStatus = "superseded"
)

// Terminal reports whether the intent should not be re-executed during recovery.
func (s PackageIntentStatus) Terminal() bool {
	switch s {
	case PackageIntentStatusSucceeded, PackageIntentStatusFailed, PackageIntentStatusSuperseded:
		return true
	default:
		return false
	}
}

// PackageRepositoryPolicy contains policy knobs enforced before backend mutation.
type PackageRepositoryPolicy struct {
	RequireSHA256              bool                  `json:"require_sha256"`
	AllowOverwrite             bool                  `json:"allow_overwrite"`
	MaxFileSizeBytes           int64                 `json:"max_file_size_bytes,omitempty"`
	AllowedMediaTypes          []string              `json:"allowed_media_types,omitempty"`
	AllowedPackageNamePrefixes []string              `json:"allowed_package_name_prefixes,omitempty"`
	PublishRequiresApproval    bool                  `json:"publish_requires_approval"`
	PromotionRequiresApproval  bool                  `json:"promotion_requires_approval"`
	RequiredAttestations       []string              `json:"required_attestations,omitempty"`
	DefaultPolicyDecision      PackagePolicyDecision `json:"default_policy_decision,omitempty"`
	Metadata                   map[string]any        `json:"metadata,omitempty"`
}

// PackageRepository is a Nostr-derived projection of desired/observed repository state.
type PackageRepository struct {
	ID                     uuid.UUID               `json:"id"`
	Name                   string                  `json:"name"`
	Format                 PackageRepositoryFormat `json:"format"`
	BackendRef             string                  `json:"backend_ref"`
	BackendType            PackageBackendType      `json:"backend_type"`
	ExternalRepositoryName string                  `json:"external_repository_name"`
	Description            string                  `json:"description,omitempty"`
	NamespacePrefix        string                  `json:"namespace_prefix,omitempty"`
	Policy                 PackageRepositoryPolicy `json:"policy"`
	Metadata               map[string]any          `json:"metadata,omitempty"`
	PublicURL              string                  `json:"public_url,omitempty"`
	Status                 PackageRepositoryStatus `json:"status"`
	LastError              string                  `json:"last_error,omitempty"`
	Deleted                bool                    `json:"deleted"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
	LastEventID            string                  `json:"last_event_id,omitempty"`
	LastEventCreatedAt     time.Time               `json:"last_event_created_at"`
}

// PackageArtifact is a Nostr-derived projection of package artifact state.
type PackageArtifact struct {
	ID                 uuid.UUID               `json:"id"`
	RepositoryID       uuid.UUID               `json:"repository_id"`
	RepositoryName     string                  `json:"repository_name"`
	Format             PackageRepositoryFormat `json:"format"`
	Namespace          string                  `json:"namespace,omitempty"`
	PackageName        string                  `json:"package_name"`
	Version            string                  `json:"version"`
	Filename           string                  `json:"filename"`
	SourceURL          string                  `json:"source_url,omitempty"`
	SHA256             string                  `json:"sha256"`
	SizeBytes          int64                   `json:"size_bytes"`
	ContentType        string                  `json:"content_type,omitempty"`
	Metadata           map[string]any          `json:"metadata,omitempty"`
	DownloadURL        string                  `json:"download_url,omitempty"`
	BackendPath        string                  `json:"backend_path,omitempty"`
	Status             PackageArtifactStatus   `json:"status"`
	LastError          string                  `json:"last_error,omitempty"`
	Deleted            bool                    `json:"deleted"`
	CreatedAt          time.Time               `json:"created_at"`
	UpdatedAt          time.Time               `json:"updated_at"`
	LastEventID        string                  `json:"last_event_id,omitempty"`
	LastEventCreatedAt time.Time               `json:"last_event_created_at"`
}

// PackagePublication is a Nostr-derived projection of publication/promotion state.
type PackagePublication struct {
	ID                 uuid.UUID                `json:"id"`
	RepositoryID       uuid.UUID                `json:"repository_id"`
	ArtifactID         uuid.UUID                `json:"artifact_id"`
	Environment        string                   `json:"environment,omitempty"`
	Channel            string                   `json:"channel,omitempty"`
	TargetRepositoryID *uuid.UUID               `json:"target_repository_id,omitempty"`
	Status             PackagePublicationStatus `json:"status"`
	PolicyDecision     PackagePolicyDecision    `json:"policy_decision"`
	PolicyRef          string                   `json:"policy_ref,omitempty"`
	ApprovedBy         string                   `json:"approved_by,omitempty"`
	ApprovedAt         *time.Time               `json:"approved_at,omitempty"`
	PublishedAt        *time.Time               `json:"published_at,omitempty"`
	PromotedAt         *time.Time               `json:"promoted_at,omitempty"`
	LastError          string                   `json:"last_error,omitempty"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	LastEventID        string                   `json:"last_event_id,omitempty"`
	LastEventCreatedAt time.Time                `json:"last_event_created_at"`
}

// PackageIntent is a durable idempotency/projection cache for Nostr request events.
type PackageIntent struct {
	ID                uuid.UUID           `json:"id"`
	RequestEventID    string              `json:"request_event_id"`
	Operation         PackageOperation    `json:"operation"`
	RepositoryID      *uuid.UUID          `json:"repository_id,omitempty"`
	RepositoryName    string              `json:"repository_name,omitempty"`
	ArtifactID        *uuid.UUID          `json:"artifact_id,omitempty"`
	Namespace         string              `json:"namespace,omitempty"`
	PackageName       string              `json:"package_name,omitempty"`
	Version           string              `json:"version,omitempty"`
	Filename          string              `json:"filename,omitempty"`
	RequesterPubkey   string              `json:"requester_pubkey"`
	RequestPayload    map[string]any      `json:"request_payload,omitempty"`
	ResultPayload     map[string]any      `json:"result_payload,omitempty"`
	Status            PackageIntentStatus `json:"status"`
	ErrorMessage      string              `json:"error_message,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
	CompletedAt       *time.Time          `json:"completed_at,omitempty"`
	LastStatusEventID string              `json:"last_status_event_id,omitempty"`
	LastResultEventID string              `json:"last_result_event_id,omitempty"`
}
