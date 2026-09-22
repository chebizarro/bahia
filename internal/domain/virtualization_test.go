package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var vmTestTime = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func vmTestDigest() string { return "sha256:" + strings.Repeat("a", 64) }
func vmTestMeta() VirtualizationResourceMeta {
	return VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: uuid.New(), Generation: 1, CreatedBy: "operator", CreatedAt: vmTestTime, UpdatedAt: vmTestTime}
}
func vmTestProvenance() VMProvenance {
	return VMProvenance{EventID: strings.Repeat("b", 64), Signer: strings.Repeat("c", 64), Verified: true, VerifiedAt: vmTestTime}
}
func vmTestResources() (*VirtualizationHost, *VMImage, *PersistentVMDeployment) {
	h := &VirtualizationHost{VirtualizationResourceMeta: vmTestMeta(), InstallationID: uuid.New(), LifecycleClasses: []VMLifecycleClass{VMLifecyclePersistent, VMLifecycleLoomQEMU}, Provider: VMProviderLibvirt, ExecutionLocation: VMExecutionLocal, TrustPolicyRef: uuid.New(), Enabled: true, Architecture: "amd64", Capacity: VMCapacity{16, 64 << 30, 220 << 30}, Quota: VMCapacity{16, 64 << 30, 220 << 30}, OperationLimits: DefaultVMOperationLimits(), CapacityObservationMaxAgeSeconds: 90}
	i := &VMImage{VirtualizationResourceMeta: vmTestMeta(), ManifestDigest: vmTestDigest(), Format: VMImageQCOW2, Architecture: "amd64", OS: VMOSLinux, LifecycleClasses: []VMLifecycleClass{VMLifecyclePersistent, VMLifecycleLoomQEMU}, Firmware: VMFirmwareBIOS, DriverContract: "virtio-v1", AgentProtocolVersion: "1", AllowedProfiles: []string{"desktop/gnome-dev"}, ReleaseRef: uuid.New(), Provenance: vmTestProvenance(), Components: []VMComponent{{Kind: VMComponentDisk, StorageRef: uuid.New(), Digest: vmTestDigest(), SizeBytes: 100 << 30}}}
	i.OrgID = h.OrgID
	v := &PersistentVMDeployment{VirtualizationResourceMeta: vmTestMeta(), LifecycleClass: VMLifecyclePersistent, Purpose: VMPurposeDesktop, Provider: VMProviderLibvirt, HostID: h.ID, ImageID: i.ID, DisplayName: "desktop", DesiredPower: VMDesiredStopped, Allocation: VMCapacity{8, 24 << 30, 100 << 30}, StoragePoolRef: uuid.New(), Network: VMNetwork{Mode: VMNetworkIsolated, PassthroughDeviceRefs: []uuid.UUID{}}, Firmware: VMFirmwareBIOS, ConfigDigest: vmTestDigest(), Bootstrap: []VMBootstrapBinding{}, Connections: []VMPublicConnection{}, Labels: map[string]string{}, Access: VMAccessPolicy{Protocols: []VMConnectionProtocol{}}}
	v.OrgID = h.OrgID
	v.Identity = VMResourceIdentity{InstallationID: h.InstallationID, OrgID: h.OrgID, HostID: h.ID, DeploymentID: v.ID, Provider: VMProviderLibvirt, ProviderResourceID: uuid.New(), LifecycleClass: VMLifecyclePersistent}
	return h, i, v
}
func vmTestPlane(h *VirtualizationHost, i *VMImage) *ExecutionPlaneDeployment {
	p := &ExecutionPlaneDeployment{VirtualizationResourceMeta: vmTestMeta(), HostID: h.ID, WorkerPubKey: strings.Repeat("e", 64), ManagementEndpointRef: uuid.New(), ManagementAuthor: strings.Repeat("d", 64), Desired: ExecutionPlaneDesired{LifecycleClasses: []VMLifecycleClass{VMLifecycleLoomQEMU}, Package: ExecutionPlanePackagePin{Digest: vmTestDigest(), Version: "1", Provenance: vmTestProvenance()}, Configuration: ExecutionPlaneConfiguration{Revision: vmTestDigest(), Network: VMNetwork{Mode: VMNetworkIsolated}, SecretBindings: []VMBootstrapBinding{}}, ImagePins: []ExecutionPlaneImagePin{{LifecycleClass: VMLifecycleLoomQEMU, ImageID: i.ID, ManifestDigest: i.ManifestDigest}}, ReservedCapacity: VMCapacity{2, 4 << 30, 10 << 30}, Concurrency: 2, ExpectedCapabilities: []ExecutionPlaneCapability{{LifecycleClass: VMLifecycleLoomQEMU, OS: VMOSLinux, Architecture: "amd64", AgentProtocolVersion: "1"}}, State: ExecutionPlaneEnabled, ProbePolicy: DefaultExecutionPlaneProbePolicy()}}
	p.OrgID = h.OrgID
	return p
}
func vmTestObservation(v *PersistentVMDeployment) VMObservation {
	state := VMRuntimeRunning
	at := vmTestTime
	return VMObservation{VMObservationStamp: VMObservationStamp{1, v.Generation, uuid.New(), 1, at}, Identity: v.Identity, LifecycleClass: VMLifecyclePersistent, Availability: VMObservationAvailable, RuntimeState: &state, RuntimeObservedAt: &at, Drift: VMDriftInSync, GuestHealth: VMGuestHealthy, Ownership: VMOwned, Marker: &VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: v.Identity, AppliedGeneration: v.Generation, OperationID: uuid.New(), ImageDigest: vmTestDigest(), ConfigDigest: vmTestDigest()}, Connections: []VMPublicConnection{}}
}
func TestVMControlPlaneContractsAndSerialization(t *testing.T) {
	h, i, v := vmTestResources()
	require.NoError(t, ValidateVMDeploymentReferences(v, h, i))
	p := vmTestPlane(h, i)
	require.NoError(t, ValidateExecutionPlaneDeployment(p))
	c := &VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(), LifecycleClass: VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: i.ID, ImageDigest: i.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: VMCheckpointCold, State: VMArtifactReady, Firmware: VMFirmwareBIOS, Components: i.Components, ManifestDigest: vmTestDigest(), RetainUntil: vmTestTime.Add(time.Hour)}
	c.OrgID = v.OrgID
	e := &VMExport{VirtualizationResourceMeta: vmTestMeta(), LifecycleClass: VMLifecyclePersistent, CheckpointID: c.ID, State: VMArtifactReady, ManifestDigest: c.ManifestDigest, StorageRef: uuid.New(), Components: c.Components, Provenance: vmTestProvenance(), RetainUntil: c.RetainUntil, AccessPolicyRef: uuid.New()}
	e.OrgID = v.OrgID
	require.NoError(t, ValidateVMCheckpoint(c))
	require.NoError(t, ValidateVMExport(e))
	o := &VMOperation{VirtualizationResourceMeta: vmTestMeta(), LifecycleClass: VMLifecyclePersistent, ResourceID: v.ID, ResourceGeneration: 1, ExpectedGeneration: 1, IdempotencyKey: "request-1", RequestHash: vmTestDigest(), Actor: "operator", Reason: "start guest", Kind: VMOperationStart, Phase: VMOperationAccepted, RequiredTier: VMApprovalOperator, ProviderCorrelationID: uuid.New(), Deadline: vmTestTime.Add(time.Minute), PreparedStorageRefs: []uuid.UUID{}}
	require.NoError(t, ValidateVMOperation(o))
	a := &VMApproval{SchemaVersion: 1, ID: uuid.New(), OrgID: v.OrgID, ResourceID: v.ID, LifecycleClass: VMLifecyclePersistent, Generation: 1, RequestHash: vmTestDigest(), ProviderFingerprint: vmTestDigest(), Tier: VMApprovalDestructive, Requester: "requester", Approver: "approver", Reason: "approved restore", CreatedAt: vmTestTime, ExpiresAt: vmTestTime.Add(VMApprovalMaxAge)}
	require.NoError(t, ValidateVMApproval(a))
	for _, value := range []any{h, i, v, p, c, e, o, a} {
		t.Run(reflect.TypeOf(value).Elem().Name(), func(t *testing.T) {
			data, err := json.Marshal(value)
			require.NoError(t, err)
			target := reflect.New(reflect.TypeOf(value).Elem()).Interface()
			require.NoError(t, DecodeVirtualizationDocument(data, target))
			require.Equal(t, value, target)
			require.Contains(t, string(data), `"schema_version":1`)
			require.Contains(t, string(data), `lifecycle_class`)
		})
	}
	for _, class := range []VMLifecycleClass{"", "vm-qemu", "unknown"} {
		require.Error(t, ValidateVMLifecycleClass(class))
	}
	for _, class := range []VMLifecycleClass{VMLifecyclePersistent, VMLifecycleLoomFirecracker, VMLifecycleLoomQEMU} {
		require.NoError(t, ValidateVMLifecycleClass(class))
	}
}
func TestVMControlPlaneRejectsAmbiguousIdentityAndSecrets(t *testing.T) {
	tests := map[string]func(*PersistentVMDeployment){
		"class missing":          func(v *PersistentVMDeployment) { v.LifecycleClass = "" },
		"ephemeral desktop":      func(v *PersistentVMDeployment) { v.LifecycleClass = VMLifecycleLoomQEMU },
		"provider UUID required": func(v *PersistentVMDeployment) { v.Identity.ProviderResourceID = uuid.Nil },
		"host mismatch":          func(v *PersistentVMDeployment) { v.Identity.HostID = uuid.New() },
		"installation required":  func(v *PersistentVMDeployment) { v.Identity.InstallationID = uuid.Nil },
		"unknown desired power":  func(v *PersistentVMDeployment) { v.DesiredPower = "paused" },
		"TPM missing identity":   func(v *PersistentVMDeployment) { v.TPM.Enabled = true },
		"negative capacity":      func(v *PersistentVMDeployment) { v.Allocation.VCPU = -1 },
		"secret metadata": func(v *PersistentVMDeployment) {
			v.Bootstrap = []VMBootstrapBinding{{TargetKey: "password", Ref: SecretRef{ID: uuid.New(), Name: "caller controlled"}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, v := vmTestResources()
			mutate(v)
			require.Error(t, ValidatePersistentVMDeployment(v))
		})
	}
	h, i, v := vmTestResources()
	h.ExecutionLocation = VMExecutionRemote
	require.Error(t, ValidateVirtualizationHost(h))
	h.ManagementEndpointRef = uuid.New()
	require.NoError(t, ValidateVirtualizationHost(h))
	v.Bootstrap = []VMBootstrapBinding{{TargetKey: "password", Ref: SecretRef{ID: uuid.New()}}}
	require.NoError(t, ValidatePersistentVMDeployment(v))
	data, err := json.Marshal(v)
	require.NoError(t, err)
	data = []byte(strings.Replace(string(data), `"target_key":"password"`, `"target_key":"password","value":"plaintext"`, 1))
	require.Error(t, DecodeVirtualizationDocument(data, new(PersistentVMDeployment)))
	_, err = json.Marshal(VMProviderOperation{})
	require.Error(t, err)
	i.Provenance.Verified = false
	require.Error(t, ValidateVMImage(i))
	i.Provenance.Verified = true
	i.OS = VMOSWindows
	require.Error(t, ValidateVMImage(i))
	i.LifecycleClasses = []VMLifecycleClass{VMLifecyclePersistent}
	require.NoError(t, ValidateVMImage(i))
}
func TestVMControlPlanePublicConnections(t *testing.T) {
	id := uuid.New()
	for _, c := range []VMPublicConnection{{VMConnectionSSH, "guest.example", 22, "operator", id}, {VMConnectionRDP, "192.0.2.1", 3389, "", id}, {VMConnectionHTTPS, "2001:db8::1", 443, "", id}, {Protocol: VMConnectionConsole, ResourceID: id}} {
		require.NoError(t, ValidateVMPublicConnection(c))
	}
	for _, address := range []string{"https://guest.example", "user:password@host", "/run/libvirt.sock", "host?token=secret", "host#secret", "-option", "host path", "qemu+ssh://host/system"} {
		require.Error(t, ValidateVMPublicConnection(VMPublicConnection{VMConnectionSSH, address, 22, "", id}), address)
	}
	require.Error(t, ValidateVMPublicConnection(VMPublicConnection{VMConnectionConsole, "host", 1234, "", id}))
}
func TestVMControlPlaneIndependentObservationAxes(t *testing.T) {
	require.NoError(t, ValidateVMResourceUsage(nil))
	usage := &VMResourceUsage{CPUUtilizationRatio: 0.5, MemoryUsedBytes: 1024, Pressure: VMPressureHigh}
	require.NoError(t, ValidateVMResourceUsage(usage))
	usage.CPUUtilizationRatio = 1.1
	require.Error(t, ValidateVMResourceUsage(usage))
	usage.CPUUtilizationRatio = 0.5
	usage.DiskUsedBytes = -1
	require.Error(t, ValidateVMResourceUsage(usage))
	_, _, v := vmTestResources()
	for _, state := range []VMRuntimeState{VMRuntimeAbsent, VMRuntimeStopped, VMRuntimeRunning, VMRuntimePaused, VMRuntimeFailed} {
		for _, drift := range []VMDrift{VMDriftInSync, VMDriftDrifted, VMDriftUnknown} {
			for _, health := range []VMGuestHealth{VMGuestNotConfigured, VMGuestStarting, VMGuestHealthy, VMGuestUnhealthy, VMGuestUnknown} {
				o := vmTestObservation(v)
				o.RuntimeState = &state
				o.Drift = drift
				o.GuestHealth = health
				require.NoError(t, ValidateVMObservation(v, &o))
			}
		}
	}
	o := vmTestObservation(v)
	o.Availability = VMObservationUnavailable
	require.Error(t, ValidateVMObservation(v, &o))
	o.Drift = VMDriftUnknown
	require.NoError(t, ValidateVMObservation(v, &o))
	o.Marker = nil
	require.Error(t, ValidateVMObservation(v, &o))
	o.Ownership = VMForeign
	require.NoError(t, ValidateVMObservation(v, &o))
	o.RuntimeState = nil
	require.Error(t, ValidateVMObservation(v, &o))
	o.RuntimeObservedAt = nil
	require.NoError(t, ValidateVMObservation(v, &o))
	o.ObservedGeneration++
	require.Error(t, ValidateVMObservation(v, &o))
}
func TestVMControlPlaneCoordinatedCheckpointAndFirecracker(t *testing.T) {
	h, i, v := vmTestResources()
	c := &VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(), LifecycleClass: VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: i.ID, ImageDigest: i.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: VMCheckpointCold, State: VMArtifactReady, Firmware: VMFirmwareUEFI, TPMEnabled: true, ManifestDigest: vmTestDigest(), Components: i.Components, RetainUntil: vmTestTime.Add(time.Hour)}
	c.OrgID = v.OrgID
	require.Error(t, ValidateVMCheckpoint(c))
	c.Components = append(c.Components, VMComponent{VMComponentNVRAM, uuid.New(), vmTestDigest(), 1024})
	require.Error(t, ValidateVMCheckpoint(c))
	c.Components = append(c.Components, VMComponent{VMComponentSWTPM, uuid.New(), vmTestDigest(), 1024})
	require.NoError(t, ValidateVMCheckpoint(c))
	c.Consistency = "live"
	require.Error(t, ValidateVMCheckpoint(c))
	h.Provider = VMProviderFirecracker
	h.LifecycleClasses = []VMLifecycleClass{VMLifecyclePersistent, VMLifecycleLoomFirecracker}
	i.Format = VMImageFirecrackerRootFS
	i.Firmware = VMFirmwareNone
	i.LifecycleClasses = h.LifecycleClasses
	i.Components = []VMComponent{{VMComponentKernel, uuid.New(), vmTestDigest(), 1024}, {VMComponentRootFS, uuid.New(), vmTestDigest(), 2048}}
	v.Provider = VMProviderFirecracker
	v.Identity.Provider = VMProviderFirecracker
	v.Firmware = VMFirmwareNone
	v.Purpose = VMPurposeService
	require.NoError(t, ValidateVMDeploymentReferences(v, h, i))
	i.OS = VMOSWindows
	require.Error(t, ValidateVMImage(i))
}
func TestVMControlPlaneGovernanceAndApprovalTransitions(t *testing.T) {
	p := DesktopVMPilotPolicy()
	require.NoError(t, ValidateVMPilotPolicy(p))
	require.Equal(t, int64(220<<30), p.AggregateCeiling.DiskBytes)
	require.NoError(t, ValidateVMPilotAllocation(p, "desktop/windows-dev", 1, VMCapacity{7, 20 << 30, 110 << 30}))
	for _, c := range []VMCapacity{{0, 0, 0}, {9, 20 << 30, 110 << 30}, {7, 15 << 30, 110 << 30}, {7, 20 << 30, 121 << 30}} {
		require.Error(t, ValidateVMPilotAllocation(p, "desktop/windows-dev", 1, c))
	}
	require.False(t, VMOperationTransitionAllowed(VMOperationAccepted, VMOperationSucceeded))
	require.False(t, VMOperationUnconfirmed.Terminal())
	require.True(t, VMOperationTransitionAllowed(VMOperationUnconfirmed, VMOperationVerifying))
	require.False(t, VMOperationTransitionAllowed(VMOperationSucceeded, VMOperationExecuting))
	for _, kind := range []VMOperationKind{VMOperationAdopt, VMOperationExport, VMOperationRestore, VMOperationDelete} {
		require.Equal(t, VMApprovalDestructive, MinimumVMApprovalTier(kind))
	}
	a := &VMApproval{SchemaVersion: 1, ID: uuid.New(), OrgID: uuid.New(), ResourceID: uuid.New(), LifecycleClass: VMLifecyclePersistent, Generation: 1, RequestHash: vmTestDigest(), ProviderFingerprint: vmTestDigest(), Tier: VMApprovalDestructive, Requester: "one", Approver: "two", Reason: "approved", CreatedAt: vmTestTime, ExpiresAt: vmTestTime.Add(VMApprovalMaxAge)}
	require.NoError(t, ValidateVMApproval(a))
	a.Approver = a.Requester
	require.Error(t, ValidateVMApproval(a))
	a.Approver = "two"
	a.ExpiresAt = a.ExpiresAt.Add(time.Second)
	require.Error(t, ValidateVMApproval(a))
}
func TestVMControlPlaneCapabilityEvidence(t *testing.T) {
	h, i, _ := vmTestResources()
	p := vmTestPlane(h, i)
	session := uuid.New()
	stamp := VMObservationStamp{1, 1, session, 1, vmTestTime}
	q := &ExecutionPlaneProbeEvidence{VMObservationStamp: stamp, PlaneID: p.ID, Author: p.ManagementAuthor, LifecycleClasses: p.Desired.LifecycleClasses, Successful: true, PackageDigest: p.Desired.Package.Digest, ConfigRevision: p.Desired.Configuration.Revision, ImagePins: p.Desired.ImagePins, Capabilities: p.Desired.ExpectedCapabilities}
	o := &ExecutionPlaneObservation{VMObservationStamp: stamp, PlaneID: p.ID, HostID: p.HostID, Author: p.ManagementAuthor, LifecycleClasses: p.Desired.LifecycleClasses, Availability: VMObservationAvailable, Drift: VMDriftInSync, PackageDigest: q.PackageDigest, ConfigRevision: q.ConfigRevision, ImagePins: q.ImagePins, ReservedCapacity: p.Desired.ReservedCapacity, Concurrency: p.Desired.Concurrency, State: ExecutionPlaneEnabled, Probe: q}
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime).Capabilities, "expected is not effective")
	p.Observation = o
	require.NoError(t, ValidateExecutionPlaneDeployment(p))
	require.Len(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime).Capabilities, 1)
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, uuid.New(), vmTestTime).Capabilities)
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime.Add(90*time.Second)).Capabilities)
	q.Successful = false
	q.Capabilities = nil
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime).Capabilities)
	q.Successful = true
	q.Capabilities = []ExecutionPlaneCapability{{VMLifecycleLoomQEMU, VMOSWindows, "amd64", "1"}}
	require.Error(t, ValidateExecutionPlaneDeployment(p))
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime).Capabilities)
	q.Capabilities = p.Desired.ExpectedCapabilities
	q.Author = strings.Repeat("e", 64)
	require.Error(t, ValidateExecutionPlaneDeployment(p))
	q.Author = p.ManagementAuthor
	q.ConfigRevision = "sha256:" + strings.Repeat("f", 64)
	require.Empty(t, EffectiveExecutionPlaneCapabilities(p, session, vmTestTime).Capabilities)
}
