package client

import (
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AdoptionTarget identifies one Docker host for adoption scan/import.
type AdoptionTarget struct {
	Name            string `json:"name"`
	EndpointRef     string `json:"endpoint_ref,omitempty"`
	DockerHost      string `json:"docker_host,omitempty"`
	EnvironmentName string `json:"environment_name,omitempty"`
}

// AdoptionScanRequest requests an adoption preview scan.
type AdoptionScanRequest struct {
	Targets []AdoptionTarget `json:"targets"`
}

// AdoptionSelection selects one discovered container for import.
type AdoptionSelection struct {
	TargetName          string `json:"target_name"`
	ContainerID         string `json:"container_id"`
	ServiceNameOverride string `json:"service_name_override,omitempty"`
}

// AdoptionImportRequest requests adoption import for selected or all containers.
type AdoptionImportRequest struct {
	Targets        []AdoptionTarget    `json:"targets"`
	Selections     []AdoptionSelection `json:"selections,omitempty"`
	ImportAll      bool                `json:"import_all,omitempty"`
	IdempotencyKey string              `json:"-"`
	OrgID          string              `json:"org_id,omitempty"`
}

// DiscoveredContainer is a normalized container preview returned by adoption scan.
type DiscoveredContainer struct {
	TargetName              string            `json:"target_name"`
	EnvironmentName         string            `json:"environment_name"`
	ContainerID             string            `json:"container_id"`
	ContainerName           string            `json:"container_name"`
	ImageRef                string            `json:"image_ref"`
	ImageRepo               string            `json:"image_repo"`
	ImageTag                string            `json:"image_tag"`
	ImageDigest             string            `json:"image_digest"`
	SourceRuntime           string            `json:"source_runtime"`
	Labels                  map[string]string `json:"labels,omitempty"`
	Environment             map[string]string `json:"environment,omitempty"`
	RedactedEnvironmentKeys []string          `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string          `json:"redacted_label_keys,omitempty"`
	Ports                   []string          `json:"ports,omitempty"`
	Volumes                 []string          `json:"volumes,omitempty"`
	Restart                 string            `json:"restart,omitempty"`
	Command                 []string          `json:"command,omitempty"`
	Entrypoint              []string          `json:"entrypoint,omitempty"`
	WorkingDir              string            `json:"working_dir,omitempty"`
	NetworkMode             string            `json:"network_mode,omitempty"`
	Compose                 *ComposeMetadata  `json:"compose,omitempty"`
	HealthStatus            string            `json:"health_status"`
	Warnings                []string          `json:"warnings,omitempty"`
	Adoptable               bool              `json:"adoptable"`
}

// ComposeMetadata preserves public Docker Compose origin metadata.
type ComposeMetadata struct {
	ProjectName string   `json:"project_name,omitempty"`
	ServiceName string   `json:"service_name,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files,omitempty"`
}

// AdoptionPreviewContainer is one discovered container plus import proposal metadata.
type AdoptionPreviewContainer struct {
	Discovered          DiscoveredContainer `json:"discovered"`
	ProposedServiceName string              `json:"proposed_service_name"`
	ExistingServiceID   *string             `json:"existing_service_id,omitempty"`
	WillUpdate          bool                `json:"will_update"`
	Warnings            []string            `json:"warnings,omitempty"`
	Adoptable           bool                `json:"adoptable"`
}

// AdoptionPreview groups discovered containers for one target.
type AdoptionPreview struct {
	Target     AdoptionTarget             `json:"target"`
	Containers []AdoptionPreviewContainer `json:"containers"`
	Error      string                     `json:"error,omitempty"`
}

// AdoptionImportResult reports one import candidate outcome.
type AdoptionImportResult struct {
	TargetName              string   `json:"target_name"`
	ContainerID             string   `json:"container_id,omitempty"`
	ContainerName           string   `json:"container_name,omitempty"`
	ServiceName             string   `json:"service_name,omitempty"`
	ServiceID               *string  `json:"service_id,omitempty"`
	EnvironmentID           *string  `json:"environment_id,omitempty"`
	BuildID                 *string  `json:"build_id,omitempty"`
	ArtifactID              *string  `json:"artifact_id,omitempty"`
	Status                  string   `json:"status"`
	Warnings                []string `json:"warnings,omitempty"`
	RedactedEnvironmentKeys []string `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string `json:"redacted_label_keys,omitempty"`
	Error                   string   `json:"error,omitempty"`
}

// RuntimeActionResult reports a direct runtime action result.
type RuntimeActionResult struct {
	Action        string              `json:"action"`
	ServiceID     string              `json:"service_id"`
	EnvironmentID string              `json:"environment_id"`
	Observation   *RuntimeObservation `json:"observation,omitempty"`
}

// RuntimeObservation is the public runtime observation response shape.
type RuntimeObservation struct {
	ID                  string         `json:"id"`
	ServiceID           string         `json:"service_id"`
	EnvironmentID       string         `json:"environment_id"`
	ObservedImageDigest string         `json:"observed_image_digest"`
	ObservedImageRepo   string         `json:"observed_image_repo,omitempty"`
	ObservedContainerID string         `json:"observed_container_id,omitempty"`
	ObservedHost        string         `json:"observed_host,omitempty"`
	ObservedVersion     string         `json:"observed_version,omitempty"`
	HealthStatus        string         `json:"health_status"`
	Source              string         `json:"source"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	ObservedAt          time.Time      `json:"observed_at"`
}

// EnvironmentDetails is the environment read model with its resolved deployment units.
type EnvironmentDetails struct {
	domain.Environment
	DeploymentUnits []domain.DeploymentUnit `json:"deployment_units"`
}

// SecretRef is a reference to a secret (without the actual value).
type SecretRef struct {
	ID               string `json:"id"`
	ServiceID        string `json:"service_id"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	Name             string `json:"name"`
	EncryptionMethod string `json:"encryption_method"`
	Version          int    `json:"version"`
}
