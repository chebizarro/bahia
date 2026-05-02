package domain

import (
	"time"

	"github.com/google/uuid"
)

// LLMBackendKind identifies a backend family that can serve an LLM release.
type LLMBackendKind string

const (
	LLMBackendKindVLLM        LLMBackendKind = "vllm"
	LLMBackendKindOllama      LLMBackendKind = "ollama"
	LLMBackendKindLlamaCPP    LLMBackendKind = "llama_cpp"
	LLMBackendKindExternalAPI LLMBackendKind = "external_api"
)

// GatewayRouteStatus describes the observed status of a gateway route.
type GatewayRouteStatus string

const (
	GatewayRouteStatusUnknown GatewayRouteStatus = "unknown"
	GatewayRouteStatusPending GatewayRouteStatus = "pending"
	GatewayRouteStatusSynced  GatewayRouteStatus = "synced"
	GatewayRouteStatusMissing GatewayRouteStatus = "missing"
	GatewayRouteStatusError   GatewayRouteStatus = "error"
)

const (
	ModelSourceHuggingFace = "huggingface"
	ModelSourceOCI         = "oci"
	ModelSourceExternal    = "external"
)

// LLMPromotionGateConfig is a JSON-friendly health gate configuration.
type LLMPromotionGateConfig struct {
	IntervalSeconds  int `json:"interval_seconds"`
	TimeoutSeconds   int `json:"timeout_seconds"`
	SuccessThreshold int `json:"success_threshold"`
	FailureThreshold int `json:"failure_threshold"`
}

// ToHealthGateConfig converts the LLM gate config into the rollout gate shape.
func (c LLMPromotionGateConfig) ToHealthGateConfig() HealthGateConfig {
	return HealthGateConfig{
		Interval:         time.Duration(c.IntervalSeconds) * time.Second,
		Timeout:          time.Duration(c.TimeoutSeconds) * time.Second,
		SuccessThreshold: c.SuccessThreshold,
		FailureThreshold: c.FailureThreshold,
	}
}

// LLMGatewayRouteConfig configures Bahia-owned gateway routing for an LLM route.
type LLMGatewayRouteConfig struct {
	PublicModel    string            `json:"public_model"`
	Path           string            `json:"path,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
}

// LLMPlacementPolicy captures route/release placement preferences.
type LLMPlacementPolicy struct {
	PreferredKinds    []LLMBackendKind `json:"preferred_kinds,omitempty"`
	WorkerSelector    map[string]any   `json:"worker_selector,omitempty"`
	MinGPUCount       int              `json:"min_gpu_count,omitempty"`
	MinGPUMemoryGB    int              `json:"min_gpu_memory_gb,omitempty"`
	MinSystemMemoryGB int              `json:"min_system_memory_gb,omitempty"`
	MaxPrice          int              `json:"max_price,omitempty"`
	AllowExternal     bool             `json:"allow_external,omitempty"`
}

// LLMRuntimeManagedBackendConfig configures a backend that Bahia starts via a runtime target.
type LLMRuntimeManagedBackendConfig struct {
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

// LLMExternalBackendConfig configures an externally managed inference endpoint.
type LLMExternalBackendConfig struct {
	BaseURL   string `json:"base_url"`
	HealthURL string `json:"health_url,omitempty"`
}

// LLMRoute is a first-class LLM control-plane resource.
type LLMRoute struct {
	ID                     uuid.UUID               `json:"id"`
	Name                   string                  `json:"name"`
	Description            string                  `json:"description,omitempty"`
	GatewayConfig          *LLMGatewayRouteConfig  `json:"gateway_config,omitempty"`
	DefaultPlacementPolicy *LLMPlacementPolicy     `json:"default_placement_policy,omitempty"`
	DefaultPromotionGate   *LLMPromotionGateConfig `json:"default_promotion_gate,omitempty"`
	Metadata               map[string]any          `json:"metadata,omitempty"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
}

// LLMRelease is an immutable deployable model revision for a route.
type LLMRelease struct {
	ID                 uuid.UUID                       `json:"id"`
	RouteID            uuid.UUID                       `json:"route_id"`
	Version            string                          `json:"version"`
	ModelRef           string                          `json:"model_ref"`
	ModelSource        string                          `json:"model_source"`
	ModelRevision      string                          `json:"model_revision,omitempty"`
	EstimatedVRAMGB    int                             `json:"estimated_vram_gb,omitempty"`
	BackendPreferences []LLMBackendKind                `json:"backend_preferences,omitempty"`
	RuntimeBackend     *LLMRuntimeManagedBackendConfig `json:"runtime_backend,omitempty"`
	ExternalBackend    *LLMExternalBackendConfig       `json:"external_backend,omitempty"`
	PlacementPolicy    *LLMPlacementPolicy             `json:"placement_policy,omitempty"`
	PromotionGate      *LLMPromotionGateConfig         `json:"promotion_gate,omitempty"`
	Metadata           map[string]any                  `json:"metadata,omitempty"`
	CreatedAt          time.Time                       `json:"created_at"`
}

