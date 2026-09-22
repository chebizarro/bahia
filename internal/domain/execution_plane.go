package domain

import (
	"context"
	"github.com/google/uuid"
	"time"
)

const (
	ExecutionPlaneInspectTool     = "execution-plane/inspect"
	ExecutionPlaneApplyTool       = "execution-plane/apply"
	ExecutionPlaneProbeTool       = "execution-plane/probe"
	ExecutionPlaneProtocolVersion = 1
)

type ExecutionPlaneState string

const (
	ExecutionPlaneEnabled  ExecutionPlaneState = "enabled"
	ExecutionPlaneDisabled ExecutionPlaneState = "disabled"
)

type ExecutionPlanePackagePin struct {
	Digest     string       `json:"digest"`
	Version    string       `json:"version"`
	Provenance VMProvenance `json:"provenance"`
}
type ExecutionPlaneImagePin struct {
	LifecycleClass VMLifecycleClass `json:"lifecycle_class"`
	ImageID        uuid.UUID        `json:"image_id"`
	ManifestDigest string           `json:"manifest_digest"`
}

// ExecutionPlaneCapability is intentionally structured: Windows cannot hide in
// an arbitrary advertised feature string. No persistent lifecycle is eligible.
type ExecutionPlaneCapability struct {
	LifecycleClass       VMLifecycleClass  `json:"lifecycle_class"`
	OS                   VMOperatingSystem `json:"os"`
	Architecture         string            `json:"architecture"`
	AgentProtocolVersion string            `json:"agent_protocol_version"`
}
type ExecutionPlaneProbePolicy struct {
	IntervalSeconds  int64 `json:"interval_seconds"`
	FreshnessSeconds int64 `json:"freshness_seconds"`
}

func DefaultExecutionPlaneProbePolicy() ExecutionPlaneProbePolicy {
	return ExecutionPlaneProbePolicy{30, 90}
}

type ExecutionPlaneConfiguration struct {
	Revision       string               `json:"revision"` // sha256 of canonical configuration
	Network        VMNetwork            `json:"network"`
	SecretBindings []VMBootstrapBinding `json:"secret_bindings"`
}
type ExecutionPlaneDesired struct {
	LifecycleClasses     []VMLifecycleClass          `json:"lifecycle_classes"`
	Package              ExecutionPlanePackagePin    `json:"package"`
	Configuration        ExecutionPlaneConfiguration `json:"configuration"`
	ImagePins            []ExecutionPlaneImagePin    `json:"image_pins"`
	ReservedCapacity     VMCapacity                  `json:"reserved_capacity"`
	Concurrency          int                         `json:"concurrency"`
	ExpectedCapabilities []ExecutionPlaneCapability  `json:"expected_capabilities"`
	State                ExecutionPlaneState         `json:"state"`
	ProbePolicy          ExecutionPlaneProbePolicy   `json:"probe_policy"`
}
type ExecutionPlaneDeployment struct {
	ObservationCursor *VMObservationCursor `json:"observation_cursor,omitempty"`
	VirtualizationResourceMeta
	HostID                uuid.UUID                  `json:"host_id"`
	WorkerPubKey          string                     `json:"worker_pubkey"`
	ManagementEndpointRef uuid.UUID                  `json:"management_endpoint_ref"`
	ManagementAuthor      string                     `json:"management_author"`
	Desired               ExecutionPlaneDesired      `json:"desired"`
	Observation           *ExecutionPlaneObservation `json:"observation,omitempty"`
}
type ExecutionPlaneObservation struct {
	VMObservationStamp
	PlaneID          uuid.UUID                    `json:"plane_id"`
	HostID           uuid.UUID                    `json:"host_id"`
	Author           string                       `json:"author"`
	LifecycleClasses []VMLifecycleClass           `json:"lifecycle_classes"`
	Availability     VMObservationAvailability    `json:"availability"`
	Drift            VMDrift                      `json:"drift"`
	PackageDigest    string                       `json:"package_digest"`
	ConfigRevision   string                       `json:"config_revision"`
	ImagePins        []ExecutionPlaneImagePin     `json:"image_pins"`
	ReservedCapacity VMCapacity                   `json:"reserved_capacity"`
	Concurrency      int                          `json:"concurrency"`
	State            ExecutionPlaneState          `json:"state"`
	Draining         bool                         `json:"draining"`
	Probe            *ExecutionPlaneProbeEvidence `json:"probe,omitempty"`
	Diagnostic       VMDiagnostic                 `json:"diagnostic"`
}
type ExecutionPlaneProbeEvidence struct {
	VMObservationStamp
	PlaneID          uuid.UUID                  `json:"plane_id"`
	Author           string                     `json:"author"`
	LifecycleClasses []VMLifecycleClass         `json:"lifecycle_classes"`
	Successful       bool                       `json:"successful"`
	PackageDigest    string                     `json:"package_digest"`
	ConfigRevision   string                     `json:"config_revision"`
	ImagePins        []ExecutionPlaneImagePin   `json:"image_pins"`
	Capabilities     []ExecutionPlaneCapability `json:"capabilities"`
	Diagnostic       VMDiagnostic               `json:"diagnostic"`
}

