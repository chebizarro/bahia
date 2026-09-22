package domain

import (
	"github.com/google/uuid"
	"time"
)

type VMImageFormat string

const (
	VMImageQCOW2             VMImageFormat = "qcow2"
	VMImageFirecrackerRootFS VMImageFormat = "firecracker-rootfs"
)

type VMOperatingSystem string

const (
	VMOSLinux   VMOperatingSystem = "linux"
	VMOSWindows VMOperatingSystem = "windows"
)

type VMFirmware string

const (
	VMFirmwareBIOS VMFirmware = "bios"
	VMFirmwareUEFI VMFirmware = "uefi"
	VMFirmwareNone VMFirmware = "none"
)

type VMComponentKind string

const (
	VMComponentDisk   VMComponentKind = "disk"
	VMComponentKernel VMComponentKind = "kernel"
	VMComponentRootFS VMComponentKind = "rootfs"
	VMComponentNVRAM  VMComponentKind = "nvram"
	VMComponentSWTPM  VMComponentKind = "swtpm"
)

// StorageRef is an opaque private storage object identifier, never a host path.
// Providers resolve it through trusted storage configuration.
type VMComponent struct {
	Kind       VMComponentKind `json:"kind"`
	StorageRef uuid.UUID       `json:"storage_ref"`
	Digest     string          `json:"digest"`
	SizeBytes  int64           `json:"size_bytes"`
}
type VMProvenance struct {
	EventID    string    `json:"event_id"`
	Signer     string    `json:"signer"`
	Verified   bool      `json:"verified"`
	VerifiedAt time.Time `json:"verified_at"`
}
type VMImage struct {
	VirtualizationResourceMeta
	ManifestDigest       string             `json:"manifest_digest"`
	Format               VMImageFormat      `json:"format"`
	Architecture         string             `json:"architecture"`
	OS                   VMOperatingSystem  `json:"os"`
	LifecycleClasses     []VMLifecycleClass `json:"lifecycle_classes"`
	Components           []VMComponent      `json:"components"`
	Firmware             VMFirmware         `json:"firmware"`
	DriverContract       string             `json:"driver_contract"`
	AgentProtocolVersion string             `json:"agent_protocol_version"`
	AllowedProfiles      []string           `json:"allowed_profiles"`
	ReleaseRef           uuid.UUID          `json:"release_ref"`
	Provenance           VMProvenance       `json:"provenance"`
}

type VMPurpose string

const (
	VMPurposeDesktop VMPurpose = "desktop"
	VMPurposeService VMPurpose = "service"
)

type VMDesiredPower string

const (
	VMDesiredStopped VMDesiredPower = "stopped"
	VMDesiredRunning VMDesiredPower = "running"
)

type VMRuntimeState string

const (
	VMRuntimeAbsent  VMRuntimeState = "absent"
	VMRuntimeStopped VMRuntimeState = "stopped"
	VMRuntimeRunning VMRuntimeState = "running"
	VMRuntimePaused  VMRuntimeState = "paused"
	VMRuntimeFailed  VMRuntimeState = "failed"
)

type VMGuestHealth string

const (
	VMGuestNotConfigured VMGuestHealth = "not_configured"
	VMGuestStarting      VMGuestHealth = "starting"
	VMGuestHealthy       VMGuestHealth = "healthy"
	VMGuestUnhealthy     VMGuestHealth = "unhealthy"
	VMGuestUnknown       VMGuestHealth = "unknown"
)

type VMOwnershipClass string

const (
	VMOwned            VMOwnershipClass = "owned"
	VMOrphan           VMOwnershipClass = "orphan"
	VMForeign          VMOwnershipClass = "foreign"
	VMOwnershipUnknown VMOwnershipClass = "unknown"
)

