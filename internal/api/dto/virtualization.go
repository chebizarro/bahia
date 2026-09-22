package dto

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// VirtualizationResource is the shared REST/ContextVM/canonical public view.
// Do not embed persisted resources: they contain private bootstrap, storage and
// management configuration. Each detail below is an explicit public allowlist.
type VirtualizationResource struct {
	SchemaVersion    int                               `json:"schema_version"`
	Actor            string                            `json:"actor,omitempty"`
	ID               uuid.UUID                         `json:"id"`
	OrgID            uuid.UUID                         `json:"org_id"`
	Generation       int64                             `json:"generation"`
	Kind             domain.VirtualizationResourceKind `json:"resource_kind"`
	LifecycleClasses []domain.VMLifecycleClass         `json:"lifecycle_classes"`
	UpdatedAt        time.Time                         `json:"updated_at"`
	Deleted          bool                              `json:"deleted"`
	Host             *VirtualizationHost               `json:"host,omitempty"`
	Image            *VMImage                          `json:"image,omitempty"`
	VM               *PersistentVM                     `json:"vm,omitempty"`
	Plane            *ExecutionPlane                   `json:"plane,omitempty"`
	Checkpoint       *VMCheckpoint                     `json:"checkpoint,omitempty"`
	Export           *VMExport                         `json:"export,omitempty"`
	Operation        *VMOperation                      `json:"operation,omitempty"`
}
type VirtualizationHost struct {
	Provider    domain.VMProvider  `json:"provider"`
	Enabled     bool               `json:"enabled"`
	Capacity    domain.VMCapacity  `json:"capacity"`
	Quota       domain.VMCapacity  `json:"quota"`
	Free        *domain.VMCapacity `json:"free,omitempty"`
	Observation *VMObservation     `json:"observation,omitempty"`
}
type VMImage struct {
	ManifestDigest     string                   `json:"manifest_digest"`
	Format             domain.VMImageFormat     `json:"format"`
	OS                 domain.VMOperatingSystem `json:"os"`
	ProvenanceVerified bool                     `json:"provenance_verified"`
	Components         []VMComponent            `json:"components"`
}
type VMComponent struct {
	Kind      domain.VMComponentKind `json:"kind"`
	Digest    string                 `json:"digest"`
	SizeBytes int64                  `json:"size_bytes"`
}
type PersistentVM struct {
	HostID       uuid.UUID                   `json:"host_id"`
	ImageID      uuid.UUID                   `json:"image_id"`
	Provider     domain.VMProvider           `json:"provider"`
	Purpose      domain.VMPurpose            `json:"purpose"`
	DesiredPower domain.VMDesiredPower       `json:"desired_power"`
	Allocation   domain.VMCapacity           `json:"allocation"`
	Connections  []domain.VMPublicConnection `json:"connections"`
	Observation  *VMObservation              `json:"observation,omitempty"`
}
type VMObservation struct {
	ObservedGeneration int64                            `json:"observed_generation"`
	Sequence           int64                            `json:"sequence"`
	ObservedAt         time.Time                        `json:"observed_at"`
	Availability       domain.VMObservationAvailability `json:"availability"`
	RuntimeState       *domain.VMRuntimeState           `json:"runtime_state,omitempty"`
	RuntimeObservedAt  *time.Time                       `json:"runtime_observed_at,omitempty"`
	Drift              domain.VMDrift                   `json:"drift,omitempty"`
	GuestHealth        domain.VMGuestHealth             `json:"guest_health,omitempty"`
	Ownership          domain.VMOwnershipClass          `json:"ownership,omitempty"`
	ConsoleAvailable   bool                             `json:"console_available"`
	Connections        []domain.VMPublicConnection      `json:"connections,omitempty"`
	Usage              *domain.VMResourceUsage          `json:"usage,omitempty"`
	Diagnostic         domain.VMErrorCode               `json:"diagnostic,omitempty"`
}
type ExecutionPlane struct {
	HostID           uuid.UUID                  `json:"host_id"`
	State            domain.ExecutionPlaneState `json:"desired_state"`
	PackageDigest    string                     `json:"package_digest"`
	ConfigRevision   string                     `json:"config_revision"`
	ReservedCapacity domain.VMCapacity          `json:"reserved_capacity"`
	Concurrency      int                        `json:"concurrency"`
	Observation      *VMObservation             `json:"observation,omitempty"`
	ProbeSuccessful  bool                       `json:"probe_successful"`
	ProbeObservedAt  *time.Time                 `json:"probe_observed_at,omitempty"`
	// Probe success is evidence, not an effective capability grant. D owns expiry
	// and eligibility; desired capabilities must never be advertised as observed.
}
type VMCheckpoint struct {
	DeploymentID      uuid.UUID              `json:"deployment_id"`
	State             domain.VMArtifactState `json:"state"`
	ManifestDigest    string                 `json:"manifest_digest"`
	Components        []VMComponent          `json:"components"`
	RetainUntil       time.Time              `json:"retain_until"`
	RestoreVerifiedAt *time.Time             `json:"restore_verified_at,omitempty"`
}
type VMExport struct {
	CheckpointID   uuid.UUID              `json:"checkpoint_id"`
	State          domain.VMArtifactState `json:"state"`
	ManifestDigest string                 `json:"manifest_digest"`
	Components     []VMComponent          `json:"components"`
	RetainUntil    time.Time              `json:"retain_until"`
}
type VMOperation struct {
	ResourceID         uuid.UUID               `json:"resource_id"`
	ResourceGeneration int64                   `json:"resource_generation"`
	Kind               domain.VMOperationKind  `json:"kind"`
	Phase              domain.VMOperationPhase `json:"phase"`
	RequiredTier       domain.VMApprovalTier   `json:"required_tier"`
	ApprovalID         *uuid.UUID              `json:"approval_id,omitempty"`
	CorrelationID      uuid.UUID               `json:"correlation_id"`
	Diagnostic         domain.VMErrorCode      `json:"diagnostic,omitempty"`
	CompletedAt        *time.Time              `json:"completed_at,omitempty"`
}

