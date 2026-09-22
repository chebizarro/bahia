package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"
)

// VMAdoptionMeasurement is provider-produced evidence, never caller authority.
// StorageKey is an opaque host file identity; no private paths leave the driver.
type VMAdoptionComponent struct {
	VMComponent
	StorageKey   string `json:"storage_key"`
	SourceDigest string `json:"source_digest"`
}

type VMAdoptionMeasurement struct {
	ProviderEvidence    []string              `json:"provider_evidence,omitempty"`
	SchemaVersion       int                   `json:"schema_version"`
	Identity            VMResourceIdentity    `json:"identity"`
	Generation          int64                 `json:"generation"`
	ImageID             uuid.UUID             `json:"image_id"`
	ImageDigest         string                `json:"image_digest"`
	ConfigDigest        string                `json:"config_digest"`
	ProviderFingerprint string                `json:"provider_fingerprint"`
	StoragePoolRef      uuid.UUID             `json:"storage_pool_ref"`
	Components          []VMAdoptionComponent `json:"components"`
	Digest              string                `json:"digest"`
}

// VMAdoptionProvider is additive: providers without measurement cannot enroll.
type VMAdoptionProvider interface {
	MeasureAdoption(context.Context, VMChangeRequest) (*VMAdoptionMeasurement, error)
}

func vmAdoptionHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// VMAdoptionConfigDigest binds the modeled hardware to the COMPLETE measured
// definition. This includes unknown provider details in the fingerprint rather
// than asserting that a caller-selected pin describes the inspected machine.
func VMAdoptionConfigDigest(v PersistentVMDeployment, fingerprint string) string {
	if len(v.Network.PassthroughDeviceRefs) == 0 {
		v.Network.PassthroughDeviceRefs = []uuid.UUID{}
	}
	return vmAdoptionHash(struct {
		Fingerprint string
		Allocation  VMCapacity
		Firmware    VMFirmware
		TPM         VMTPM
		Network     VMNetwork
		Autostart   bool
	}{fingerprint, v.Allocation, v.Firmware, v.TPM, v.Network, v.Autostart})
}

func VMAdoptionDigest(m VMAdoptionMeasurement) string {
	m.Digest = ""
	return vmAdoptionHash(m)
}

func ValidateVMAdoptionMeasurement(m *VMAdoptionMeasurement) error {
	if m == nil || m.SchemaVersion != 1 || ValidateVMResourceIdentity(m.Identity) != nil || m.Generation < 1 || vmUUIDs(m.ImageID, m.StoragePoolRef) != nil || vmDigest(m.ImageDigest) != nil || vmDigest(m.ConfigDigest) != nil || vmDigest(m.ProviderFingerprint) != nil || m.Digest != VMAdoptionDigest(*m) || len(m.Components) == 0 {
		return vmInvalid("adoption measurement")
	}
	for _, digest := range m.ProviderEvidence {
		if vmDigest(digest) != nil {
			return vmInvalid("adoption provider evidence")
		}
	}
	seen := map[VMComponentKind]bool{}
	refs := map[uuid.UUID]bool{}
	keys := map[string]bool{}
	for _, c := range m.Components {
		if !vmOneOf(c.Kind, VMComponentDisk, VMComponentRootFS, VMComponentKernel, VMComponentNVRAM, VMComponentSWTPM) || seen[c.Kind] || refs[c.StorageRef] || keys[c.StorageKey] || c.StorageRef == uuid.Nil || c.SizeBytes < 0 || vmDigest(c.Digest) != nil || vmDigest(c.StorageKey) != nil || vmDigest(c.SourceDigest) != nil {
			return vmInvalid("adoption component")
		}
		seen[c.Kind] = true
		refs[c.StorageRef], keys[c.StorageKey] = true, true
	}
	if m.Identity.Provider == VMProviderLibvirt && (!seen[VMComponentDisk] || seen[VMComponentKernel] || seen[VMComponentRootFS]) {
		return vmInvalid("libvirt adoption components")
	}
	if m.Identity.Provider == VMProviderFirecracker && (len(seen) != 2 || !seen[VMComponentKernel] || !seen[VMComponentRootFS]) {
		return vmInvalid("firecracker adoption components")
	}
	return nil
}