// VMResourceIdentity is the exact provider target, not a display/service name.
type VMResourceIdentity struct {
	InstallationID     uuid.UUID        `json:"installation_id"`
	OrgID              uuid.UUID        `json:"org_id"`
	HostID             uuid.UUID        `json:"host_id"`
	DeploymentID       uuid.UUID        `json:"deployment_id"`
	DeploymentUnitID   *uuid.UUID       `json:"deployment_unit_id,omitempty"`
	Provider           VMProvider       `json:"provider"`
	ProviderResourceID uuid.UUID        `json:"provider_resource_id"`
	LifecycleClass     VMLifecycleClass `json:"lifecycle_class"`
}
type VMOwnershipMarker struct {
	SchemaVersion int `json:"schema_version"` // provider metadata version 2
	VMResourceIdentity
	AppliedGeneration int64     `json:"applied_generation"`
	OperationID       uuid.UUID `json:"operation_id"`
	ImageDigest       string    `json:"image_digest"`
	ConfigDigest      string    `json:"config_digest"`
}

type VMBootstrapBinding struct {
	TargetKey string `json:"target_key"`
	// Only ID may be supplied. Resolve metadata and authorize it at admission.
	Ref SecretRef `json:"ref"`
}
type VMConnectionProtocol string

const (
	VMConnectionSSH     VMConnectionProtocol = "ssh"
	VMConnectionRDP     VMConnectionProtocol = "rdp"
	VMConnectionHTTPS   VMConnectionProtocol = "https"
	VMConnectionConsole VMConnectionProtocol = "console"
)

type VMPublicConnection struct {
	Protocol   VMConnectionProtocol `json:"protocol"`
	Address    string               `json:"address,omitempty"`
	Port       uint16               `json:"port,omitempty"`
	Username   string               `json:"username,omitempty"`
	ResourceID uuid.UUID            `json:"resource_id"`
}
type VMNetworkMode string

const (
	VMNetworkIsolated VMNetworkMode = "isolated"
	VMNetworkNAT      VMNetworkMode = "nat"
	VMNetworkBridged  VMNetworkMode = "bridged"
)

type VMNetwork struct {
	Mode                  VMNetworkMode `json:"mode"`
	NetworkRef            *uuid.UUID    `json:"network_ref,omitempty"`
	PassthroughDeviceRefs []uuid.UUID   `json:"passthrough_device_refs"`
}
type VMTPM struct {
	Enabled    bool       `json:"enabled"`
	IdentityID *uuid.UUID `json:"identity_id,omitempty"`
}
type VMAccessPolicy struct {
	ConsoleEnabled bool                   `json:"console_enabled"`
	Protocols      []VMConnectionProtocol `json:"protocols"`
}
type VMMaintenancePolicy struct {
	AutomaticRecovery   bool  `json:"automatic_recovery"`
	MaxRecoveryAttempts int   `json:"max_recovery_attempts"`
	WindowSeconds       int64 `json:"window_seconds"`
}
type VMCheckpointPolicy struct {
	RetainCount   int   `json:"retain_count"`
	MaxAgeSeconds int64 `json:"max_age_seconds"`
}
type PersistentVMDeployment struct {
	ObservationCursor *VMObservationCursor `json:"observation_cursor,omitempty"`
	VirtualizationResourceMeta
	LifecycleClass   VMLifecycleClass     `json:"lifecycle_class"`
	Purpose          VMPurpose            `json:"purpose"`
	Provider         VMProvider           `json:"provider"`
	HostID           uuid.UUID            `json:"host_id"`
	ImageID          uuid.UUID            `json:"image_id"`
	ServiceID        *uuid.UUID           `json:"service_id,omitempty"`
	EnvironmentID    *uuid.UUID           `json:"environment_id,omitempty"`
	DeploymentUnitID *uuid.UUID           `json:"deployment_unit_id,omitempty"`
	DisplayName      string               `json:"display_name"`
	Profile          string               `json:"profile"`
	ProfileRevision  int                  `json:"profile_revision"`
	DesiredPower     VMDesiredPower       `json:"desired_power"`
	Autostart        bool                 `json:"autostart"`
	Allocation       VMCapacity           `json:"allocation"`
	StoragePoolRef   uuid.UUID            `json:"storage_pool_ref"`
	Network          VMNetwork            `json:"network"`
	Firmware         VMFirmware           `json:"firmware"`
	TPM              VMTPM                `json:"tpm"`
	Bootstrap        []VMBootstrapBinding `json:"bootstrap"`
	BootstrapApplied bool                 `json:"bootstrap_applied"`
	Labels           map[string]string    `json:"labels"`
	Identity         VMResourceIdentity   `json:"identity"`
	ConfigDigest     string               `json:"config_digest"`
	Maintenance      VMMaintenancePolicy  `json:"maintenance"`
	CheckpointPolicy VMCheckpointPolicy   `json:"checkpoint_policy"`
	Access           VMAccessPolicy       `json:"access"`
	Connections      []VMPublicConnection `json:"connections"`
	Observation      *VMObservation       `json:"observation,omitempty"`
}

