package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (d *Driver) MeasurePersistent(ctx context.Context, r *vm.PersistentResource, want domain.PersistentVMDeployment, release *vm.Release) (*vm.AdoptionProof, error) {
	if err := d.recheckPersistent(ctx, r); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(d.instanceDir(r.ID.String()), vmConfigFileName))
	if err != nil {
		return nil, err
	}
	var canonical any
	if err = json.Unmarshal(data, &canonical); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(canonical)
	if err != nil || vm.DigestBytes(normalized) != r.Fingerprint {
		return nil, vm.ProviderError(domain.VMErrorConflict, err)
	}
	var cfg vmConfigFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cfg); err != nil {
		return nil, vm.ProviderError(domain.VMErrorUnsupported, err)
	}
	if want.Firmware != domain.VMFirmwareNone || want.TPM.Enabled || want.Autostart || want.Network.Mode != domain.VMNetworkIsolated || want.Network.NetworkRef != nil || len(want.Network.PassthroughDeviceRefs) != 0 || want.Allocation.VCPU != int64(cfg.MachineConfig.VcpuCount) || want.Allocation.MemoryBytes != int64(cfg.MachineConfig.MemSizeMib)<<20 || cfg.MachineConfig.Smt || cfg.BootSource.BootArgs != d.cfg.KernelArgs || len(cfg.Drives) != 1 || cfg.Drives[0].DriveID != "rootfs" || !cfg.Drives[0].IsRootDevice || cfg.Drives[0].IsReadOnly || cfg.Drives[0].PathOnHost != r.Components[domain.VMComponentRootFS] || cfg.BootSource.KernelImagePath != r.Components[domain.VMComponentKernel] {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if cfg.Vsock != nil && (cfg.Vsock.GuestCID < 3 || cfg.Vsock.UDSPath != filepath.Join(d.instanceDir(r.ID.String()), vsockSocketFileName)) {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	for kind, source := range map[domain.VMComponentKind]string{domain.VMComponentKernel: release.KernelPath, domain.VMComponentRootFS: release.RootFSPath} {
		actual, err := vm.MeasureAdoptionFile(ctx, r.Components[kind])
		if err != nil {
			return nil, err
		}
		base, err := vm.MeasureAdoptionFile(ctx, source)
		if err != nil {
			return nil, err
		}
		// A raw writable copy has no independently verifiable ancestry. Require a
		// trusted snapshot of its current bytes, not a legacy image_digest claim.
		if actual.Digest != base.Digest || actual.Size != base.Size || (kind == domain.VMComponentRootFS && (actual.Key == base.Key || actual.Size != want.Allocation.DiskBytes)) {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
	}
	return &vm.AdoptionProof{Sources: map[domain.VMComponentKind]string{domain.VMComponentKernel: release.KernelPath, domain.VMComponentRootFS: release.RootFSPath}}, nil
}