func VirtualizationCoordinate(kind domain.VirtualizationResourceKind, id uuid.UUID) (string, error) {
	prefix := map[domain.VirtualizationResourceKind]string{
		domain.VirtualizationHostResource: "virtualization-host", domain.VMImageResource: "vm-image", domain.PersistentVMResource: "persistent-vm", domain.ExecutionPlaneResource: "execution-plane", domain.VMCheckpointResource: "vm-checkpoint", domain.VMExportResource: "vm-export", domain.VMOperationResource: "vm-operation",
	}[kind]
	if prefix == "" || id == uuid.Nil {
		return "", domain.ErrInvalidValue
	}
	return prefix + ":" + id.String(), nil
}

var publicVMActor = regexp.MustCompile(`^[0-9a-f]{64}$`)

func vmActor(s string) string {
	if publicVMActor.MatchString(s) {
		return s
	}
	return ""
}

var publicVMDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func vmDigest(s string) string {
	if publicVMDigest.MatchString(s) {
		return s
	}
	return ""
}
func vmComponents(in []domain.VMComponent) []VMComponent {
	out := make([]VMComponent, 0, len(in))
	for _, c := range in {
		switch c.Kind {
		case domain.VMComponentDisk, domain.VMComponentKernel, domain.VMComponentRootFS, domain.VMComponentNVRAM, domain.VMComponentSWTPM:
			out = append(out, VMComponent{c.Kind, vmDigest(c.Digest), c.SizeBytes})
		}
	}
	return out
}
func vmConnections(in []domain.VMPublicConnection, id uuid.UUID) []domain.VMPublicConnection {
	out := make([]domain.VMPublicConnection, 0, len(in))
	for _, c := range in {
		if c.ResourceID == id && domain.ValidateVMPublicConnection(c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// PublicVMDiagnostic strips the cause, evidence and unknown codes at every output boundary.
func PublicVMDiagnostic(c domain.VMErrorCode) domain.VMErrorCode {
	switch c {
	case "", domain.VMErrorInvalid, domain.VMErrorConflict, domain.VMErrorUnavailable, domain.VMErrorUnsupported, domain.VMErrorForeign, domain.VMErrorApprovalRequired, domain.VMErrorQuota, domain.VMErrorUnconfirmed, domain.VMErrorIntegrity:
		return c
	}
	return domain.VMErrorUnavailable
}
func publicStamp(s domain.VMObservationStamp, a domain.VMObservationAvailability) *VMObservation {
	return &VMObservation{ObservedGeneration: s.ObservedGeneration, Sequence: s.Sequence, ObservedAt: s.ObservedAt, Availability: a}
}

// PublicVirtualizationDocument converts a journal document (plus its separate
// observation column) using exactly the same allowlist as interactive queries.
func PublicVirtualizationDocument(kind domain.VirtualizationResourceKind, document, observation json.RawMessage) (*VirtualizationResource, error) {
	var meta domain.VirtualizationResourceMeta
	if err := json.Unmarshal(document, &meta); err != nil {
		return nil, domain.ErrInvalidValue
	}
	if meta.SchemaVersion != domain.VirtualizationSchemaVersion || meta.ID == uuid.Nil || meta.OrgID == uuid.Nil || meta.Generation < 1 {
		return nil, domain.ErrInvalidValue
	}
	out := &VirtualizationResource{SchemaVersion: meta.SchemaVersion, ID: meta.ID, OrgID: meta.OrgID, Generation: meta.Generation, Kind: kind, UpdatedAt: meta.UpdatedAt, Actor: vmActor(meta.CreatedBy)}
	decode := func(v any) error {
		if err := json.Unmarshal(document, v); err != nil {
			return domain.ErrInvalidValue
		}
		return nil
	}
	switch kind {
	case domain.VirtualizationHostResource:
		var v domain.VirtualizationHost
		if err := decode(&v); err != nil {
			return nil, err
		}
		if len(observation) > 0 {
			if err := json.Unmarshal(observation, &v.Observation); err != nil {
				return nil, domain.ErrInvalidValue
			}
		}
		out.LifecycleClasses = v.LifecycleClasses
		out.Host = &VirtualizationHost{Provider: v.Provider, Enabled: v.Enabled, Capacity: v.Capacity, Quota: v.Quota}
		if o := v.Observation; o != nil {
			out.Host.Free = &o.Free
			out.Host.Observation = publicStamp(o.VMObservationStamp, o.Availability)
			out.Host.Observation.Usage = o.Usage
			out.Host.Observation.Diagnostic = PublicVMDiagnostic(o.Diagnostic.Code)
		}
	case domain.VMImageResource:
		var v domain.VMImage
		if err := decode(&v); err != nil {
			return nil, err
		}
		out.LifecycleClasses = v.LifecycleClasses
		out.Image = &VMImage{vmDigest(v.ManifestDigest), v.Format, v.OS, v.Provenance.Verified, vmComponents(v.Components)}
	case domain.PersistentVMResource:
		var v domain.PersistentVMDeployment
		if err := decode(&v); err != nil {
			return nil, err
		}
		if len(observation) > 0 {
			if err := json.Unmarshal(observation, &v.Observation); err != nil {
				return nil, domain.ErrInvalidValue
			}
		}
		out.LifecycleClasses = []domain.VMLifecycleClass{v.LifecycleClass}
		out.VM = &PersistentVM{HostID: v.HostID, ImageID: v.ImageID, Provider: v.Provider, Purpose: v.Purpose, DesiredPower: v.DesiredPower, Allocation: v.Allocation, Connections: vmConnections(v.Connections, v.ID)}
		if o := v.Observation; o != nil {
			p := publicStamp(o.VMObservationStamp, o.Availability)
			p.RuntimeState = o.RuntimeState
			p.RuntimeObservedAt = o.RuntimeObservedAt
			p.Drift = o.Drift
			p.GuestHealth = o.GuestHealth
			p.Ownership = o.Ownership
			p.ConsoleAvailable = o.ConsoleAvailable
			p.Connections = vmConnections(o.Connections, v.ID)
			p.Usage = o.Usage
			p.Diagnostic = PublicVMDiagnostic(o.Diagnostic.Code)
			out.VM.Observation = p
		}
	case domain.ExecutionPlaneResource:
		var v domain.ExecutionPlaneDeployment
		if err := decode(&v); err != nil {
			return nil, err
		}
		if len(observation) > 0 {
			if err := json.Unmarshal(observation, &v.Observation); err != nil {
				return nil, domain.ErrInvalidValue
			}
		}
		out.LifecycleClasses = v.Desired.LifecycleClasses
		out.Plane = &ExecutionPlane{HostID: v.HostID, State: v.Desired.State, PackageDigest: vmDigest(v.Desired.Package.Digest), ConfigRevision: vmDigest(v.Desired.Configuration.Revision), ReservedCapacity: v.Desired.ReservedCapacity, Concurrency: v.Desired.Concurrency}
		if o := v.Observation; o != nil {
			out.Plane.Observation = publicStamp(o.VMObservationStamp, o.Availability)
			out.Plane.Observation.Drift = o.Drift
			out.Plane.Observation.Diagnostic = PublicVMDiagnostic(o.Diagnostic.Code)
			if o.Probe != nil {
				out.Plane.ProbeSuccessful = o.Probe.Successful
				out.Plane.ProbeObservedAt = &o.Probe.ObservedAt
			}
		}
	case domain.VMCheckpointResource:
		var v domain.VMCheckpoint
		if err := decode(&v); err != nil {
			return nil, err
		}
		out.LifecycleClasses = []domain.VMLifecycleClass{v.LifecycleClass}
		out.Deleted = v.State == domain.VMArtifactDeleted
		out.Checkpoint = &VMCheckpoint{v.DeploymentID, v.State, vmDigest(v.ManifestDigest), vmComponents(v.Components), v.RetainUntil, v.RestoreVerifiedAt}
	case domain.VMExportResource:
		var v domain.VMExport
		if err := decode(&v); err != nil {
			return nil, err
		}
		out.LifecycleClasses = []domain.VMLifecycleClass{v.LifecycleClass}
		out.Deleted = v.State == domain.VMArtifactDeleted
		out.Export = &VMExport{v.CheckpointID, v.State, vmDigest(v.ManifestDigest), vmComponents(v.Components), v.RetainUntil}
	case domain.VMOperationResource:
		var v domain.VMOperation
		if err := decode(&v); err != nil {
			return nil, err
		}
		out.LifecycleClasses = []domain.VMLifecycleClass{v.LifecycleClass}
		out.Actor = vmActor(v.Actor)
		out.Operation = &VMOperation{v.ResourceID, v.ResourceGeneration, v.Kind, v.Phase, v.RequiredTier, v.ApprovalID, v.ProviderCorrelationID, PublicVMDiagnostic(v.Outcome.Code), v.CompletedAt}
	default:
		return nil, domain.ErrInvalidValue
	}
	if len(out.LifecycleClasses) == 0 {
		return nil, domain.ErrInvalidValue
	}
	for _, c := range out.LifecycleClasses {
		if c != domain.VMLifecyclePersistent && c != domain.VMLifecycleLoomFirecracker && c != domain.VMLifecycleLoomQEMU {
			return nil, domain.ErrInvalidValue
		}
	}
	if !validPublicVMEnums(out) {
		return nil, domain.ErrInvalidValue
	}
	return out, nil
}
func publicVMEnum[T ~string](v T, allowed ...string) bool {
	for _, a := range allowed {
		if string(v) == a {
			return true
		}
	}
	return false
}
func validPublicVMEnums(v *VirtualizationResource) bool {
	var observation *VMObservation
	if h := v.Host; h != nil {
		if !publicVMEnum(h.Provider, "libvirt", "firecracker") {
			return false
		}
		observation = h.Observation
	}
	if i := v.Image; i != nil {
		if !publicVMEnum(i.Format, "qcow2", "firecracker-rootfs") || !publicVMEnum(i.OS, "linux", "windows") {
			return false
		}
	}
	if vm := v.VM; vm != nil {
		if !publicVMEnum(vm.Provider, "libvirt", "firecracker") || !publicVMEnum(vm.Purpose, "desktop", "service") || !publicVMEnum(vm.DesiredPower, "running", "stopped") || len(v.LifecycleClasses) != 1 || v.LifecycleClasses[0] != domain.VMLifecyclePersistent {
			return false
		}
		observation = vm.Observation
	}
	if p := v.Plane; p != nil {
		if !publicVMEnum(p.State, "enabled", "disabled") {
			return false
		}
		for _, c := range v.LifecycleClasses {
			if c == domain.VMLifecyclePersistent {
				return false
			}
		}
		observation = p.Observation
	}
	if c := v.Checkpoint; c != nil {
		if !publicVMEnum(c.State, "creating", "ready", "failed", "deleting", "deleted") {
			return false
		}
	}
	if e := v.Export; e != nil {
		if !publicVMEnum(e.State, "creating", "ready", "failed", "deleting", "deleted") {
			return false
		}
	}
	if o := v.Operation; o != nil {
		if !publicVMEnum(o.Kind, "define", "adopt", "start", "graceful_stop", "reboot", "checkpoint", "export", "clone", "restore", "delete") || !publicVMEnum(o.Phase, "accepted", "awaiting_approval", "executing", "verifying", "succeeded", "failed", "cancelled", "unconfirmed") {
			return false
		}
	}
	if o := observation; o != nil {
		if !publicVMEnum(o.Availability, "available", "unavailable") || !publicVMEnum(o.Drift, "", "unknown", "in_sync", "drifted") || !publicVMEnum(o.GuestHealth, "", "not_configured", "starting", "healthy", "unhealthy", "unknown") || !publicVMEnum(o.Ownership, "", "owned", "orphan", "foreign", "unknown") {
			return false
		}
		if o.RuntimeState != nil && !publicVMEnum(*o.RuntimeState, "absent", "stopped", "running", "paused", "failed") {
			return false
		}
		if o.Usage != nil && !publicVMEnum(o.Usage.Pressure, "normal", "high", "unknown") {
			return false
		}
	}
	return true
}
func PublicVirtualizationResource(kind domain.VirtualizationResourceKind, resource any) (*VirtualizationResource, error) {
	b, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("invalid virtualization resource")
	}
	return PublicVirtualizationDocument(kind, b, nil)
}
