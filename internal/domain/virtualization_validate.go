package domain

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"math"
	"net"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
)

func vmInvalid(field string) error                    { return fmt.Errorf("%w: %s", ErrInvalidValue, field) }
func vmOneOf[T comparable](value T, values ...T) bool { return slices.Contains(values, value) }
func ValidateVMLifecycleClass(c VMLifecycleClass) error {
	if !vmOneOf(c, VMLifecyclePersistent, VMLifecycleLoomFirecracker, VMLifecycleLoomQEMU) {
		return vmInvalid("lifecycle_class")
	}
	return nil
}
func vmClasses(classes []VMLifecycleClass, ephemeral bool) error {
	if len(classes) == 0 {
		return vmInvalid("lifecycle_classes required")
	}
	seen := map[VMLifecycleClass]bool{}
	for _, c := range classes {
		if ValidateVMLifecycleClass(c) != nil || seen[c] || (ephemeral && c == VMLifecyclePersistent) {
			return vmInvalid("lifecycle_classes")
		}
		seen[c] = true
	}
	return nil
}
func vmDigest(s string) error {
	if s == "" {
		return vmInvalid("required sha256 digest")
	}
	return ValidateImageDigest(s)
}
func vmHex(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func vmText(s string, max int) bool {
	return strings.TrimSpace(s) == s && len(s) > 0 && len(s) <= max && !strings.ContainsAny(s, "\n\r\x00")
}
func vmToken(s string) bool {
	if !vmText(s, 128) {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._/-", r) {
			return false
		}
	}
	return true
}
func vmUUIDs(ids ...uuid.UUID) error {
	for _, id := range ids {
		if err := ValidateRequiredUUID(id, "virtualization identity"); err != nil {
			return err
		}
	}
	return nil
}
func vmOptionalIDs(ids ...*uuid.UUID) error {
	for _, id := range ids {
		if id != nil && *id == uuid.Nil {
			return vmInvalid("optional UUID")
		}
	}
	return nil
}
func ValidateVMCapacity(c VMCapacity, positive bool) error {
	if c.VCPU < 0 || c.MemoryBytes < 0 || c.DiskBytes < 0 || (positive && (c.VCPU == 0 || c.MemoryBytes == 0 || c.DiskBytes == 0)) {
		return vmInvalid("capacity")
	}
	return nil
}
func (c VMCapacity) Fits(limit VMCapacity) bool {
	return c.VCPU <= limit.VCPU && c.MemoryBytes <= limit.MemoryBytes && c.DiskBytes <= limit.DiskBytes
}
func ValidateVirtualizationMeta(m VirtualizationResourceMeta) error {
	if err := vmUUIDs(m.ID, m.OrgID); err != nil {
		return err
	}
	if m.SchemaVersion != VirtualizationSchemaVersion || m.Generation < 1 || !vmText(m.CreatedBy, 128) {
		return vmInvalid("resource metadata")
	}
	return nil
}
func ValidateVirtualizationHost(h *VirtualizationHost) error {
	if h == nil {
		return vmInvalid("host")
	}
	if err := ValidateVirtualizationMeta(h.VirtualizationResourceMeta); err != nil {
		return err
	}
	if err := vmClasses(h.LifecycleClasses, false); err != nil {
		return err
	}
	if err := vmUUIDs(h.InstallationID, h.TrustPolicyRef); err != nil {
		return err
	}
	if !vmOneOf(h.Provider, VMProviderLibvirt, VMProviderFirecracker) || !vmOneOf(h.ExecutionLocation, VMExecutionLocal, VMExecutionRemote) || !vmOneOf(h.Architecture, "amd64", "arm64") {
		return vmInvalid("host provider/location/architecture")
	}
	if h.ExecutionLocation == VMExecutionRemote && h.ManagementEndpointRef == uuid.Nil {
		return vmInvalid("remote management endpoint required")
	}
	for _, c := range h.LifecycleClasses {
		if (h.Provider == VMProviderLibvirt && c == VMLifecycleLoomFirecracker) || (h.Provider == VMProviderFirecracker && c == VMLifecycleLoomQEMU) {
			return vmInvalid("host lifecycle/provider")
		}
	}
	if ValidateVMCapacity(h.Capacity, true) != nil || ValidateVMCapacity(h.Quota, true) != nil || !h.Quota.Fits(h.Capacity) {
		return vmInvalid("host capacity/quota")
	}
	if h.CapacityObservationMaxAgeSeconds < 1 || h.CapacityObservationMaxAgeSeconds > 3600 {
		return vmInvalid("capacity observation freshness policy")
	}
	l := h.OperationLimits
	if l.InspectSeconds <= 0 || l.MutationSeconds <= 0 || l.GracefulStopSeconds <= 0 || l.TransferSeconds <= 0 || l.InspectSeconds > 300 || l.MutationSeconds > 3600 || l.GracefulStopSeconds > 3600 || l.TransferSeconds > 86400 {
		return vmInvalid("operation limits")
	}
	if h.PilotPolicy != nil {
		if err := ValidateVMPilotPolicy(*h.PilotPolicy); err != nil {
			return err
		}
		if !h.Quota.Fits(h.PilotPolicy.AggregateCeiling) {
			return vmInvalid("pilot aggregate ceiling")
		}
	}
	if h.Observation != nil {
		return ValidateVirtualizationHostObservation(h, h.Observation)
	}
	return nil
}
func vmProvenance(p VMProvenance) error {
	if !vmHex(p.EventID) || !vmHex(p.Signer) || !p.Verified || p.VerifiedAt.IsZero() {
		return vmInvalid("verified provenance required")
	}
	return nil
}
func vmComponents(cs []VMComponent, complete bool, firmware VMFirmware, tpm bool, provider VMProvider) error {
	if complete && len(cs) == 0 {
		return vmInvalid("component manifest required")
	}
	seen := map[VMComponentKind]bool{}
	refs := map[uuid.UUID]bool{}
	for _, c := range cs {
		if !vmOneOf(c.Kind, VMComponentDisk, VMComponentKernel, VMComponentRootFS, VMComponentNVRAM, VMComponentSWTPM) || seen[c.Kind] || refs[c.StorageRef] || c.StorageRef == uuid.Nil || c.SizeBytes <= 0 || vmDigest(c.Digest) != nil {
			return vmInvalid("component")
		}
		seen[c.Kind] = true
		refs[c.StorageRef] = true
	}
	if complete {
		if provider == VMProviderLibvirt && !seen[VMComponentDisk] {
			return vmInvalid("disk required")
		}
		if provider == VMProviderFirecracker && (!seen[VMComponentRootFS] || !seen[VMComponentKernel]) {
			return vmInvalid("kernel/rootfs required")
		}
		if firmware == VMFirmwareUEFI && !seen[VMComponentNVRAM] {
			return vmInvalid("NVRAM required")
		}
		if tpm && !seen[VMComponentSWTPM] {
			return vmInvalid("swtpm state required")
		}
	}
	if (firmware != VMFirmwareUEFI && seen[VMComponentNVRAM]) || (!tpm && seen[VMComponentSWTPM]) || (provider == VMProviderFirecracker && (seen[VMComponentDisk] || seen[VMComponentNVRAM] || seen[VMComponentSWTPM])) {
		return vmInvalid("incompatible component")
	}
	return nil
}
func ValidateVMImage(v *VMImage) error {
	if v == nil {
		return vmInvalid("image")
	}
	if err := ValidateVirtualizationMeta(v.VirtualizationResourceMeta); err != nil {
		return err
	}
	if vmDigest(v.ManifestDigest) != nil || vmProvenance(v.Provenance) != nil || v.ReleaseRef == uuid.Nil || vmClasses(v.LifecycleClasses, false) != nil {
		return vmInvalid("image identity/provenance")
	}
	if !vmOneOf(v.Architecture, "amd64", "arm64") || !vmOneOf(v.OS, VMOSLinux, VMOSWindows) || !vmToken(v.AgentProtocolVersion) || !vmToken(v.DriverContract) {
		return vmInvalid("image guest contract")
	}
	provider := VMProviderLibvirt
	switch v.Format {
	case VMImageQCOW2:
		if !vmOneOf(v.Firmware, VMFirmwareBIOS, VMFirmwareUEFI) || slices.Contains(v.LifecycleClasses, VMLifecycleLoomFirecracker) {
			return vmInvalid("qcow2 compatibility")
		}
	case VMImageFirecrackerRootFS:
		provider = VMProviderFirecracker
		if v.OS != VMOSLinux || v.Firmware != VMFirmwareNone || slices.Contains(v.LifecycleClasses, VMLifecycleLoomQEMU) {
			return vmInvalid("firecracker compatibility")
		}
	default:
		return vmInvalid("image format")
	}
	if v.OS == VMOSWindows && !reflect.DeepEqual(v.LifecycleClasses, []VMLifecycleClass{VMLifecyclePersistent}) {
		return vmInvalid("Windows ephemeral image forbidden")
	}
	for _, p := range v.AllowedProfiles {
		if !vmToken(p) {
			return vmInvalid("profile")
		}
	}
	return vmComponents(v.Components, true, v.Firmware, false, provider)
}
func ValidateVMResourceIdentity(i VMResourceIdentity) error {
	if err := vmUUIDs(i.InstallationID, i.OrgID, i.HostID, i.DeploymentID, i.ProviderResourceID); err != nil {
		return err
	}
	if i.LifecycleClass != VMLifecyclePersistent || !vmOneOf(i.Provider, VMProviderLibvirt, VMProviderFirecracker) {
		return vmInvalid("persistent identity")
	}
	return vmOptionalIDs(i.DeploymentUnitID)
}
func ValidateVMOwnershipMarker(m VMOwnershipMarker) error {
	if ValidateVMResourceIdentity(m.VMResourceIdentity) != nil || m.SchemaVersion != 2 || m.AppliedGeneration < 1 || m.OperationID == uuid.Nil || vmDigest(m.ImageDigest) != nil || vmDigest(m.ConfigDigest) != nil {
		return vmInvalid("ownership marker")
	}
	return nil
}
func ValidateVMBootstrapBindings(bs []VMBootstrapBinding) error {
	seen := map[string]bool{}
	for _, b := range bs {
		if !vmToken(b.TargetKey) || seen[b.TargetKey] || b.Ref.ID == uuid.Nil {
			return vmInvalid("bootstrap binding")
		}
		seen[b.TargetKey] = true
		if !reflect.DeepEqual(b.Ref, SecretRef{ID: b.Ref.ID}) {
			return vmInvalid("bootstrap reference metadata must be resolved, not supplied")
		}
	}
	return nil
}
func ValidateVMPublicConnection(c VMPublicConnection) error {
	if c.ResourceID == uuid.Nil || !vmOneOf(c.Protocol, VMConnectionSSH, VMConnectionRDP, VMConnectionHTTPS, VMConnectionConsole) {
		return vmInvalid("connection")
	}
	if c.Protocol == VMConnectionConsole {
		if c.Address != "" || c.Port != 0 || c.Username != "" {
			return vmInvalid("console must be an authorized resource action")
		}
		return nil
	}
	if c.Port == 0 || !vmText(c.Address, 253) || strings.ContainsAny(c.Address, "/@?#%\\ \t") {
		return vmInvalid("connection address")
	}
	if net.ParseIP(c.Address) == nil {
		for _, label := range strings.Split(c.Address, ".") {
			if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return vmInvalid("connection hostname")
			}
			for _, r := range label {
				switch {
				case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
				default:
					return vmInvalid("connection hostname")
				}
			}
		}
	}
	if c.Username != "" {
		for _, r := range c.Username {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			default:
				return vmInvalid("connection username")
			}
		}
		if len(c.Username) > 64 {
			return vmInvalid("connection username")
		}
	}
	return nil
}
func vmNetwork(n VMNetwork) error {
	if !vmOneOf(n.Mode, VMNetworkIsolated, VMNetworkNAT, VMNetworkBridged) || vmOptionalIDs(n.NetworkRef) != nil {
		return vmInvalid("network")
	}
	if (n.Mode == VMNetworkIsolated && n.NetworkRef != nil) || (n.Mode != VMNetworkIsolated && n.NetworkRef == nil) {
		return vmInvalid("network reference")
	}
	return vmUUIDs(n.PassthroughDeviceRefs...)
}
func ValidatePersistentVMDeployment(v *PersistentVMDeployment) error {
	if v == nil {
		return vmInvalid("deployment")
	}
	if err := ValidateVirtualizationMeta(v.VirtualizationResourceMeta); err != nil {
		return err
	}
	if v.LifecycleClass != VMLifecyclePersistent || !vmOneOf(v.Purpose, VMPurposeDesktop, VMPurposeService) || !vmOneOf(v.DesiredPower, VMDesiredStopped, VMDesiredRunning) || !vmOneOf(v.Provider, VMProviderLibvirt, VMProviderFirecracker) {
		return vmInvalid("deployment class/purpose/provider/power")
	}
	if vmUUIDs(v.HostID, v.ImageID, v.StoragePoolRef) != nil || vmOptionalIDs(v.ServiceID, v.EnvironmentID, v.DeploymentUnitID) != nil {
		return vmInvalid("deployment references")
	}
	if (v.ServiceID == nil) != (v.EnvironmentID == nil) || (v.DeploymentUnitID != nil && v.EnvironmentID == nil) {
		return vmInvalid("service/environment targeting")
	}
	if ValidateVMResourceIdentity(v.Identity) != nil || v.Identity.OrgID != v.OrgID || v.Identity.HostID != v.HostID || v.Identity.DeploymentID != v.ID || v.Identity.Provider != v.Provider || !reflect.DeepEqual(v.Identity.DeploymentUnitID, v.DeploymentUnitID) {
		return vmInvalid("deployment ownership identity")
	}
	if ValidateVMCapacity(v.Allocation, true) != nil || vmDigest(v.ConfigDigest) != nil || vmNetwork(v.Network) != nil || ValidateVMBootstrapBindings(v.Bootstrap) != nil {
		return vmInvalid("deployment configuration")
	}
	if v.TPM.Enabled != (v.TPM.IdentityID != nil) || vmOptionalIDs(v.TPM.IdentityID) != nil || (v.TPM.Enabled && v.Firmware != VMFirmwareUEFI) {
		return vmInvalid("TPM identity")
	}
	if v.Provider == VMProviderFirecracker {
		if v.Firmware != VMFirmwareNone || v.TPM.Enabled || v.Purpose == VMPurposeDesktop {
			return vmInvalid("firecracker persistent configuration")
		}
	} else if !vmOneOf(v.Firmware, VMFirmwareBIOS, VMFirmwareUEFI) {
		return vmInvalid("firmware")
	}
	if !vmText(v.DisplayName, 128) || v.CheckpointPolicy.RetainCount < 0 || v.CheckpointPolicy.MaxAgeSeconds < 0 || v.Maintenance.MaxRecoveryAttempts < 0 || v.Maintenance.WindowSeconds < 0 || (v.Maintenance.AutomaticRecovery && (v.Maintenance.MaxRecoveryAttempts == 0 || v.Maintenance.WindowSeconds == 0)) {
		return vmInvalid("deployment policy")
	}
	if (v.Profile == "") != (v.ProfileRevision == 0) || v.ProfileRevision < 0 {
		return vmInvalid("profile revision")
	}
	for k, val := range v.Labels {
		if !vmToken(k) || !vmToken(val) {
			return vmInvalid("public labels")
		}
	}
	for _, p := range v.Access.Protocols {
		if !vmOneOf(p, VMConnectionSSH, VMConnectionRDP, VMConnectionHTTPS, VMConnectionConsole) {
			return vmInvalid("access protocol")
		}
	}
	for _, c := range v.Connections {
		if ValidateVMPublicConnection(c) != nil || c.ResourceID != v.ID {
			return vmInvalid("deployment connection")
		}
	}
	if v.Observation != nil {
		return ValidateVMObservation(v, v.Observation)
	}
	return nil
}
func ValidateVMDeploymentReferences(v *PersistentVMDeployment, h *VirtualizationHost, i *VMImage) error {
	if ValidatePersistentVMDeployment(v) != nil || ValidateVirtualizationHost(h) != nil || ValidateVMImage(i) != nil {
		return vmInvalid("deployment references invalid")
	}
	if h.ID != v.HostID || i.ID != v.ImageID || h.OrgID != v.OrgID || i.OrgID != v.OrgID || !h.Enabled || h.Provider != v.Provider || h.InstallationID != v.Identity.InstallationID || h.Architecture != i.Architecture || !slices.Contains(h.LifecycleClasses, v.LifecycleClass) || !slices.Contains(i.LifecycleClasses, v.LifecycleClass) || i.Firmware != v.Firmware {
		return vmInvalid("deployment reference mismatch")
	}
	if (v.Provider == VMProviderLibvirt) != (i.Format == VMImageQCOW2) {
		return vmInvalid("image/provider mismatch")
	}
	if h.PilotPolicy != nil {
		if err := ValidateVMPilotAllocation(*h.PilotPolicy, v.Profile, v.ProfileRevision, v.Allocation); err != nil {
			return err
		}
	}
	if v.Profile != "" && !slices.Contains(i.AllowedProfiles, v.Profile) {
		return vmInvalid("image profile not allowed")
	}
	return nil
}
func ValidateVMObservationStamp(s VMObservationStamp) error {
	if s.SchemaVersion != 1 || s.ObservedGeneration < 1 || s.Sequence < 1 || s.SessionID == uuid.Nil || s.ObservedAt.IsZero() {
		return vmInvalid("observation stamp")
	}
	return nil
}
func vmDiagnostic(d VMDiagnostic) error {
	if d.Code != "" && !vmOneOf(d.Code, VMErrorInvalid, VMErrorConflict, VMErrorUnavailable, VMErrorUnsupported, VMErrorForeign, VMErrorApprovalRequired, VMErrorQuota, VMErrorUnconfirmed, VMErrorIntegrity) {
		return vmInvalid("diagnostic code")
	}
	if d.EvidenceDigest != "" {
		return vmDigest(d.EvidenceDigest)
	}
	return nil
}
func vmObservationAxes(a VMObservationAvailability, d VMDrift) error {
	if !vmOneOf(a, VMObservationAvailable, VMObservationUnavailable) || !vmOneOf(d, VMDriftInSync, VMDriftDrifted, VMDriftUnknown) || (a == VMObservationUnavailable && d != VMDriftUnknown) {
		return vmInvalid("observation axes")
	}
	return nil
}
func ValidateVMResourceUsage(u *VMResourceUsage) error {
	if u == nil {
		return nil
	}
	if math.IsNaN(u.CPUUtilizationRatio) || math.IsInf(u.CPUUtilizationRatio, 0) || u.CPUUtilizationRatio < 0 || u.CPUUtilizationRatio > 1 || u.MemoryUsedBytes < 0 || u.DiskUsedBytes < 0 || u.DiskReadBytes < 0 || u.DiskWrittenBytes < 0 || !vmOneOf(u.Pressure, VMPressureNormal, VMPressureHigh, VMPressureUnknown) {
		return vmInvalid("resource usage")
	}
	return nil
}
func ValidateVMObservation(v *PersistentVMDeployment, o *VMObservation) error {
	if v == nil || o == nil || ValidateVMObservationStamp(o.VMObservationStamp) != nil || o.ObservedGeneration != v.Generation || o.LifecycleClass != v.LifecycleClass || !reflect.DeepEqual(o.Identity, v.Identity) || ValidateVMResourceUsage(o.Usage) != nil || vmObservationAxes(o.Availability, o.Drift) != nil || vmDiagnostic(o.Diagnostic) != nil {
		return vmInvalid("VM observation")
	}
	if !vmOneOf(o.GuestHealth, VMGuestNotConfigured, VMGuestStarting, VMGuestHealthy, VMGuestUnhealthy, VMGuestUnknown) || !vmOneOf(o.Ownership, VMOwned, VMOrphan, VMForeign, VMOwnershipUnknown) {
		return vmInvalid("health/ownership")
	}
	if (o.RuntimeState == nil) != (o.RuntimeObservedAt == nil) || (o.Availability == VMObservationAvailable && o.RuntimeState == nil) {
		return vmInvalid("runtime observation required")
	}
	if o.RuntimeState != nil && (!vmOneOf(*o.RuntimeState, VMRuntimeAbsent, VMRuntimeStopped, VMRuntimeRunning, VMRuntimePaused, VMRuntimeFailed) || o.RuntimeObservedAt.IsZero() || o.RuntimeObservedAt.After(o.ObservedAt)) {
		return vmInvalid("runtime state/time")
	}
	if o.Marker != nil {
		if ValidateVMOwnershipMarker(*o.Marker) != nil || !reflect.DeepEqual(o.Marker.VMResourceIdentity, o.Identity) || o.Marker.AppliedGeneration > v.Generation {
			return vmInvalid("observation marker")
		}
	}
	if vmOneOf(o.Ownership, VMOwned, VMOrphan) && o.Marker == nil {
		return vmInvalid("ownership requires proof")
	}
	for _, d := range []string{o.AppliedConfigDigest, o.AppliedImageDigest} {
		if d != "" && vmDigest(d) != nil {
			return vmInvalid("applied digest")
		}
	}
	for _, c := range o.Connections {
		if ValidateVMPublicConnection(c) != nil || c.ResourceID != v.ID {
			return vmInvalid("observed connection")
		}
	}
	return nil
}
func ValidateVirtualizationHostObservation(h *VirtualizationHost, o *VirtualizationHostObservation) error {
	if h == nil || o == nil || ValidateVMObservationStamp(o.VMObservationStamp) != nil || o.ObservedGeneration != h.Generation || !reflect.DeepEqual(o.LifecycleClasses, h.LifecycleClasses) || !vmOneOf(o.Availability, VMObservationAvailable, VMObservationUnavailable) || ValidateVMCapacity(o.Free, false) != nil || !o.Free.Fits(h.Capacity) || o.NUMANodes < 0 || ValidateVMResourceUsage(o.Usage) != nil || vmDiagnostic(o.Diagnostic) != nil {
		return vmInvalid("host observation")
	}
	return nil
}
func vmArtifactState(s VMArtifactState) bool {
	return vmOneOf(s, VMArtifactCreating, VMArtifactReady, VMArtifactFailed, VMArtifactDeleting, VMArtifactDeleted)
}
func ValidateVMCheckpoint(c *VMCheckpoint) error {
	if c == nil || ValidateVirtualizationMeta(c.VirtualizationResourceMeta) != nil || c.LifecycleClass != VMLifecyclePersistent || vmUUIDs(c.DeploymentID, c.ImageID) != nil || c.DeploymentGeneration < 1 || vmDigest(c.ImageDigest) != nil || vmDigest(c.ConfigDigest) != nil || ValidateVMResourceIdentity(c.Identity) != nil || c.Identity.DeploymentID != c.DeploymentID || c.Identity.OrgID != c.OrgID || c.Consistency != VMCheckpointCold || !vmArtifactState(c.State) {
		return vmInvalid("checkpoint")
	}
	if !vmOneOf(c.Firmware, VMFirmwareBIOS, VMFirmwareUEFI, VMFirmwareNone) || (c.TPMEnabled && c.Firmware != VMFirmwareUEFI) || (c.Identity.Provider == VMProviderFirecracker) != (c.Firmware == VMFirmwareNone) {
		return vmInvalid("checkpoint firmware")
	}
	if c.State == VMArtifactReady && (vmDigest(c.ManifestDigest) != nil || c.RetainUntil.IsZero()) {
		return vmInvalid("ready checkpoint manifest/retention")
	}
	if c.ManifestDigest != "" && vmDigest(c.ManifestDigest) != nil {
		return vmInvalid("checkpoint digest")
	}
	return vmComponents(c.Components, c.State == VMArtifactReady, c.Firmware, c.TPMEnabled, c.Identity.Provider)
}
func ValidateVMExport(e *VMExport) error {
	if e == nil || ValidateVirtualizationMeta(e.VirtualizationResourceMeta) != nil || e.LifecycleClass != VMLifecyclePersistent || vmUUIDs(e.CheckpointID, e.StorageRef, e.AccessPolicyRef) != nil || !vmArtifactState(e.State) {
		return vmInvalid("export")
	}
	if e.State == VMArtifactReady && (vmDigest(e.ManifestDigest) != nil || vmProvenance(e.Provenance) != nil || len(e.Components) == 0 || e.RetainUntil.IsZero()) {
		return vmInvalid("ready export")
	}
	if e.ManifestDigest != "" && vmDigest(e.ManifestDigest) != nil {
		return vmInvalid("export digest")
	}
	seen := map[VMComponentKind]bool{}
	for _, c := range e.Components {
		if seen[c.Kind] {
			return vmInvalid("duplicate export component")
		}
		seen[c.Kind] = true
		if c.StorageRef == uuid.Nil || c.SizeBytes <= 0 || vmDigest(c.Digest) != nil || !vmOneOf(c.Kind, VMComponentDisk, VMComponentKernel, VMComponentRootFS, VMComponentNVRAM, VMComponentSWTPM) {
			return vmInvalid("export component")
		}
	}
	return nil
}
func ValidateVMOperation(o *VMOperation) error {
	if o == nil || ValidateVirtualizationMeta(o.VirtualizationResourceMeta) != nil || o.LifecycleClass != VMLifecyclePersistent || o.ResourceID == uuid.Nil || o.ResourceGeneration < 1 || o.ExpectedGeneration < 0 || o.ExpectedGeneration > o.ResourceGeneration || !vmText(o.IdempotencyKey, 256) || vmDigest(o.RequestHash) != nil || !vmText(o.Actor, 128) || !vmText(o.Reason, 512) || SanitizeEvidence(o.Reason) != o.Reason || o.ProviderCorrelationID == uuid.Nil || o.Deadline.IsZero() {
		return vmInvalid("operation")
	}
	if !vmOneOf(o.Kind, VMOperationDefine, VMOperationAdopt, VMOperationStart, VMOperationGracefulStop, VMOperationReboot, VMOperationCheckpoint, VMOperationExport, VMOperationClone, VMOperationRestore, VMOperationDelete) || !vmOneOf(o.Phase, VMOperationAccepted, VMOperationAwaitingApproval, VMOperationExecuting, VMOperationVerifying, VMOperationSucceeded, VMOperationFailed, VMOperationCancelled, VMOperationUnconfirmed) || o.RequiredTier < MinimumVMApprovalTier(o.Kind) || o.RequiredTier > VMApprovalDestructive {
		return vmInvalid("operation kind/phase/tier")
	}
	if o.RequiredTier == VMApprovalDestructive && vmDigest(o.ProviderFingerprint) != nil {
		return vmInvalid("approved provider fingerprint required")
	}
	if o.Adoption != nil && (o.Kind != VMOperationAdopt || ValidateVMAdoptionMeasurement(o.Adoption) != nil || o.Adoption.Identity.OrgID != o.OrgID || o.Adoption.Identity.DeploymentID != o.ResourceID || o.Adoption.Generation != o.ResourceGeneration || o.Adoption.ProviderFingerprint != o.ProviderFingerprint) {
		return vmInvalid("operation adoption evidence")
	}
	if vmOptionalIDs(o.CheckpointID, o.ExportID, o.CloneTargetID) != nil {
		return vmInvalid("operation artifact references")
	}
	if (vmOneOf(o.Kind, VMOperationCheckpoint, VMOperationExport, VMOperationClone, VMOperationRestore) && o.CheckpointID == nil) || (o.Kind == VMOperationExport && o.ExportID == nil) || (o.Kind == VMOperationClone && o.CloneTargetID == nil) {
		return vmInvalid("operation source/target required")
	}
	if o.Kind == VMOperationDelete {
		if !vmOneOf(o.DeleteTarget, VMDeleteDeployment, VMDeleteCheckpoint, VMDeleteExport) || (o.DeleteTarget == VMDeleteCheckpoint && o.CheckpointID == nil) || (o.DeleteTarget == VMDeleteExport && o.ExportID == nil) {
			return vmInvalid("explicit delete target required")
		}
	} else if o.DeleteTarget != "" {
		return vmInvalid("delete target on non-delete operation")
	}
	if !o.DataDisposition.Valid() || (o.DataDisposition != "" && (o.Kind != VMOperationDelete || o.DeleteTarget != VMDeleteDeployment)) {
		return vmInvalid("deployment data disposition")
	}
	if o.DataDisposition == VMDataDelete && o.RequiredTier != VMApprovalDestructive {
		return vmInvalid("data deletion requires destructive approval")
	}
	if o.AllowForceStop && (o.Kind != VMOperationDelete || o.DeleteTarget != VMDeleteDeployment || o.RequiredTier != VMApprovalDestructive) {
		return vmInvalid("force stop must be explicit tier-2 delete")
	}
	if o.Plan != nil {
		p := o.Plan
		if p.LifecycleClass != o.LifecycleClass || p.ExpectedGeneration != o.ExpectedGeneration || p.RequiredTier > o.RequiredTier || vmDigest(p.CurrentConfigDigest) != nil || vmDigest(p.DesiredConfigDigest) != nil || p.RequiredTier < VMApprovalOperator || p.RequiredTier > VMApprovalDestructive || (p.RecreateRequired && o.RequiredTier != VMApprovalDestructive) {
			return vmInvalid("operation change plan")
		}
		for _, c := range p.Changes {
			if !vmToken(c.Field) || !vmToken(c.ReasonCode) || !vmOneOf(c.Class, VMChangeSafeMutable, VMChangeLifecycle, VMChangeRequiresStopped, VMChangeRecreateRequired) || (c.Class == VMChangeRecreateRequired && !p.RecreateRequired) {
				return vmInvalid("field change")
			}
		}
	}
	if o.Phase.Terminal() != (o.CompletedAt != nil) || vmOptionalIDs(o.ApprovalID) != nil || vmUUIDs(o.PreparedStorageRefs...) != nil {
		return vmInvalid("operation completion/references")
	}
	return vmDiagnostic(o.Outcome)
}
func ValidateVMApproval(a *VMApproval) error {
	if a == nil || a.SchemaVersion != 1 || vmUUIDs(a.ID, a.OrgID, a.ResourceID) != nil || a.LifecycleClass != VMLifecyclePersistent || a.Generation < 1 || vmDigest(a.RequestHash) != nil || vmDigest(a.ProviderFingerprint) != nil || a.Tier != VMApprovalDestructive || !vmText(a.Requester, 128) || !vmText(a.Approver, 128) || a.Requester == a.Approver || !vmText(a.Reason, 512) || SanitizeEvidence(a.Reason) != a.Reason || a.CreatedAt.IsZero() || !a.ExpiresAt.After(a.CreatedAt) || a.ExpiresAt.Sub(a.CreatedAt) > VMApprovalMaxAge {
		return vmInvalid("approval")
	}
	if a.AdoptionDigest != "" && vmDigest(a.AdoptionDigest) != nil {
		return vmInvalid("approval adoption evidence")
	}
	return nil
}
func ValidateExecutionPlaneDeployment(p *ExecutionPlaneDeployment) error {
	if p == nil || ValidateVirtualizationMeta(p.VirtualizationResourceMeta) != nil || vmUUIDs(p.HostID, p.ManagementEndpointRef) != nil || !vmHex(p.WorkerPubKey) || !vmHex(p.ManagementAuthor) {
		return vmInvalid("execution plane")
	}
	d := p.Desired
	if vmClasses(d.LifecycleClasses, true) != nil || vmDigest(d.Package.Digest) != nil || !vmToken(d.Package.Version) || vmProvenance(d.Package.Provenance) != nil || vmDigest(d.Configuration.Revision) != nil || vmNetwork(d.Configuration.Network) != nil || ValidateVMBootstrapBindings(d.Configuration.SecretBindings) != nil || ValidateVMCapacity(d.ReservedCapacity, true) != nil || d.Concurrency < 1 || !vmOneOf(d.State, ExecutionPlaneEnabled, ExecutionPlaneDisabled) || d.ProbePolicy.IntervalSeconds < 1 || d.ProbePolicy.IntervalSeconds > 3600 || d.ProbePolicy.FreshnessSeconds <= d.ProbePolicy.IntervalSeconds || d.ProbePolicy.FreshnessSeconds > 10800 {
		return vmInvalid("execution plane desired state")
	}
	if err := vmPlanePins(d.ImagePins, d.LifecycleClasses); err != nil {
		return err
	}
	if err := vmPlaneCapabilities(d.ExpectedCapabilities, d.LifecycleClasses); err != nil {
		return err
	}
	if p.Observation != nil {
		return ValidateExecutionPlaneObservation(p, p.Observation)
	}
	return nil
}
func vmPlanePins(pins []ExecutionPlaneImagePin, classes []VMLifecycleClass) error {
	if len(pins) != len(classes) {
		return vmInvalid("one image pin per lifecycle class required")
	}
	seen := map[VMLifecycleClass]bool{}
	for _, p := range pins {
		if !slices.Contains(classes, p.LifecycleClass) || seen[p.LifecycleClass] || p.ImageID == uuid.Nil || vmDigest(p.ManifestDigest) != nil {
			return vmInvalid("plane image pin")
		}
		seen[p.LifecycleClass] = true
	}
	return nil
}
func vmPlaneCapabilities(cs []ExecutionPlaneCapability, classes []VMLifecycleClass) error {
	seen := map[ExecutionPlaneCapability]bool{}
	for _, c := range cs {
		if !slices.Contains(classes, c.LifecycleClass) || c.LifecycleClass == VMLifecyclePersistent || c.OS != VMOSLinux || !vmOneOf(c.Architecture, "amd64", "arm64") || !vmToken(c.AgentProtocolVersion) || seen[c] {
			return vmInvalid("ephemeral capability; Windows is forbidden")
		}
		seen[c] = true
	}
	return nil
}
func ValidateExecutionPlaneObservation(p *ExecutionPlaneDeployment, o *ExecutionPlaneObservation) error {
	if p == nil || o == nil || ValidateVMObservationStamp(o.VMObservationStamp) != nil || o.ObservedGeneration != p.Generation || o.PlaneID != p.ID || o.HostID != p.HostID || o.Author != p.ManagementAuthor || !reflect.DeepEqual(o.LifecycleClasses, p.Desired.LifecycleClasses) || vmObservationAxes(o.Availability, o.Drift) != nil || vmDiagnostic(o.Diagnostic) != nil || ValidateVMCapacity(o.ReservedCapacity, false) != nil || o.Concurrency < 0 || !vmOneOf(o.State, ExecutionPlaneEnabled, ExecutionPlaneDisabled) {
		return vmInvalid("plane observation")
	}
	if o.Availability == VMObservationAvailable && (vmDigest(o.PackageDigest) != nil || vmDigest(o.ConfigRevision) != nil || vmPlanePins(o.ImagePins, o.LifecycleClasses) != nil) {
		return vmInvalid("observed plane pins")
	}
	if o.Probe != nil {
		q := o.Probe
		if ValidateVMObservationStamp(q.VMObservationStamp) != nil || q.PlaneID != p.ID || q.Author != p.ManagementAuthor || q.ObservedGeneration != p.Generation || q.SessionID != o.SessionID || q.Sequence > o.Sequence || q.ObservedAt.After(o.ObservedAt) || !reflect.DeepEqual(q.LifecycleClasses, o.LifecycleClasses) || vmDiagnostic(q.Diagnostic) != nil || vmPlaneCapabilities(q.Capabilities, o.LifecycleClasses) != nil {
			return vmInvalid("probe evidence")
		}
		if q.Successful && (vmDigest(q.PackageDigest) != nil || vmDigest(q.ConfigRevision) != nil || vmPlanePins(q.ImagePins, q.LifecycleClasses) != nil) {
			return vmInvalid("probe pins")
		}
		if !q.Successful && len(q.Capabilities) != 0 {
			return vmInvalid("failed probe cannot grant capabilities")
		}
	}
	return nil
}

