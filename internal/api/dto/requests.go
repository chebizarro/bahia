// Package dto defines request and response types for the Bahia API.
package dto

import "github.com/google/uuid"

// CreateServiceRequest represents a request to register a new service.
type CreateServiceRequest struct {
	Name          string                `json:"name"`
	RepoURL       string                `json:"repo_url,omitempty"`
	Repository    *RepositoryRefRequest `json:"repository,omitempty"`
	ArtifactRepo  string                `json:"artifact_repo"`
	DefaultBranch string                `json:"default_branch,omitempty"`
	RuntimeType   string                `json:"runtime_type,omitempty"`
}

// UpdateServiceRequest represents a request to update a service.
type UpdateServiceRequest struct {
	Name          *string               `json:"name,omitempty"`
	RepoURL       *string               `json:"repo_url,omitempty"`
	Repository    *RepositoryRefRequest `json:"repository,omitempty"`
	ArtifactRepo  *string               `json:"artifact_repo,omitempty"`
	DefaultBranch *string               `json:"default_branch,omitempty"`
	RuntimeType   *string               `json:"runtime_type,omitempty"`
}

// RepositoryRefRequest is the request payload for structured repository metadata.
type RepositoryRefRequest struct {
	Source         string                  `json:"source,omitempty"`
	RepoCoordinate string                  `json:"repo_coordinate,omitempty"`
	CloneURL       string                  `json:"clone_url,omitempty"`
	WebURL         string                  `json:"web_url,omitempty"`
	RelayURLs      []string                `json:"relay_urls,omitempty"`
	CI             *ServiceCIConfigRequest `json:"ci,omitempty"`
}

// ServiceCIConfigRequest is the request payload for CI configuration.
type ServiceCIConfigRequest struct {
	Provider     string `json:"provider,omitempty"`
	WorkflowPath string `json:"workflow_path,omitempty"`
}

// CreateEnvironmentRequest represents a request to register a new environment.
type CreateEnvironmentRequest struct {
	Name               string         `json:"name"`
	LoomWorkerSelector map[string]any `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any `json:"runtime_config,omitempty"`
	DeployStrategy     string         `json:"deploy_strategy,omitempty"`
	Protected          bool           `json:"protected"`
}

// UpdateEnvironmentRequest represents a request to update an environment.
type UpdateEnvironmentRequest struct {
	Name               *string         `json:"name,omitempty"`
	LoomWorkerSelector *map[string]any `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      *map[string]any `json:"runtime_config,omitempty"`
	DeployStrategy     *string         `json:"deploy_strategy,omitempty"`
	Protected          *bool           `json:"protected,omitempty"`
}

// RegisterBuildRequest represents a request to register a new build.
type RegisterBuildRequest struct {
	ServiceID     uuid.UUID      `json:"service_id"`
	GitSHA        string         `json:"git_sha"`
	GitRef        string         `json:"git_ref"`
	CISystem      string         `json:"ci_system,omitempty"`
	CIRunID       string         `json:"ci_run_id"`
	LoomJobID     string         `json:"loom_job_id,omitempty"`
	Status        string         `json:"status,omitempty"`
	SourceEventID string         `json:"source_event_id,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// UpdateBuildStatusRequest represents a request to update build status.
type UpdateBuildStatusRequest struct {
	Status string `json:"status"`
}

// RegisterArtifactRequest represents a request to register a new artifact.
type RegisterArtifactRequest struct {
	BuildID           uuid.UUID      `json:"build_id"`
	ServiceID         uuid.UUID      `json:"service_id"`
	ImageRepo         string         `json:"image_repo"`
	ImageTag          string         `json:"image_tag"`
	ImageDigest       string         `json:"image_digest"`
	ManifestMediaType string         `json:"manifest_media_type,omitempty"`
	SizeBytes         *int64         `json:"size_bytes,omitempty"`
	SBOMURL           string         `json:"sbom_url,omitempty"`
	SignatureRef      string         `json:"signature_ref,omitempty"`
	ScanStatus        string         `json:"scan_status,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// CreateDeploymentIntentRequest represents a request to create a deployment intent.
type CreateDeploymentIntentRequest struct {
	ServiceID     uuid.UUID      `json:"service_id"`
	EnvironmentID uuid.UUID      `json:"environment_id"`
	ArtifactID    uuid.UUID      `json:"artifact_id"`
	RequestedBy   string         `json:"requested_by"`
	SourceKind    string         `json:"source_kind,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// CreateDeploymentRunRequest represents a request to create a deployment run.
type CreateDeploymentRunRequest struct {
	DeploymentIntentID uuid.UUID      `json:"deployment_intent_id"`
	LoomJobID          string         `json:"loom_job_id,omitempty"`
	WorkerPubkey       string         `json:"worker_pubkey,omitempty"`
	WorkerName         string         `json:"worker_name,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// CompleteDeploymentRunRequest represents a request to mark a deployment run as complete.
type CompleteDeploymentRunRequest struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code,omitempty"`
}

// RollbackRequest represents a request to roll back a deployment.
type RollbackRequest struct {
	ServiceID     uuid.UUID `json:"service_id"`
	EnvironmentID uuid.UUID `json:"environment_id"`
	RequestedBy   string    `json:"requested_by"`
}

// RecordObservationRequest represents a request to record a runtime observation.
type RecordObservationRequest struct {
	ServiceID           uuid.UUID      `json:"service_id"`
	EnvironmentID       uuid.UUID      `json:"environment_id"`
	ObservedImageDigest string         `json:"observed_image_digest"`
	ObservedImageRepo   string         `json:"observed_image_repo,omitempty"`
	ObservedContainerID string         `json:"observed_container_id,omitempty"`
	ObservedHost        string         `json:"observed_host,omitempty"`
	ObservedVersion     string         `json:"observed_version,omitempty"`
	HealthStatus        string         `json:"health_status"`
	Source              string         `json:"source"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

// AdoptionTargetRequest identifies one Docker host to scan/import.
type AdoptionTargetRequest struct {
	Name            string `json:"name"`
	DockerHost      string `json:"docker_host"`
	EnvironmentName string `json:"environment_name,omitempty"`
}

// ScanAdoptionRequest requests an adoption preview scan across one or more targets.
type ScanAdoptionRequest struct {
	Targets []AdoptionTargetRequest `json:"targets"`
}

// AdoptionSelectionRequest selects one discovered container for import.
type AdoptionSelectionRequest struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

// ImportAdoptionRequest imports selected or all discovered containers from targets.
type ImportAdoptionRequest struct {
	Targets    []AdoptionTargetRequest    `json:"targets"`
	Selections []AdoptionSelectionRequest `json:"selections,omitempty"`
	ImportAll  bool                       `json:"import_all,omitempty"`
}

// DeployServiceActionRequest requests a direct runtime deploy action.
type DeployServiceActionRequest struct {
	ArtifactID *uuid.UUID `json:"artifact_id,omitempty"`
}

// LookupRepositoryCIRequest is the request payload for batch CI status lookup.
type LookupRepositoryCIRequest struct {
	RepoCoordinates         []string `json:"repo_coordinates"`
	IncludeDisabledPolicies bool     `json:"include_disabled_policies,omitempty"`
}

// ListBlossomBlobsRequest is the request payload for listing Blossom blobs.
type ListBlossomBlobsRequest struct {
	// Pubkey filters blobs by owner pubkey. If empty, lists blobs for the server's configured identity.
	Pubkey string `json:"pubkey,omitempty"`
}
