package dto

import (
	"time"

	"github.com/google/uuid"
)

// APIResponse is a standard JSON wrapper for API responses.
type APIResponse struct {
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// ListResponse wraps a list of items with pagination metadata.
type ListResponse struct {
	Data   any `json:"data"`
	Total  int `json:"total,omitempty"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// AdoptionTargetResponse identifies one scanned/imported Docker host.
type AdoptionTargetResponse struct {
	Name            string `json:"name"`
	EndpointRef     string `json:"endpoint_ref,omitempty"`
	DockerHost      string `json:"docker_host,omitempty"`
	EnvironmentName string `json:"environment_name"`
}

// DiscoveredContainerResponse is a normalized container preview for adoption.
type DiscoveredContainerResponse struct {
	TargetName              string                   `json:"target_name"`
	EnvironmentName         string                   `json:"environment_name"`
	ContainerID             string                   `json:"container_id"`
	ContainerName           string                   `json:"container_name"`
	ImageRef                string                   `json:"image_ref"`
	ImageRepo               string                   `json:"image_repo"`
	ImageTag                string                   `json:"image_tag"`
	ImageDigest             string                   `json:"image_digest"`
	SourceRuntime           string                   `json:"source_runtime"`
	Labels                  map[string]string        `json:"labels,omitempty"`
	Environment             map[string]string        `json:"environment,omitempty"`
	RedactedEnvironmentKeys []string                 `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string                 `json:"redacted_label_keys,omitempty"`
	Ports                   []string                 `json:"ports,omitempty"`
	Volumes                 []string                 `json:"volumes,omitempty"`
	Restart                 string                   `json:"restart,omitempty"`
	Command                 []string                 `json:"command,omitempty"`
	Entrypoint              []string                 `json:"entrypoint,omitempty"`
	WorkingDir              string                   `json:"working_dir,omitempty"`
	NetworkMode             string                   `json:"network_mode,omitempty"`
	Compose                 *ComposeMetadataResponse `json:"compose,omitempty"`
	HealthStatus            string                   `json:"health_status"`
	Warnings                []string                 `json:"warnings,omitempty"`
	Adoptable               bool                     `json:"adoptable"`
}

// ComposeMetadataResponse preserves public Docker Compose origin metadata.
type ComposeMetadataResponse struct {
	ProjectName string   `json:"project_name,omitempty"`
	ServiceName string   `json:"service_name,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files,omitempty"`
}

// AdoptionPreviewContainerResponse is one discovered container plus import proposal metadata.
type AdoptionPreviewContainerResponse struct {
	Discovered          DiscoveredContainerResponse `json:"discovered"`
	ProposedServiceName string                      `json:"proposed_service_name"`
	ExistingServiceID   *uuid.UUID                  `json:"existing_service_id,omitempty"`
	WillUpdate          bool                        `json:"will_update"`
	Warnings            []string                    `json:"warnings,omitempty"`
	Adoptable           bool                        `json:"adoptable"`
}

// AdoptionPreviewResponse groups discovered containers for one target.
type AdoptionPreviewResponse struct {
	Target     AdoptionTargetResponse             `json:"target"`
	Containers []AdoptionPreviewContainerResponse `json:"containers"`
	Error      string                             `json:"error,omitempty"`
}

// AdoptionImportResultResponse reports one import candidate outcome.
type AdoptionImportResultResponse struct {
	TargetName              string     `json:"target_name"`
	ContainerID             string     `json:"container_id,omitempty"`
	ContainerName           string     `json:"container_name,omitempty"`
	ServiceName             string     `json:"service_name,omitempty"`
	ServiceID               *uuid.UUID `json:"service_id,omitempty"`
	EnvironmentID           *uuid.UUID `json:"environment_id,omitempty"`
	BuildID                 *uuid.UUID `json:"build_id,omitempty"`
	ArtifactID              *uuid.UUID `json:"artifact_id,omitempty"`
	Status                  string     `json:"status"`
	Warnings                []string   `json:"warnings,omitempty"`
	RedactedEnvironmentKeys []string   `json:"redacted_environment_keys,omitempty"`
	RedactedLabelKeys       []string   `json:"redacted_label_keys,omitempty"`
	Error                   string     `json:"error,omitempty"`
}

// RuntimeActionResponse reports a completed direct runtime action.
type RuntimeActionResponse struct {
	Action        string                      `json:"action"`
	ServiceID     uuid.UUID                   `json:"service_id"`
	EnvironmentID uuid.UUID                   `json:"environment_id"`
	Observation   *RuntimeObservationResponse `json:"observation,omitempty"`
}

// RuntimeObservationResponse is the public runtime observation contract.
type RuntimeObservationResponse struct {
	ID                  uuid.UUID      `json:"id"`
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
	ObservedAt          time.Time      `json:"observed_at"`
}