// EffectiveExecutionPlaneCapabilities never infers capability from expectations.
// Scheduling/capacity admission remain the worker selector's separate gates.
func EffectiveExecutionPlaneCapabilities(p *ExecutionPlaneDeployment, session uuid.UUID, now time.Time) VerifiedExecutionPlaneCapabilities {
	out := VerifiedExecutionPlaneCapabilities{Capabilities: []ExecutionPlaneCapability{}}
	if ValidateExecutionPlaneDeployment(p) != nil || p.Desired.State != ExecutionPlaneEnabled || p.Observation == nil {
		return out
	}
	o := p.Observation
	q := o.Probe
	if o.Availability != VMObservationAvailable || o.State != ExecutionPlaneEnabled || o.Draining || o.Drift != VMDriftInSync || q == nil || !q.Successful || q.SessionID != session || q.ObservedAt.After(now) || !now.Before(q.ObservedAt.Add(time.Duration(p.Desired.ProbePolicy.FreshnessSeconds)*time.Second)) || q.PackageDigest != p.Desired.Package.Digest || q.ConfigRevision != p.Desired.Configuration.Revision || o.PackageDigest != q.PackageDigest || o.ConfigRevision != q.ConfigRevision || !reflect.DeepEqual(q.ImagePins, p.Desired.ImagePins) || !reflect.DeepEqual(o.ImagePins, q.ImagePins) || o.ReservedCapacity != p.Desired.ReservedCapacity || o.Concurrency != p.Desired.Concurrency {
		return out
	}
	out.PlaneID = p.ID
	out.Generation = p.Generation
	out.SessionID = session
	out.ProbeSequence = q.Sequence
	out.ExpiresAt = q.ObservedAt.Add(time.Duration(p.Desired.ProbePolicy.FreshnessSeconds) * time.Second)
	for _, c := range q.Capabilities {
		if slices.Contains(p.Desired.ExpectedCapabilities, c) {
			out.Capabilities = append(out.Capabilities, c)
		}
	}
	return out
}

