package client

import (
	"github.com/google/uuid"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// EnvironmentTargetingRequest configures environment-level placement defaults.
type EnvironmentTargetingRequest struct {
	DefaultUnitKey       string            `json:"default_unit_key,omitempty"`
	FailureDomainLabels  map[string]string `json:"failure_domain_labels,omitempty"`
	SecretScopeMode      string            `json:"secret_scope_mode,omitempty"`
	DefaultReconcileMode string            `json:"default_reconcile_mode,omitempty"`
}

// DeploymentUnitRequest is one desired explicit environment deployment unit.
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

// GitSourceRequest identifies the git checkout backing a deployment unit.
type GitSourceRequest struct {
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Branch        string `json:"branch,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
}

// RepositoryRefRequest is signer-first structured source repository metadata.
type RepositoryRefRequest struct {
	Source         string                  `json:"source,omitempty"`
	RepoCoordinate string                  `json:"repo_coordinate,omitempty"`
	CloneURL       string                  `json:"clone_url,omitempty"`
	WebURL         string                  `json:"web_url,omitempty"`
	RelayURLs      []string                `json:"relay_urls,omitempty"`
	CI             *ServiceCIConfigRequest `json:"ci,omitempty"`
}

// ServiceCIConfigRequest describes the build workflow attached to a service repository.
type ServiceCIConfigRequest struct {
	Provider     string `json:"provider,omitempty"`
	WorkflowPath string `json:"workflow_path,omitempty"`
}

// CreateServiceNostrRequest is the service/create desired-state input shared
// by the CLI intent builder and legacy compatibility callers.
type CreateServiceNostrRequest struct {
	// ID is the client-minted service id (bahia-irsry.42): a canonical
	// UUIDv7 (or v4). The CLI mints one when it is empty; reuse it to
	// target the same entity when retrying a create.
	ID                   string                       `json:"id,omitempty"`
	OrgID                string                       `json:"org_id,omitempty"`
	Name                 string                       `json:"name"`
	RepoURL              string                       `json:"repo_url,omitempty"`
	Repository           *RepositoryRefRequest        `json:"repository,omitempty"`
	ArtifactRepo         string                       `json:"artifact_repo"`
	DefaultBranch        string                       `json:"default_branch,omitempty"`
	RuntimeType          string                       `json:"runtime_type,omitempty"`
	ManagedRuntimeConfig *domain.ManagedRuntimeConfig `json:"managed_runtime_config,omitempty"`
	IdempotencyKey       string                       `json:"idempotency_key,omitempty"`
}

// UpdateServiceNostrRequest is the signer-first service/update payload.
type UpdateServiceNostrRequest struct {
	ID                       string                       `json:"id"`
	OrgID                    *string                      `json:"org_id,omitempty"`
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

// ServiceCommandResult is the terminal acknowledgment for signer-first service mutations.
type ServiceCommandResult struct {
	Status         string          `json:"status,omitempty"`
	Service        *domain.Service `json:"service,omitempty"`
	ServiceID      string          `json:"service_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Message        string          `json:"message,omitempty"`
}

// BuildRequestNostrRequest is the signer-first build/request payload.
// IdempotencyKey controls the ContextVM d tag and _meta.progressToken; it is
// deliberately excluded from the strictly decoded business payload.
type BuildRequestNostrRequest struct {
	ServiceID               string            `json:"service_id"`
	GitRef                  string            `json:"git_ref"`
	RepositoryCredentialRef string            `json:"repository_credential_ref"`
	ArtifactRepo            string            `json:"artifact_repo"`
	BuildArgs               map[string]string `json:"build_args,omitempty"`
	IdempotencyKey          string            `json:"-"`
}

// BuildCommandResult is the terminal acknowledgment for build/request.
type BuildCommandResult struct {
	Status   string `json:"status,omitempty"`
	BuildID  string `json:"build_id,omitempty"`
	GitSHA   string `json:"git_sha,omitempty"`
	GitRef   string `json:"git_ref,omitempty"`
	CISystem string `json:"ci_system,omitempty"`
	CIRunID  string `json:"ci_run_id,omitempty"`
	Message  string `json:"message,omitempty"`
}

// BuildDetailsResult wraps one governed build read.
type BuildDetailsResult struct {
	Build *domain.Build `json:"build,omitempty"`
}

// BuildListResult is one offset-based page of governed build history.
type BuildListResult struct {
	Builds []domain.Build `json:"builds"`
	Count  int            `json:"count"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// RegisterArtifactNostrRequest is the signer-first artifact/register payload.
type RegisterArtifactNostrRequest struct {
	BuildID           string         `json:"build_id"`
	ServiceID         string         `json:"service_id"`
	ImageRepo         string         `json:"image_repo"`
	ImageTag          string         `json:"image_tag"`
	ImageDigest       string         `json:"image_digest"`
	ManifestMediaType string         `json:"manifest_media_type,omitempty"`
	SizeBytes         *int64         `json:"size_bytes,omitempty"`
	SBOMURL           string         `json:"sbom_url,omitempty"`
	SignatureRef      string         `json:"signature_ref,omitempty"`
	ScanStatus        string         `json:"scan_status,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
}

// ImportObservedArtifactNostrRequest imports an already-running,
// observation-verified image as governed lineage. There is no build id: Bahia
// creates the lineage, which is the reason this path exists.
type ImportObservedArtifactNostrRequest struct {
	ServiceID        string `json:"service_id"`
	EnvironmentID    string `json:"environment_id"`
	DeploymentUnitID string `json:"deployment_unit_id,omitempty"`
	ImageRepo        string `json:"image_repo"`
	ImageTag         string `json:"image_tag"`
	ImageDigest      string `json:"image_digest"`
	GitSHA           string `json:"git_sha,omitempty"`
	GitRef           string `json:"git_ref,omitempty"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
}

// ImportObservedArtifactResult reports imported lineage. DesiredState restates
// that importing provenance never promotes it.
type ImportObservedArtifactResult struct {
	Status           string           `json:"status,omitempty"`
	Artifact         *domain.Artifact `json:"artifact,omitempty"`
	ArtifactID       string           `json:"artifact_id,omitempty"`
	BuildID          string           `json:"build_id,omitempty"`
	ObservedDigest   string           `json:"observed_digest,omitempty"`
	ObservationID    string           `json:"observation_id,omitempty"`
	RegistryVerified bool             `json:"registry_verified,omitempty"`
	VerifiedLabels   []string         `json:"verified_labels,omitempty"`
	DesiredState     string           `json:"desired_state,omitempty"`
}

// ArtifactCommandResult is the terminal acknowledgment for signer-first artifact registration.
type ArtifactCommandResult struct {
	Status     string           `json:"status,omitempty"`
	Artifact   *domain.Artifact `json:"artifact,omitempty"`
	ArtifactID string           `json:"artifact_id,omitempty"`
	BuildID    string           `json:"build_id,omitempty"`
	ServiceID  string           `json:"service_id,omitempty"`
	Message    string           `json:"message,omitempty"`
}

// DNSPolicyApplyRequest is the signer-first dns/policy-apply payload.
type DNSPolicyApplyRequest struct {
	ID            uuid.UUID              `json:"id"`
	Name          string                 `json:"name"`
	ZoneID        *uuid.UUID             `json:"zone_id,omitempty"`
	EnvironmentID *uuid.UUID             `json:"environment_id,omitempty"`
	Rules         []domain.DNSPolicyRule `json:"rules"`
	Enabled       bool                   `json:"enabled"`
	Metadata      map[string]any         `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
}

// DNSRecordSetRequest is the signer-first dns/record-set payload. Operator
// An empty zone requests reconciliation of all configured zones.
type DNSDriftRemediateRequest struct {
	Zone string `json:"zone,omitempty"`
}

// DNSCommandResult is the terminal acknowledgment for signer-first DNS mutations.
type DNSCommandResult struct {
	Action     string `json:"action,omitempty"`
	Status     string `json:"status,omitempty"`
	Step       string `json:"step,omitempty"`
	Message    string `json:"message,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
	Zone       string `json:"zone,omitempty"`
	Policy     string `json:"policy,omitempty"`
	PolicyID   string `json:"policy_id,omitempty"`
	RuleCount  int    `json:"rule_count,omitempty"`
	OverrideID string `json:"override_id,omitempty"`
}

// CreateEnvironmentNostrRequest is the signer-first environment/create payload.
type CreateEnvironmentNostrRequest struct {
	// ID is the client-minted environment id; see CreateServiceNostrRequest.ID.
	ID                 string                       `json:"id,omitempty"`
	OrgID              string                       `json:"org_id,omitempty"`
	Name               string                       `json:"name"`
	LoomWorkerSelector map[string]any               `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      map[string]any               `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	ReconcileMode      string                       `json:"reconcile_mode,omitempty"`
	DeploymentUnits    *[]DeploymentUnitRequest     `json:"deployment_units,omitempty"`
	DeployStrategy     string                       `json:"deploy_strategy,omitempty"`
	Protected          bool                         `json:"protected"`
}

// UpdateEnvironmentNostrRequest is the signer-first environment/update payload.
type UpdateEnvironmentNostrRequest struct {
	ID                 string                       `json:"id"`
	OrgID              *string                      `json:"org_id,omitempty"`
	ExpectedUpdatedAt  *time.Time                   `json:"expected_updated_at,omitempty"`
	Name               *string                      `json:"name,omitempty"`
	LoomWorkerSelector *map[string]any              `json:"loom_worker_selector,omitempty"`
	RuntimeConfig      *map[string]any              `json:"runtime_config,omitempty"`
	Targeting          *EnvironmentTargetingRequest `json:"targeting,omitempty"`
	ReconcileMode      *string                      `json:"reconcile_mode,omitempty"`
	DeploymentUnits    *[]DeploymentUnitRequest     `json:"deployment_units,omitempty"`
	DeployStrategy     *string                      `json:"deploy_strategy,omitempty"`
	Protected          *bool                        `json:"protected,omitempty"`
}

// EnvironmentCommandResult is the terminal acknowledgment for signer-first environment mutations.
type EnvironmentCommandResult struct {
	Status          string                  `json:"status,omitempty"`
	Environment     *domain.Environment     `json:"environment,omitempty"`
	EnvironmentID   string                  `json:"environment_id,omitempty"`
	DeploymentUnits []domain.DeploymentUnit `json:"deployment_units,omitempty"`
	Message         string                  `json:"message,omitempty"`
}

// RouteAttachRequest is the signer-first service/route-attach payload.
type RouteAttachRequest struct {
	ServiceID        string                    `json:"service_id"`
	EnvironmentID    string                    `json:"environment_id"`
	DeploymentUnitID string                    `json:"deployment_unit_id,omitempty"`
	PublicRoute      domain.PublicRouteRequest `json:"public_route"`
	Internal         *bool                     `json:"internal,omitempty"`
	IdempotencyKey   string                    `json:"idempotency_key,omitempty"`
}

// RollbackDeploymentNostrRequest is the explicit signer-first rollback target.
// Requester attribution is derived from the signed event, never caller payload.
type RollbackDeploymentNostrRequest struct {
	ServiceID          string `json:"service_id"`
	EnvironmentID      string `json:"environment_id"`
	DeploymentUnitID   string `json:"deployment_unit_id,omitempty"`
	TargetArtifactID   string `json:"target_artifact_id"`
	SupersedesIntentID string `json:"supersedes_intent_id"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
}

// DeploymentIntentNostrRequest is the signer-first deployment intent target.
// Requester attribution is derived from the signed event, never caller payload.
type DeploymentIntentNostrRequest struct {
	ServiceID                string `json:"service_id"`
	EnvironmentID            string `json:"environment_id"`
	DeploymentUnitID         string `json:"deployment_unit_id,omitempty"`
	ArtifactID               string `json:"artifact_id"`
	ExpectedDesiredStateHash string `json:"expected_desired_state_hash,omitempty"`
	RequestedBy              string `json:"-"`
	IdempotencyKey           string `json:"idempotency_key,omitempty"`
}

// DeploymentPreviewNostrRequest builds a reviewed managed desired-state hash
// for a subsequent signer-first deployment request.
type DeploymentPreviewNostrRequest struct {
	ServiceID            string         `json:"service_id"`
	EnvironmentID        string         `json:"environment_id"`
	DeploymentUnitID     string         `json:"deployment_unit_id,omitempty"`
	ArtifactID           string         `json:"artifact_id"`
	ManagedRuntimeConfig map[string]any `json:"managed_runtime_config"`
	Compact              bool           `json:"compact,omitempty"`
	IdempotencyKey       string         `json:"idempotency_key,omitempty"`
}

// DeploymentApprovalNostrRequest is the signer-first approval/rejection target.
type DeploymentApprovalNostrRequest struct {
	IntentID       string `json:"intent_id"`
	Decision       string `json:"decision"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// DeploymentCommandResult is the terminal acknowledgment returned for signer-first deployment intent mutations.
type DeploymentCommandResult struct {
	Status           string                         `json:"status,omitempty"`
	IntentID         string                         `json:"intent_id,omitempty"`
	ServiceID        string                         `json:"service_id,omitempty"`
	EnvironmentID    string                         `json:"environment_id,omitempty"`
	DeploymentUnitID string                         `json:"deployment_unit_id,omitempty"`
	ArtifactID       string                         `json:"artifact_id,omitempty"`
	DesiredStateHash string                         `json:"desired_state_hash,omitempty"`
	PublicRoute      *domain.DesiredPublicRoutePlan `json:"public_route,omitempty"`
	Message          string                         `json:"message,omitempty"`
}
