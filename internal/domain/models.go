// Package domain defines the core domain types for Bahia.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// BuildStatus represents the state of a CI build.
type BuildStatus string

const (
	BuildStatusQueued    BuildStatus = "queued"
	BuildStatusRunning   BuildStatus = "running"
	BuildStatusSucceeded BuildStatus = "succeeded"
	BuildStatusFailed    BuildStatus = "failed"
	BuildStatusCancelled BuildStatus = "cancelled"
)

// ScanStatus represents the vulnerability scan state of an artifact.
type ScanStatus string

const (
	ScanStatusUnknown ScanStatus = "unknown"
	ScanStatusPending ScanStatus = "pending"
	ScanStatusClean   ScanStatus = "clean"
	ScanStatusWarning ScanStatus = "warning"
	ScanStatusFailed  ScanStatus = "failed"
)

// SourceKind indicates how a deployment intent was initiated.
type SourceKind string

const (
	SourceKindManual         SourceKind = "manual"
	SourceKindAutoPromote    SourceKind = "auto_promote"
	SourceKindRollback       SourceKind = "rollback"
	SourceKindScheduled      SourceKind = "scheduled"
	SourceKindEventTriggered SourceKind = "event_triggered"
)

// ApprovalStatus represents the approval state of a deployment intent.
type ApprovalStatus string

const (
	ApprovalStatusNotRequired ApprovalStatus = "not_required"
	ApprovalStatusPending     ApprovalStatus = "pending"
	ApprovalStatusApproved    ApprovalStatus = "approved"
	ApprovalStatusRejected    ApprovalStatus = "rejected"
)

// DeploymentIntentStatus represents the lifecycle state of a deployment intent.
type DeploymentIntentStatus string

const (
	IntentStatusPending    DeploymentIntentStatus = "pending"
	IntentStatusApproved   DeploymentIntentStatus = "approved"
	IntentStatusRejected   DeploymentIntentStatus = "rejected"
	IntentStatusSuperseded DeploymentIntentStatus = "superseded"
	IntentStatusDeploying  DeploymentIntentStatus = "deploying"
	IntentStatusDeployed   DeploymentIntentStatus = "deployed"
	IntentStatusFailed     DeploymentIntentStatus = "failed"
	IntentStatusRolledBack DeploymentIntentStatus = "rolled_back"
)

// DeploymentRunStatus represents the execution state of a deployment run.
type DeploymentRunStatus string

const (
	RunStatusQueued    DeploymentRunStatus = "queued"
	RunStatusRunning   DeploymentRunStatus = "running"
	RunStatusSucceeded DeploymentRunStatus = "succeeded"
	RunStatusFailed    DeploymentRunStatus = "failed"
	RunStatusCancelled DeploymentRunStatus = "cancelled"
	RunStatusTimeout   DeploymentRunStatus = "timeout"
)

// HealthStatus represents the observed health of a running service.
type HealthStatus string

const (
	HealthStatusUnknown   HealthStatus = "unknown"
	HealthStatusStarting  HealthStatus = "starting"
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusStopped   HealthStatus = "stopped"
)

// DriftStatus represents whether observed state matches desired state.
type DriftStatus string

const (
	DriftStatusUnknown   DriftStatus = "unknown"
	DriftStatusInSync    DriftStatus = "in_sync"
	DriftStatusDrifted   DriftStatus = "drifted"
	DriftStatusDeploying DriftStatus = "deploying"
)

// DeployStrategy is the method used to deploy to an environment.
type DeployStrategy string

const (
	DeployStrategyReplace DeployStrategy = "replace"
	DeployStrategyBlueGreen DeployStrategy = "blue_green"
	DeployStrategyCanary  DeployStrategy = "canary"
)

// RuntimeType identifies the target runtime.
type RuntimeType string

const (
	RuntimeTypeDocker  RuntimeType = "docker"
	RuntimeTypeCompose RuntimeType = "compose"
	RuntimeTypeK8s     RuntimeType = "kubernetes"
	RuntimeTypePodman  RuntimeType = "podman"
)

