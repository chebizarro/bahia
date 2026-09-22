package domain

import (
	"github.com/google/uuid"
	"time"
)

const VirtualizationSchemaVersion = 1

type VMLifecycleClass string

const (
	VMLifecyclePersistent      VMLifecycleClass = "persistent_vm"
	VMLifecycleLoomFirecracker VMLifecycleClass = "loom_firecracker_job_microvm"
	VMLifecycleLoomQEMU        VMLifecycleClass = "loom_qemu_job_domain"
)

type VMProvider string

const (
	VMProviderLibvirt     VMProvider = "libvirt"
	VMProviderFirecracker VMProvider = "firecracker"
)

type VMExecutionLocation string

const (
	VMExecutionLocal  VMExecutionLocation = "local"
	VMExecutionRemote VMExecutionLocation = "remote"
)

// VirtualizationResourceMeta is immutable except Generation and UpdatedAt. Repositories
// assign timestamps; callers provide UUIDs and the authenticated creation identity.
type VirtualizationResourceMeta struct {
	SchemaVersion int       `json:"schema_version"`
	ID            uuid.UUID `json:"id"`
	OrgID         uuid.UUID `json:"org_id"`
	Generation    int64     `json:"generation"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type VMCapacity struct {
	VCPU        int64 `json:"vcpu"`
	MemoryBytes int64 `json:"memory_bytes"`
	DiskBytes   int64 `json:"disk_bytes"`
}

// VMOperationLimits are seconds, not Go duration nanoseconds on the wire.
type VMOperationLimits struct {
	InspectSeconds      int64 `json:"inspect_seconds"`
	MutationSeconds     int64 `json:"mutation_seconds"`
	GracefulStopSeconds int64 `json:"graceful_stop_seconds"`
	TransferSeconds     int64 `json:"transfer_seconds"`
}

func DefaultVMOperationLimits() VMOperationLimits { return VMOperationLimits{15, 120, 120, 1800} }

type VirtualizationHost struct {
	ObservationCursor *VMObservationCursor `json:"observation_cursor,omitempty"`
	PilotPolicy       *VMPilotPolicy       `json:"pilot_policy,omitempty"`
	VirtualizationResourceMeta
	LifecycleClasses  []VMLifecycleClass  `json:"lifecycle_classes"`
	InstallationID    uuid.UUID           `json:"installation_id"`
	Provider          VMProvider          `json:"provider"`
	ExecutionLocation VMExecutionLocation `json:"execution_location"`
	// EndpointRef identifies operator configuration, never a URI or credential.
	ManagementEndpointRef            uuid.UUID                      `json:"management_endpoint_ref"`
	TrustPolicyRef                   uuid.UUID                      `json:"trust_policy_ref"`
	Enabled                          bool                           `json:"enabled"`
	Architecture                     string                         `json:"architecture"`
	Capacity                         VMCapacity                     `json:"capacity"`
	Quota                            VMCapacity                     `json:"quota"`
	OperationLimits                  VMOperationLimits              `json:"operation_limits"`
	CapacityObservationMaxAgeSeconds int64                          `json:"capacity_observation_max_age_seconds"`
	Observation                      *VirtualizationHostObservation `json:"observation,omitempty"`
}

type VMObservationAvailability string

const (
	VMObservationAvailable   VMObservationAvailability = "available"
	VMObservationUnavailable VMObservationAvailability = "unavailable"
)

type VMDrift string

const (
	VMDriftInSync  VMDrift = "in_sync"
	VMDriftDrifted VMDrift = "drifted"
	VMDriftUnknown VMDrift = "unknown"
)

// VMObservationCursor is read-only repository fencing state. It remains readable
// after rotation even when no observation has yet been accepted in the session.
type VMObservationCursor struct {
	SessionID uuid.UUID `json:"session_id"`
	Sequence  int64     `json:"sequence"`
}

// Observation sequence is monotonically increasing within a separately installed
// session. A new observation cannot install its own session or resurrect an old one.
type VMObservationStamp struct {
	SchemaVersion      int       `json:"schema_version"`
	ObservedGeneration int64     `json:"observed_generation"`
	SessionID          uuid.UUID `json:"session_id"`
	Sequence           int64     `json:"sequence"`
	ObservedAt         time.Time `json:"observed_at"`
}

// VMResourceUsage is optional measured data, not inferred from desired allocation.
// Cumulative I/O counters are scoped to the observation session.
type VMResourceUsage struct {
	CPUUtilizationRatio float64            `json:"cpu_utilization_ratio"`
	MemoryUsedBytes     int64              `json:"memory_used_bytes"`
	DiskUsedBytes       int64              `json:"disk_used_bytes"`
	DiskReadBytes       int64              `json:"disk_read_bytes"`
	DiskWrittenBytes    int64              `json:"disk_written_bytes"`
	Pressure            VMResourcePressure `json:"pressure"`
}
type VMResourcePressure string

const (
	VMPressureNormal  VMResourcePressure = "normal"
	VMPressureHigh    VMResourcePressure = "high"
	VMPressureUnknown VMResourcePressure = "unknown"
)

type VirtualizationHostObservation struct {
	Usage *VMResourceUsage `json:"usage,omitempty"`
	VMObservationStamp
	LifecycleClasses   []VMLifecycleClass        `json:"lifecycle_classes"`
	Availability       VMObservationAvailability `json:"availability"`
	Free               VMCapacity                `json:"free"`
	KVMVersion         string                    `json:"kvm_version"`
	LibvirtVersion     string                    `json:"libvirt_version"`
	FirecrackerVersion string                    `json:"firecracker_version"`
	NUMANodes          int                       `json:"numa_nodes"`
	IOMMU              bool                      `json:"iommu"`
	StoragePools       []string                  `json:"storage_pools"`
	Networks           []string                  `json:"networks"`
	Diagnostic         VMDiagnostic              `json:"diagnostic"`
}

type VirtualizationResourceKind string

const (
	VirtualizationHostResource VirtualizationResourceKind = "host"
	VMImageResource            VirtualizationResourceKind = "image"
	PersistentVMResource       VirtualizationResourceKind = "persistent_vm"
	ExecutionPlaneResource     VirtualizationResourceKind = "execution_plane"
	VMCheckpointResource       VirtualizationResourceKind = "checkpoint"
	VMExportResource           VirtualizationResourceKind = "export"
	VMOperationResource        VirtualizationResourceKind = "operation"
)

type VMCapacityReservation struct {
	ID             uuid.UUID                  `json:"id"`
	OrgID          uuid.UUID                  `json:"org_id"`
	HostID         uuid.UUID                  `json:"host_id"`
	ResourceID     uuid.UUID                  `json:"resource_id"`
	ResourceKind   VirtualizationResourceKind `json:"resource_kind"`
	LifecycleClass VMLifecycleClass           `json:"lifecycle_class"`
	Capacity       VMCapacity                 `json:"capacity"`
	CreatedAt      time.Time                  `json:"created_at"`
}

// Profiles are opt-in governed policy, never host defaults. Concrete Windows
// allocations must be chosen by the caller within the approved range.
type VMPilotProfile struct {
	Name           string           `json:"name"`
	Revision       int              `json:"revision"`
	LifecycleClass VMLifecycleClass `json:"lifecycle_class"`
	Minimum        VMCapacity       `json:"minimum"`
	Maximum        VMCapacity       `json:"maximum"`
}
type VMPilotPolicy struct {
	Revision         int              `json:"revision"`
	Profiles         []VMPilotProfile `json:"profiles"`
	AggregateCeiling VMCapacity       `json:"aggregate_ceiling"`
}

func DesktopVMPilotPolicy() VMPilotPolicy {
	const gib = int64(1 << 30)
	return VMPilotPolicy{Revision: 1, Profiles: []VMPilotProfile{
		{"desktop/gnome-dev", 1, VMLifecyclePersistent, VMCapacity{8, 24 * gib, 100 * gib}, VMCapacity{8, 24 * gib, 100 * gib}},
		{"desktop/windows-dev", 1, VMLifecyclePersistent, VMCapacity{6, 16 * gib, 100 * gib}, VMCapacity{8, 24 * gib, 120 * gib}},
	}, AggregateCeiling: VMCapacity{16, 64 * gib, 220 * gib}}
}