func ValidateVMPilotPolicy(p VMPilotPolicy) error {
	if p.Revision < 1 || len(p.Profiles) == 0 || ValidateVMCapacity(p.AggregateCeiling, true) != nil {
		return vmInvalid("pilot policy")
	}
	seen := map[string]bool{}
	for _, profile := range p.Profiles {
		if !vmToken(profile.Name) || seen[profile.Name] || profile.Revision < 1 || profile.LifecycleClass != VMLifecyclePersistent || ValidateVMCapacity(profile.Minimum, true) != nil || !profile.Minimum.Fits(profile.Maximum) || !profile.Maximum.Fits(p.AggregateCeiling) {
			return vmInvalid("pilot profile")
		}
		seen[profile.Name] = true
	}
	return nil
}
func ValidateVMPilotAllocation(p VMPilotPolicy, name string, revision int, allocation VMCapacity) error {
	if ValidateVMPilotPolicy(p) != nil || ValidateVMCapacity(allocation, true) != nil {
		return vmInvalid("pilot allocation")
	}
	for _, profile := range p.Profiles {
		if profile.Name == name && profile.Revision == revision && profile.Minimum.Fits(allocation) && allocation.Fits(profile.Maximum) {
			return nil
		}
	}
	return vmInvalid("allocation outside approved profile revision")
}

// DecodeVirtualizationDocument is the strict schema boundary for stored and wire
// documents. Unknown fields (including plaintext bootstrap values) fail closed.
func DecodeVirtualizationDocument(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return vmInvalid("resource document must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return vmInvalid("trailing JSON")
	}
	return nil
}