type VMObservation struct {
	Usage *VMResourceUsage `json:"usage,omitempty"`
	VMObservationStamp
	Identity       VMResourceIdentity        `json:"identity"`
	LifecycleClass VMLifecycleClass          `json:"lifecycle_class"`
	Availability   VMObservationAvailability `json:"availability"`
	// Nil means never observed, not absent. Unavailability retains state/time.
	RuntimeState        *VMRuntimeState      `json:"runtime_state,omitempty"`
	RuntimeObservedAt   *time.Time           `json:"runtime_observed_at,omitempty"`
	Drift               VMDrift              `json:"drift"`
	GuestHealth         VMGuestHealth        `json:"guest_health"`
	Ownership           VMOwnershipClass     `json:"ownership"`
	Marker              *VMOwnershipMarker   `json:"marker,omitempty"`
	AppliedImageDigest  string               `json:"applied_image_digest,omitempty"`
	AppliedConfigDigest string               `json:"applied_config_digest,omitempty"`
	Connections         []VMPublicConnection `json:"connections"`
	ConsoleAvailable    bool                 `json:"console_available"`
	Diagnostic          VMDiagnostic         `json:"diagnostic"`
}

type VMArtifactState string

const (
	VMArtifactCreating VMArtifactState = "creating"
	VMArtifactReady    VMArtifactState = "ready"
	VMArtifactFailed   VMArtifactState = "failed"
	VMArtifactDeleting VMArtifactState = "deleting"
	VMArtifactDeleted  VMArtifactState = "deleted"
)

type VMCheckpointConsistency string

const VMCheckpointCold VMCheckpointConsistency = "cold"

type VMCheckpoint struct {
	VirtualizationResourceMeta
	LifecycleClass       VMLifecycleClass        `json:"lifecycle_class"`
	DeploymentID         uuid.UUID               `json:"deployment_id"`
	DeploymentGeneration int64                   `json:"deployment_generation"`
	ImageID              uuid.UUID               `json:"image_id"`
	ImageDigest          string                  `json:"image_digest"`
	ConfigDigest         string                  `json:"config_digest"`
	Identity             VMResourceIdentity      `json:"identity"`
	Consistency          VMCheckpointConsistency `json:"consistency"`
	State                VMArtifactState         `json:"state"`
	Firmware             VMFirmware              `json:"firmware"`
	TPMEnabled           bool                    `json:"tpm_enabled"`
	Components           []VMComponent           `json:"components"`
	ManifestDigest       string                  `json:"manifest_digest,omitempty"`
	RetainUntil          time.Time               `json:"retain_until"`
	RestoreVerifiedAt    *time.Time              `json:"restore_verified_at,omitempty"`
}
type VMExport struct {
	VirtualizationResourceMeta
	LifecycleClass  VMLifecycleClass `json:"lifecycle_class"`
	CheckpointID    uuid.UUID        `json:"checkpoint_id"`
	State           VMArtifactState  `json:"state"`
	ManifestDigest  string           `json:"manifest_digest,omitempty"`
	StorageRef      uuid.UUID        `json:"storage_ref"`
	Components      []VMComponent    `json:"components"`
	Provenance      VMProvenance     `json:"provenance"`
	RetainUntil     time.Time        `json:"retain_until"`
	AccessPolicyRef uuid.UUID        `json:"access_policy_ref"`
}
