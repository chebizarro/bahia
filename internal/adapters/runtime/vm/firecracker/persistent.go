package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/atomicfile"
	"github.com/openagentsinc/bahia/internal/domain"
)

const ownershipFile = "ownership.json"

func (d *Driver) InspectPersistent(ctx context.Context, id uuid.UUID) (*vm.PersistentResource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == uuid.Nil || !filepath.IsAbs(d.cfg.InstancesDir) {
		return nil, vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	dir := d.instanceDir(id.String())
	if err := vm.CheckContainedPath(d.cfg.InstancesDir, dir); err != nil {
		return nil, err
	}
	r := &vm.PersistentResource{ID: id, State: domain.VMRuntimeAbsent, Components: map[domain.VMComponentKind]string{}}
	data, err := os.ReadFile(filepath.Join(dir, vmConfigFileName))
	if errors.Is(err, os.ErrNotExist) {
		if err := d.confirmProcessAbsent(ctx, id.String()); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg vmConfigFile
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if len(cfg.Drives) != 1 || !cfg.Drives[0].IsRootDevice || cfg.Drives[0].IsReadOnly || cfg.BootSource.KernelImagePath == "" {
		return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	r.Components[domain.VMComponentRootFS] = cfg.Drives[0].PathOnHost
	r.Components[domain.VMComponentKernel] = cfg.BootSource.KernelImagePath
	// Hash canonical JSON including unknown fields, so adoption cannot bless a
	// changed provider configuration that this driver's typed parser ignores.
	var canonical any
	if err = json.Unmarshal(data, &canonical); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	r.Fingerprint = vm.DigestBytes(normalized)
	data, err = os.ReadFile(filepath.Join(dir, ownershipFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		var m domain.VMOwnershipMarker
		if err = json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		r.Marker = &m
	}
	record, err := d.readRecord(id.String())
	if err != nil {
		return nil, vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	r.State = domain.VMRuntimeStopped
	if record == nil {
		for _, name := range []string{apiSocketFileName, vsockSocketFileName} {
			if _, err := os.Lstat(filepath.Join(dir, name)); err == nil || !errors.Is(err, os.ErrNotExist) {
				return nil, vm.ProviderError(domain.VMErrorIntegrity, err)
			}
		}
	}
	if record != nil {
		if record.PID <= 0 || record.StartTime == 0 || record.Marker != d.apiSocketPath(id.String()) {
			return nil, vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		manager, ok := d.cfg.Processes.(PersistentProcessManager)
		if !ok {
			return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		alive, err := manager.InspectProcess(ctx, record.VMMIdentity, record.Marker)
		if err != nil {
			return nil, err
		}
		if alive {
			state, err := inspectAPI(ctx, record.Marker)
			if err != nil {
				return nil, err
			}
			r.State = state
		}
	}
	return r, nil
}

// Missing configuration is not evidence that a VMM is gone. A valid recorded
// process must be confirmed dead; unexplained sockets or malformed records fail
// closed so deletion cannot remove the only remaining supervision evidence.
func (d *Driver) confirmProcessAbsent(ctx context.Context, name string) error {
	record, err := d.readRecord(name)
	if err != nil {
		return vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	if record != nil {
		if record.PID <= 0 || record.StartTime == 0 || record.Marker != d.apiSocketPath(name) {
			return vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		manager, ok := d.cfg.Processes.(PersistentProcessManager)
		if !ok {
			return vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		alive, err := manager.InspectProcess(ctx, record.VMMIdentity, record.Marker)
		if err != nil || alive {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		return nil
	}
	for _, file := range []string{apiSocketFileName, vsockSocketFileName} {
		if _, err := os.Lstat(filepath.Join(d.instanceDir(name), file)); !errors.Is(err, os.ErrNotExist) {
			return vm.ProviderError(domain.VMErrorIntegrity, err)
		}
	}
	return nil
}

func (d *Driver) ListPersistent(ctx context.Context) ([]uuid.UUID, error) {
	entries, err := os.ReadDir(d.cfg.InstancesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, e := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !e.IsDir() {
			continue
		}
		id, err := uuid.Parse(e.Name())
		if err != nil || id == uuid.Nil {
			continue
		}
		// Include orphaned process records even when the definition vanished.
		ids = append(ids, id)
	}
	return ids, nil
}
func (d *Driver) recheckPersistent(ctx context.Context, r *vm.PersistentResource) error {
	after, err := d.InspectPersistent(ctx, r.ID)
	if err != nil {
		return err
	}
	return vm.CheckPersistentResource(r, after)
}
func (d *Driver) AdoptPersistent(ctx context.Context, r *vm.PersistentResource, m domain.VMOwnershipMarker) error {
	if domain.ValidateVMOwnershipMarker(m) != nil || m.ProviderResourceID != r.ID || r.State == domain.VMRuntimeAbsent {
		return vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	if r.Marker == nil && r.State != domain.VMRuntimeStopped {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err := d.recheckPersistent(ctx, r); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(ctx, filepath.Join(d.instanceDir(r.ID.String()), ownershipFile), ".ownership-*.tmp", data, 0600)
}
func (d *Driver) DefinePersistent(ctx context.Context, s vm.PersistentSpec, current *vm.PersistentResource) error {
	if domain.ValidateVMOwnershipMarker(s.Marker) != nil || s.Instance.Name != s.Marker.ProviderResourceID.String() || s.Instance.InstanceDir != d.instanceDir(s.Instance.Name) {
		return vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	if s.Deployment.Firmware != domain.VMFirmwareNone || s.Deployment.TPM.Enabled || s.Deployment.Autostart {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	if err := vm.CheckDefinitionBaseline(current, s.Marker); err != nil {
		return err
	}
	if err := vm.CheckWritableComponents(s.Instance.InstanceDir, s.Components); err != nil {
		return err
	}
	if current.State != domain.VMRuntimeAbsent && current.State != domain.VMRuntimeStopped {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err := d.recheckPersistent(ctx, current); err != nil {
		return err
	}
	components := s.Components
	if components == nil {
		components = map[domain.VMComponentKind]string{}
	}
	if len(components) == 0 {
		if s.Instance.Image.Format != vm.FormatFirecrackerRootFS {
			return vm.ProviderError(domain.VMErrorInvalid, nil)
		}
		rootfs := filepath.Join(s.Instance.InstanceDir, rootfsFileName)
		if err := vm.CopyRegularFile(ctx, s.Instance.Image.RootFSPath, rootfs); err != nil {
			return err
		}
		components[domain.VMComponentRootFS] = rootfs
		components[domain.VMComponentKernel] = s.Instance.Image.KernelPath
	}
	if len(components) != 2 || components[domain.VMComponentKernel] == "" || components[domain.VMComponentRootFS] == "" {
		return vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	if err := vm.CheckContainedPath(s.Instance.InstanceDir, components[domain.VMComponentRootFS]); err != nil {
		return err
	}
	f, err := os.OpenFile(components[domain.VMComponentRootFS], os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err == nil && s.Deployment.Allocation.DiskBytes < info.Size() {
		err = vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err == nil {
		err = f.Truncate(s.Deployment.Allocation.DiskBytes)
	}
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	cfg := vmConfigFile{BootSource: bootSourceConfig{KernelImagePath: components[domain.VMComponentKernel], BootArgs: d.cfg.KernelArgs}, Drives: []driveConfig{{DriveID: "rootfs", PathOnHost: components[domain.VMComponentRootFS], IsRootDevice: true}}, MachineConfig: machineConfig{VcpuCount: s.Instance.VCPUs, MemSizeMib: s.Instance.MemoryMB}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err = d.recheckPersistent(ctx, current); err != nil {
		return err
	}
	// An ownership write cannot make a partially defined instance appear owned:
	// the core requires its separately verified v2 metadata before any mutation.
	if err = atomicfile.WriteFile(ctx, filepath.Join(s.Instance.InstanceDir, vmConfigFileName), ".config-*.tmp", data, 0600); err != nil {
		return err
	}
	data, err = json.Marshal(s.Marker)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(ctx, filepath.Join(s.Instance.InstanceDir, ownershipFile), ".ownership-*.tmp", data, 0600)
}
func (d *Driver) TransitionPersistent(ctx context.Context, r *vm.PersistentResource, kind domain.VMOperationKind, force bool) error {
	if r.Marker == nil || domain.ValidateVMOwnershipMarker(*r.Marker) != nil || r.Marker.ProviderResourceID != r.ID {
		return vm.ProviderError(domain.VMErrorForeign, nil)
	}
	if err := d.recheckPersistent(ctx, r); err != nil {
		return err
	}
	name := r.ID.String()
	switch kind {
	case domain.VMOperationStart:
		if r.State == domain.VMRuntimeRunning {
			return nil
		}
		if r.State != domain.VMRuntimeStopped {
			return vm.ProviderError(domain.VMErrorConflict, nil)
		}
		if _, ok := d.cfg.Processes.(PersistentProcessManager); !ok {
			return vm.ProviderError(domain.VMErrorUnsupported, nil)
		}
		if !filepath.IsAbs(d.cfg.Binary) || filepath.Base(d.cfg.Binary) != "firecracker" {
			return vm.ProviderError(domain.VMErrorInvalid, nil)
		}
		if err := d.startPersistentVMM(ctx, name); err != nil {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		after, err := d.InspectPersistent(ctx, r.ID)
		if err != nil || after.State != domain.VMRuntimeRunning {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		return nil
	case domain.VMOperationGracefulStop, domain.VMOperationReboot, domain.VMOperationDelete:
		if r.State != domain.VMRuntimeStopped {
			if kind == domain.VMOperationDelete && !force {
				return vm.ProviderError(domain.VMErrorApprovalRequired, nil)
			}
			record, err := d.readRecord(name)
			if err != nil || record == nil {
				return vm.ProviderError(domain.VMErrorIntegrity, err)
			}
			manager, ok := d.cfg.Processes.(PersistentProcessManager)
			if !ok {
				return vm.ProviderError(domain.VMErrorUnsupported, nil)
			}
			watch, err := manager.WatchExit(ctx, record.VMMIdentity, record.Marker)
			if err != nil {
				return err
			}
			defer watch.Close()
			if err = d.recheckPersistent(ctx, r); err != nil {
				return err
			}
			if kind == domain.VMOperationDelete {
				err = d.cfg.Processes.Kill(record.VMMIdentity, record.Marker)
			} else {
				err = sendCtrlAltDel(ctx, record.Marker)
			}
			if err != nil {
				return vm.ProviderError(domain.VMErrorUnconfirmed, err)
			}
			if err = watch.Wait(ctx); err != nil {
				return vm.ProviderError(domain.VMErrorUnconfirmed, err)
			}
			after, err := d.InspectPersistent(ctx, r.ID)
			if err != nil || after.State != domain.VMRuntimeStopped {
				return vm.ProviderError(domain.VMErrorUnconfirmed, err)
			}
			r = after
		}
		if kind == domain.VMOperationReboot {
			return d.TransitionPersistent(ctx, r, domain.VMOperationStart, false)
		}
		if kind == domain.VMOperationDelete {
			if err := d.recheckPersistent(ctx, r); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(d.instanceDir(name), vmConfigFileName)); err != nil {
				return err
			}
		}
		return nil
	}
	return vm.ProviderError(domain.VMErrorUnsupported, nil)
}
func (d *Driver) CopyPersistentComponent(ctx context.Context, kind domain.VMComponentKind, src, dst string) error {
	if kind != domain.VMComponentRootFS && kind != domain.VMComponentKernel {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	if vm.CheckContainedPath(d.cfg.InstancesDir, src) != nil && (kind != domain.VMComponentKernel || vm.CheckContainedPath(d.cfg.ImageRoot, src) != nil) {
		return vm.ProviderError(domain.VMErrorIntegrity, nil)
	}
	return vm.CopyRegularFile(ctx, src, dst)
}
func inspectAPI(ctx context.Context, socket string) (domain.VMRuntimeState, error) {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", vm.ProviderError(domain.VMErrorUnavailable, nil)
	}
	var state struct {
		State string `json:"state"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&state); err != nil {
		return "", err
	}
	switch state.State {
	case "Running":
		return domain.VMRuntimeRunning, nil
	case "Paused":
		return domain.VMRuntimePaused, nil
	default:
		return "", vm.ProviderError(domain.VMErrorUnavailable, nil)
	}
}

func (d *Driver) rejectPersistentLegacyMutation(name string) error {
	return d.VerifyLegacy(context.Background(), name, uuid.Nil)
}
func (d *Driver) VerifyLegacy(ctx context.Context, name string, expected uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	proof, err := vm.ReadLegacyProof(d.cfg.InstancesDir, name)
	if err != nil {
		return err
	}
	if expected != uuid.Nil && proof.ID != expected {
		return vm.ProviderError(domain.VMErrorForeign, nil)
	}
	if _, err := os.Lstat(filepath.Join(d.instanceDir(name), ownershipFile)); !errors.Is(err, os.ErrNotExist) {
		return vm.ProviderError(domain.VMErrorForeign, err)
	}
	path := filepath.Join(d.instanceDir(name), vmConfigFileName)
	if err := vm.CheckContainedPath(d.cfg.InstancesDir, path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil || vm.DigestBytes(data) != proof.DefinitionDigest {
		return vm.ProviderError(domain.VMErrorForeign, err)
	}
	return nil
}

var _ vm.PersistentDriver = (*Driver)(nil)