// VerifiedExecutionPlaneCapabilities belongs to D, separate from worker software
// advertisements. Expiry/session replacement/failure retract this contribution.
type VerifiedExecutionPlaneCapabilities struct {
	PlaneID       uuid.UUID                  `json:"plane_id"`
	Generation    int64                      `json:"generation"`
	SessionID     uuid.UUID                  `json:"session_id"`
	ProbeSequence int64                      `json:"probe_sequence"`
	ExpiresAt     time.Time                  `json:"expires_at"`
	Capabilities  []ExecutionPlaneCapability `json:"capabilities"`
}

// ExecutionPlaneClient is the administrative boundary D implements, not SubmitJob.
// Apply returns admission only; Observe must deliver authenticated current state.
// No endpoint support means unavailable, without host/provider/shell fallback.
type ExecutionPlaneClient interface {
	Discover(context.Context, ExecutionPlaneEndpoint) (ExecutionPlaneSupport, error)
	Inspect(context.Context, ExecutionPlaneEndpoint, uuid.UUID) (*ExecutionPlaneObservation, error)
	Apply(context.Context, ExecutionPlaneEndpoint, ExecutionPlaneApplyRequest) (*ExecutionPlaneAcknowledgment, error)
	Probe(context.Context, ExecutionPlaneEndpoint, ExecutionPlaneProbeRequest) (*ExecutionPlaneProbeEvidence, error)
	Observe(context.Context, ExecutionPlaneEndpoint, uuid.UUID, ExecutionPlaneObserver) error
}
type ExecutionPlaneEndpoint struct {
	HostID      uuid.UUID
	EndpointRef uuid.UUID
	Author      string
}
type ExecutionPlaneSupport struct {
	ProtocolVersion  int                `json:"protocol_version"`
	Tools            []string           `json:"tools"`
	LifecycleClasses []VMLifecycleClass `json:"lifecycle_classes"`
}
type ExecutionPlaneApplyRequest struct {
	SchemaVersion int                   `json:"schema_version"`
	PlaneID       uuid.UUID             `json:"plane_id"`
	HostID        uuid.UUID             `json:"host_id"`
	Generation    int64                 `json:"generation"`
	OperationID   uuid.UUID             `json:"operation_id"`
	Desired       ExecutionPlaneDesired `json:"desired"`
}
type ExecutionPlaneProbeRequest struct {
	PlaneID          uuid.UUID          `json:"plane_id"`
	Generation       int64              `json:"generation"`
	SessionID        uuid.UUID          `json:"session_id"`
	Sequence         int64              `json:"sequence"`
	LifecycleClasses []VMLifecycleClass `json:"lifecycle_classes"`
}
type ExecutionPlaneAcknowledgment struct {
	OperationID uuid.UUID    `json:"operation_id"`
	Accepted    bool         `json:"accepted"`
	Diagnostic  VMDiagnostic `json:"diagnostic"`
}
type ExecutionPlaneObserver interface {
	OnObservation(context.Context, ExecutionPlaneObservation) error
	OnEOSE(context.Context) error
	OnUnavailable(context.Context, VMDiagnostic) error
}