// LLMDeploymentIntent is the desired-state record for an LLM route deployment.
type LLMDeploymentIntent struct {
	ID                 uuid.UUID              `json:"id"`
	RouteID            uuid.UUID              `json:"route_id"`
	EnvironmentID      uuid.UUID              `json:"environment_id"`
	ReleaseID          uuid.UUID              `json:"release_id"`
	RequestedBy        string                 `json:"requested_by"`
	SourceKind         SourceKind             `json:"source_kind"`
	ApprovalStatus     ApprovalStatus         `json:"approval_status"`
	Status             DeploymentIntentStatus `json:"status"`
	SupersedesIntentID *uuid.UUID             `json:"supersedes_intent_id,omitempty"`
	ApprovalMetadata   map[string]any         `json:"approval_metadata,omitempty"`
	Metadata           map[string]any         `json:"metadata,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	ApprovedAt         *time.Time             `json:"approved_at,omitempty"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

// LLMDeploymentRun is a queued/executing provisioning attempt for an LLM intent.
type LLMDeploymentRun struct {
	ID                 uuid.UUID           `json:"id"`
	DeploymentIntentID uuid.UUID           `json:"deployment_intent_id"`
	BackendKind        LLMBackendKind      `json:"backend_kind,omitempty"`
	EndpointRef        string              `json:"endpoint_ref,omitempty"`
	WorkerPubkey       string              `json:"worker_pubkey,omitempty"`
	WorkerName         string              `json:"worker_name,omitempty"`
	BackendEndpoint    string              `json:"backend_endpoint,omitempty"`
	Status             DeploymentRunStatus `json:"status"`
	ExitCode           *int                `json:"exit_code,omitempty"`
	StdoutRef          string              `json:"stdout_ref,omitempty"`
	StderrRef          string              `json:"stderr_ref,omitempty"`
	Metadata           map[string]any      `json:"metadata,omitempty"`
	StartedAt          *time.Time          `json:"started_at,omitempty"`
	FinishedAt         *time.Time          `json:"finished_at,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

// LLMRouteObservation captures observed backend and gateway state for a route.
type LLMRouteObservation struct {
	ID                uuid.UUID          `json:"id"`
	RouteID           uuid.UUID          `json:"route_id"`
	EnvironmentID     uuid.UUID          `json:"environment_id"`
	ObservedReleaseID *uuid.UUID         `json:"observed_release_id,omitempty"`
	ObservedRunID     *uuid.UUID         `json:"observed_run_id,omitempty"`
	BackendKind       LLMBackendKind     `json:"backend_kind,omitempty"`
	BackendEndpoint   string             `json:"backend_endpoint,omitempty"`
	BackendHealth     HealthStatus       `json:"backend_health"`
	GatewayStatus     GatewayRouteStatus `json:"gateway_status"`
	GatewayTarget     string             `json:"gateway_target,omitempty"`
	GatewayConfigHash string             `json:"gateway_config_hash,omitempty"`
	Source            string             `json:"source"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
	ObservedAt        time.Time          `json:"observed_at"`
}

// LLMRouteState is the denormalized desired-vs-observed state for one route/environment.
type LLMRouteState struct {
	RouteID              uuid.UUID          `json:"route_id"`
	EnvironmentID        uuid.UUID          `json:"environment_id"`
	DesiredReleaseID     *uuid.UUID         `json:"desired_release_id,omitempty"`
	DesiredIntentID      *uuid.UUID         `json:"desired_intent_id,omitempty"`
	ActiveRunID          *uuid.UUID         `json:"active_run_id,omitempty"`
	CurrentObservationID *uuid.UUID         `json:"current_observation_id,omitempty"`
	DriftStatus          DriftStatus        `json:"drift_status"`
	GatewayStatus        GatewayRouteStatus `json:"gateway_status"`
	BackendKind          LLMBackendKind     `json:"backend_kind,omitempty"`
	BackendEndpoint      string             `json:"backend_endpoint,omitempty"`
	BackendHealth        HealthStatus       `json:"backend_health"`
	GatewayTarget        string             `json:"gateway_target,omitempty"`
	LastReconciledAt     *time.Time         `json:"last_reconciled_at,omitempty"`
	UpdatedAt            time.Time          `json:"updated_at"`
}
