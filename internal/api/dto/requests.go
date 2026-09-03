// Package dto defines request and response types for the Bahia API.
package dto

import (
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type EnvironmentTargetingRequest struct {
	DefaultUnitKey       string            `json:"default_unit_key,omitempty"`
	FailureDomainLabels  map[string]string `json:"failure_domain_labels,omitempty"`
	SecretScopeMode      string            `json:"secret_scope_mode,omitempty"`
	DefaultReconcileMode string            `json:"default_reconcile_mode,omitempty"`
}

type DeploymentUnitRequest struct {
	Key            string            `json:"key"`
	DisplayName    string            `json:"display_name,omitempty"`
	RuntimeType    string            `json:"runtime_type,omitempty"`
	EndpointRef    string            `json:"endpoint_ref,omitempty"`
	ComposeDir     string            `json:"compose_dir,omitempty"`
	Namespace      string            `json:"namespace,omitempty"`
	NetworkProfile map[string]string `json:"network_profile,omitempty"`
	GitSource      *GitSourceRequest `json:"git_source,omitempty"`
	OwnershipMode  string            `json:"ownership_mode,omitempty"`
	ReconcileMode  string            `json:"reconcile_mode,omitempty"`
	RuntimeConfig  map[string]any    `json:"runtime_config,omitempty"`
}

type GitSourceRequest struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
}

// CreateServiceRequest represents a request to register a new service.
type CreateServiceRequest struct {
	OrgID                uuid.UUID                    `json:"org_id"`
	Name                 string                       `json:"name"`
	RepoURL              string                       `json:"repo_url,omitempty"`
	Repository           *RepositoryRefRequest        `json:"repository,omitempty"`
	ArtifactRepo         string                       `json:"artifact_repo"`
	DefaultBranch        string                       `json:"default_branch,omitempty"`
	RuntimeType          string                       `json:"runtime_type,omitempty"`
	ManagedRuntimeConfig *domain.ManagedRuntimeConfig `json:"managed_runtime_config,omitempty"`
	IdempotencyKey       string                       `json:"idempotency_key,omitempty"`
}

// UpdateServiceRequest represents a request to update a service.
type UpdateServiceRequest struct {
	ID                       uuid.UUID                    `json:"id"`
	Name                     *string                      `json:"name,omitempty"`
	RepoURL                  *string                      `json:"repo_url,omitempty"`
	Repository               *RepositoryRefRequest        `json:"repository,omitempty"`
	ArtifactRepo             *string                      `json:"artifact_repo,omitempty"`
	DefaultBranch            *string                      `json:"default_branch,omitempty"`
	RuntimeType              *string                      `json:"runtime_type,omitempty"`
	ManagedRuntimeConfig     *domain.ManagedRuntimeConfig `json:"managed_runtime_config,omitempty"`
	AdoptedPublicEnvironment map[string]string            `json:"adopted_public_environment,omitempty"`
	IdempotencyKey           string                       `json:"idempotency_key,omitempty"`
}