// Service represents a deployable application component.
type Service struct {
	ID            uuid.UUID      `json:"id"`
	Name          string         `json:"name"`
	RepoURL       string         `json:"repo_url,omitempty"`
	Repository    *RepositoryRef `json:"repository,omitempty"`
	ArtifactRepo  string         `json:"artifact_repo"`
	DefaultBranch string         `json:"default_branch"`
	RuntimeType   RuntimeType    `json:"runtime_type"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// RepositoryRef captures structured source repository metadata for a service.
type RepositoryRef struct {
	Source         string           `json:"source"`
	RepoCoordinate string           `json:"repo_coordinate,omitempty"`
	CloneURL       string           `json:"clone_url,omitempty"`
	WebURL         string           `json:"web_url,omitempty"`
	RelayURLs      []string         `json:"relay_urls,omitempty"`
	CI             *ServiceCIConfig `json:"ci,omitempty"`
}

// ServiceCIConfig contains CI provider settings for a service repository.
type ServiceCIConfig struct {
	Provider     string `json:"provider"`
	WorkflowPath string `json:"workflow_path,omitempty"`
}

// Environment represents a named deployment target.
type Environment struct {
	ID                 uuid.UUID      `json:"id"`
	Name               string         `json:"name"`
	LoomWorkerSelector map[string]any `json:"loom_worker_selector"`
	RuntimeConfig      map[string]any `json:"runtime_config"`
	DeployStrategy     DeployStrategy `json:"deploy_strategy"`
	Protected          bool           `json:"protected"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// Build represents a CI build execution.
type Build struct {
	ID            uuid.UUID   `json:"id"`
	ServiceID     uuid.UUID   `json:"service_id"`
	GitSHA        string      `json:"git_sha"`
	GitRef        string      `json:"git_ref"`
	CISystem      string      `json:"ci_system"`
	CIRunID       string      `json:"ci_run_id"`
	LoomJobID     string      `json:"loom_job_id,omitempty"`
	Status        BuildStatus `json:"status"`
	SourceEventID string      `json:"source_event_id,omitempty"`
	StartedAt     *time.Time  `json:"started_at,omitempty"`
	FinishedAt    *time.Time  `json:"finished_at,omitempty"`
	Metadata      map[string]any `json:"metadata"`
	CreatedAt     time.Time   `json:"created_at"`
}

// Artifact represents an immutable OCI image artifact.
type Artifact struct {
	ID                uuid.UUID      `json:"id"`
	BuildID           uuid.UUID      `json:"build_id"`
	ServiceID         uuid.UUID      `json:"service_id"`
	ImageRepo         string         `json:"image_repo"`
	ImageTag          string         `json:"image_tag"`
	ImageDigest       string         `json:"image_digest"`
	ManifestMediaType string         `json:"manifest_media_type,omitempty"`
	SizeBytes         *int64         `json:"size_bytes,omitempty"`
	SBOMURL           string         `json:"sbom_url,omitempty"`
	SignatureRef      string         `json:"signature_ref,omitempty"`
	ScanStatus        ScanStatus     `json:"scan_status"`
	Metadata          map[string]any `json:"metadata"`
	CreatedAt         time.Time      `json:"created_at"`
}

// DeploymentIntent represents a request to deploy an artifact to an environment.
type DeploymentIntent struct {
	ID                  uuid.UUID              `json:"id"`
	ServiceID           uuid.UUID              `json:"service_id"`
	EnvironmentID       uuid.UUID              `json:"environment_id"`
	ArtifactID          uuid.UUID              `json:"artifact_id"`
	RequestedBy         string                 `json:"requested_by"`
	SourceKind          SourceKind             `json:"source_kind"`
	ApprovalStatus      ApprovalStatus         `json:"approval_status"`
	Status              DeploymentIntentStatus `json:"status"`
	SupersedesIntentID  *uuid.UUID             `json:"supersedes_intent_id,omitempty"`
	ApprovalMetadata    map[string]any         `json:"approval_metadata"`
	Metadata            map[string]any         `json:"metadata"`
	CreatedAt           time.Time              `json:"created_at"`
	ApprovedAt          *time.Time             `json:"approved_at,omitempty"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

// DeploymentRun represents a concrete deployment execution attempt.
type DeploymentRun struct {
	ID                 uuid.UUID           `json:"id"`
	DeploymentIntentID uuid.UUID           `json:"deployment_intent_id"`
	LoomJobID          string              `json:"loom_job_id,omitempty"`
	WorkerPubkey       string              `json:"worker_pubkey,omitempty"`
	WorkerName         string              `json:"worker_name,omitempty"`
	Status             DeploymentRunStatus `json:"status"`
	ExitCode           *int                `json:"exit_code,omitempty"`
	StdoutRef          string              `json:"stdout_ref,omitempty"`
	StderrRef          string              `json:"stderr_ref,omitempty"`
	StartedAt          *time.Time          `json:"started_at,omitempty"`
	FinishedAt         *time.Time          `json:"finished_at,omitempty"`
	Metadata           map[string]any      `json:"metadata"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

// RuntimeObservation represents a snapshot of actual runtime state.
type RuntimeObservation struct {
	ID                  uuid.UUID      `json:"id"`
	ServiceID           uuid.UUID      `json:"service_id"`
	EnvironmentID       uuid.UUID      `json:"environment_id"`
	ObservedImageDigest string         `json:"observed_image_digest"`
	ObservedImageRepo   string         `json:"observed_image_repo,omitempty"`
	ObservedContainerID string         `json:"observed_container_id,omitempty"`
	ObservedHost        string         `json:"observed_host,omitempty"`
	ObservedVersion     string         `json:"observed_version,omitempty"`
	HealthStatus        HealthStatus   `json:"health_status"`
	Source              string         `json:"source"`
	Metadata            map[string]any `json:"metadata"`
	ObservedAt          time.Time      `json:"observed_at"`
}

// EnvironmentServiceState is a denormalized view of current desired and observed state.
type EnvironmentServiceState struct {
	ServiceID            uuid.UUID   `json:"service_id"`
	EnvironmentID        uuid.UUID   `json:"environment_id"`
	DesiredArtifactID    *uuid.UUID  `json:"desired_artifact_id,omitempty"`
	DesiredIntentID      *uuid.UUID  `json:"desired_intent_id,omitempty"`
	LastSuccessfulRunID  *uuid.UUID  `json:"last_successful_run_id,omitempty"`
	CurrentObservationID *uuid.UUID  `json:"current_observation_id,omitempty"`
	DriftStatus          DriftStatus `json:"drift_status"`
	LastReconciledAt     *time.Time  `json:"last_reconciled_at,omitempty"`
	UpdatedAt            time.Time   `json:"updated_at"`
}