// DeleteServiceRequest deletes a service through the signer-first control plane.
type DeleteServiceRequest struct {
	ID             uuid.UUID `json:"id"`
	Force          bool      `json:"force,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
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
	OrgID              uuid.UUID                    `json:"org_id,omitempty"`
	Name               string                       `json:"name"`
	LoomWorkerSelector map[string]any               `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any               `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	DeploymentUnits    []DeploymentUnitRequest      `json:"deployment_units,omitempty"`
	ReconcileMode      string                       `json:"reconcile_mode,omitempty"`
	DeployStrategy     string                       `json:"deploy_strategy,omitempty"`
	Protected          bool                         `json:"protected"`
}

// UpdateEnvironmentRequest represents a request to update an environment.
type UpdateEnvironmentRequest struct {
	OrgID              *uuid.UUID                   `json:"org_id,omitempty"`
	ExpectedUpdatedAt  *time.Time                   `json:"expected_updated_at,omitempty"`
	Name               *string                      `json:"name,omitempty"`
	LoomWorkerSelector *map[string]any              `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      *map[string]any              `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	DeploymentUnits    []DeploymentUnitRequest      `json:"deployment_units,omitempty"`
	ReconcileMode      *string                      `json:"reconcile_mode,omitempty"`
	DeployStrategy     *string                      `json:"deploy_strategy,omitempty"`
	Protected          *bool                        `json:"protected,omitempty"`
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

// ServiceDeployRequest is the signer-first request to create and, when
// policy permits, execute a deployment intent. RequestedBy is intentionally
// absent: handlers derive it from the verified request event.
type ServiceDeployRequest struct {
	ServiceID                uuid.UUID                  `json:"service_id"`
	EnvironmentID            uuid.UUID                  `json:"environment_id"`
	DeploymentUnitID         *uuid.UUID                 `json:"deployment_unit_id,omitempty"`
	ArtifactID               uuid.UUID                  `json:"artifact_id"`
	ExpectedDesiredStateHash string                     `json:"expected_desired_state_hash"`
	PublicRoute              *domain.PublicRouteRequest `json:"public_route,omitempty"`
	IdempotencyKey           string                     `json:"idempotency_key,omitempty"`
}

// ServiceRollbackRequest creates a fresh, policy-checked deployment intent for
// an explicit previously healthy artifact.
type ServiceRollbackRequest struct {
	ServiceID          uuid.UUID  `json:"service_id"`
	EnvironmentID      uuid.UUID  `json:"environment_id"`
	DeploymentUnitID   *uuid.UUID `json:"deployment_unit_id,omitempty"`
	TargetArtifactID   uuid.UUID  `json:"target_artifact_id"`
	SupersedesIntentID uuid.UUID  `json:"supersedes_intent_id"`
	IdempotencyKey     string     `json:"idempotency_key,omitempty"`
}

// ServiceDeployPreviewRequest builds a canonical non-secret desired state from
// a proposed managed runtime definition without persisting or applying it.
type ServiceDeployPreviewRequest struct {
	ServiceID            uuid.UUID                    `json:"service_id"`
	EnvironmentID        uuid.UUID                    `json:"environment_id"`
	DeploymentUnitID     *uuid.UUID                   `json:"deployment_unit_id,omitempty"`
	ArtifactID           uuid.UUID                    `json:"artifact_id"`
	ManagedRuntimeConfig *domain.ManagedRuntimeConfig `json:"managed_runtime_config"`
	PublicRoute          *domain.PublicRouteRequest   `json:"public_route,omitempty"`
	IdempotencyKey       string                       `json:"idempotency_key,omitempty"`
}

// DeploymentDecisionRequest approves or rejects a pending deployment intent.
type DeploymentDecisionRequest struct {
	IntentID       uuid.UUID `json:"intent_id"`
	Decision       string    `json:"decision"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

// CreateDeploymentIntentRequest represents a request to create a deployment intent.
type CreateDeploymentIntentRequest struct {
	ServiceID        uuid.UUID      `json:"service_id"`
	EnvironmentID    uuid.UUID      `json:"environment_id"`
	DeploymentUnitID *uuid.UUID     `json:"deployment_unit_id,omitempty"`
	ArtifactID       uuid.UUID      `json:"artifact_id"`
	RequestedBy      string         `json:"requested_by"`
	SourceKind       string         `json:"source_kind,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	IdempotencyKey   string         `json:"idempotency_key,omitempty"`
}

// CreateDeploymentRunRequest represents a request to create a deployment run.
type CreateDeploymentRunRequest struct {
	DeploymentIntentID uuid.UUID      `json:"deployment_intent_id"`
	DeploymentUnitID   *uuid.UUID     `json:"deployment_unit_id,omitempty"`
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
	DeploymentUnitID    *uuid.UUID     `json:"deployment_unit_id,omitempty"`
	ObservedImageDigest string         `json:"observed_image_digest"`
	ObservedImageRepo   string         `json:"observed_image_repo,omitempty"`
	ObservedContainerID string         `json:"observed_container_id,omitempty"`
	ObservedHost        string         `json:"observed_host,omitempty"`
	ObservedVersion     string         `json:"observed_version,omitempty"`
	HealthStatus        string         `json:"health_status"`
	Source              string         `json:"source"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

// LLMGatewayConfigRequest configures gateway routing for an LLM route.
type LLMGatewayConfigRequest struct {
	PublicModel      string            `json:"public_model,omitempty"`
	Path             string            `json:"path,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	HeaderSecretRefs map[string]string `json:"header_secret_refs,omitempty"`
}

// LLMPlacementPolicyRequest captures placement preferences for LLM backends.
type LLMPlacementPolicyRequest struct {
	PreferredKinds    []string       `json:"preferred_kinds,omitempty"`
	WorkerSelector    map[string]any `json:"worker_selector,omitempty"`
	MinGPUCount       int            `json:"min_gpu_count,omitempty"`
	MinGPUMemoryGB    int            `json:"min_gpu_memory_gb,omitempty"`
	MinSystemMemoryGB int            `json:"min_system_memory_gb,omitempty"`
	MaxPrice          int            `json:"max_price,omitempty"`
	AllowExternal     bool           `json:"allow_external,omitempty"`
}

// LLMPromotionGateRequest configures backend readiness checks before promotion.
type LLMPromotionGateRequest struct {
	IntervalSeconds  int `json:"interval_seconds,omitempty"`
	TimeoutSeconds   int `json:"timeout_seconds,omitempty"`
	SuccessThreshold int `json:"success_threshold,omitempty"`
	FailureThreshold int `json:"failure_threshold,omitempty"`
}

// CreateLLMRouteRequest creates an LLM route.
type CreateLLMRouteRequest struct {
	Name                   string                     `json:"name"`
	Description            string                     `json:"description,omitempty"`
	GatewayConfig          *LLMGatewayConfigRequest   `json:"gateway_config,omitempty"`
	DefaultPlacementPolicy *LLMPlacementPolicyRequest `json:"default_placement_policy,omitempty"`
	DefaultPromotionGate   *LLMPromotionGateRequest   `json:"default_promotion_gate,omitempty"`
	Metadata               map[string]any             `json:"metadata,omitempty"`
	IdempotencyKey         string                     `json:"idempotency_key,omitempty"`
}

// UpdateLLMRouteRequest updates mutable LLM route fields. Name is intentionally omitted.
type UpdateLLMRouteRequest struct {
	Description            *string                    `json:"description,omitempty"`
	GatewayConfig          *LLMGatewayConfigRequest   `json:"gateway_config,omitempty"`
	DefaultPlacementPolicy *LLMPlacementPolicyRequest `json:"default_placement_policy,omitempty"`
	DefaultPromotionGate   *LLMPromotionGateRequest   `json:"default_promotion_gate,omitempty"`
	Metadata               *map[string]any            `json:"metadata,omitempty"`
}

// LLMRuntimeManagedBackendRequest configures vLLM/Ollama/llama.cpp runtime backends.
type LLMRuntimeManagedBackendRequest struct {
	Image         string            `json:"image"`
	Scheme        string            `json:"scheme,omitempty"`
	ContainerPort int               `json:"container_port"`
	HostPort      int               `json:"host_port"`
	HealthPath    string            `json:"health_path"`
	Environment   map[string]string `json:"environment,omitempty"`
	Volumes       []string          `json:"volumes,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	NetworkMode   string            `json:"network_mode,omitempty"`
	PullAlways    bool              `json:"pull_always,omitempty"`
}

// LLMExternalBackendRequest configures externally hosted LLM backends.
type LLMExternalBackendRequest struct {
	BaseURL                string            `json:"base_url"`
	HealthURL              string            `json:"health_url,omitempty"`
	HealthHeaders          map[string]string `json:"health_headers,omitempty"`
	HealthHeaderSecretRefs map[string]string `json:"health_header_secret_refs,omitempty"`
}

// RegisterLLMHostRequest registers or updates an LLM-capable runtime host.
type RegisterLLMHostRequest struct {
	PubKey            string              `json:"pubkey"`
	Name              string              `json:"name"`
	Description       string              `json:"description,omitempty"`
	Architecture      string              `json:"architecture,omitempty"`
	MaxConcurrentJobs int                 `json:"max_concurrent_jobs,omitempty"`
	CurrentQueueDepth int                 `json:"current_queue_depth,omitempty"`
	Software          []map[string]string `json:"software,omitempty"`
	Pricing           []map[string]any    `json:"pricing,omitempty"`
	Resources         map[string]int      `json:"resources,omitempty"`
	Accelerators      []map[string]any    `json:"accelerators,omitempty"`
	RuntimeTarget     map[string]string   `json:"runtime_target,omitempty"`
	MinDurationSecs   int                 `json:"min_duration_secs,omitempty"`
	MaxDurationSecs   int                 `json:"max_duration_secs,omitempty"`
	Geohash           string              `json:"geohash,omitempty"`
	PreferredRelays   []string            `json:"preferred_relays,omitempty"`
}

// CreateLLMReleaseRequest registers an immutable model release for a route.
type CreateLLMReleaseRequest struct {
	Version            string                           `json:"version"`
	ModelRef           string                           `json:"model_ref"`
	ModelSource        string                           `json:"model_source"`
	ModelRevision      string                           `json:"model_revision,omitempty"`
	EstimatedVRAMGB    int                              `json:"estimated_vram_gb,omitempty"`
	BackendPreferences []string                         `json:"backend_preferences,omitempty"`
	RuntimeBackend     *LLMRuntimeManagedBackendRequest `json:"runtime_backend,omitempty"`
	ExternalBackend    *LLMExternalBackendRequest       `json:"external_backend,omitempty"`
	PlacementPolicy    *LLMPlacementPolicyRequest       `json:"placement_policy,omitempty"`
	PromotionGate      *LLMPromotionGateRequest         `json:"promotion_gate,omitempty"`
	Metadata           map[string]any                   `json:"metadata,omitempty"`
}

// CreateLLMDeploymentIntentRequest requests an async LLM deployment.
type CreateLLMDeploymentIntentRequest struct {
	RouteID       uuid.UUID      `json:"route_id"`
	EnvironmentID uuid.UUID      `json:"environment_id"`
	ReleaseID     uuid.UUID      `json:"release_id"`
	RequestedBy   string         `json:"requested_by"`
	SourceKind    string         `json:"source_kind,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// RollbackLLMRouteRequest requests rollback to a previous deployed LLM release.
type RollbackLLMRouteRequest struct {
	RouteID       uuid.UUID `json:"route_id"`
	EnvironmentID uuid.UUID `json:"environment_id"`
	RequestedBy   string    `json:"requested_by"`
}

// RecordLLMRouteObservationRequest records observed LLM route/backend state.
type RecordLLMRouteObservationRequest struct {
	RouteID           uuid.UUID      `json:"route_id"`
	EnvironmentID     uuid.UUID      `json:"environment_id"`
	ObservedReleaseID *uuid.UUID     `json:"observed_release_id,omitempty"`
	ObservedRunID     *uuid.UUID     `json:"observed_run_id,omitempty"`
	BackendKind       string         `json:"backend_kind,omitempty"`
	BackendEndpoint   string         `json:"backend_endpoint,omitempty"`
	BackendHealth     string         `json:"backend_health"`
	GatewayStatus     string         `json:"gateway_status"`
	GatewayTarget     string         `json:"gateway_target,omitempty"`
	GatewayConfigHash string         `json:"gateway_config_hash,omitempty"`
	Source            string         `json:"source"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

// AdoptionTargetRequest identifies one Docker host to scan/import.
type AdoptionTargetRequest struct {
	Name            string `json:"name"`
	EndpointRef     string `json:"endpoint_ref,omitempty"`
	DockerHost      string `json:"docker_host,omitempty"`
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
	OrgID      uuid.UUID                  `json:"org_id,omitempty"`
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
