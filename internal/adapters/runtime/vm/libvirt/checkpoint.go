package libvirt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (d *Driver) CopyPersistentComponent(ctx context.Context, kind domain.VMComponentKind, source, dest string) error {
	if err := vm.CheckContainedPath(d.cfg.InstancesDir, source); err != nil {
		return err
	}
	switch kind {
	case domain.VMComponentDisk:
		// Flatten the entire backing chain into an independent qcow2; snapshots do
		// not silently depend on a moving image channel or an unexported backing file.
		out, err := d.qemuImg(ctx, "info", "--backing-chain", "--output=json", source)
		if err != nil {
			return err
		}
		var chain []struct {
			Filename string `json:"filename"`
			Format   string `json:"format"`
			DataFile string `json:"data-file"`
		}
		if err = json.Unmarshal(out, &chain); err != nil {
			return err
		}
		if len(chain) == 0 {
			return vm.ProviderError(domain.VMErrorIntegrity, nil)
		}
		for _, image := range chain {
			if image.Format != "qcow2" || image.DataFile != "" {
				return vm.ProviderError(domain.VMErrorUnsupported, nil)
			}
			if vm.CheckContainedPath(d.cfg.InstancesDir, image.Filename) != nil && (d.cfg.ImageRoot == "" || vm.CheckContainedPath(d.cfg.ImageRoot, image.Filename) != nil) {
				return vm.ProviderError(domain.VMErrorIntegrity, nil)
			}
			info, err := os.Lstat(image.Filename)
			if err != nil || !info.Mode().IsRegular() {
				return vm.ProviderError(domain.VMErrorIntegrity, err)
			}
		}
		_, err = d.qemuImg(ctx, "convert", "-f", "qcow2", "-O", "qcow2", source, dest)
		return err
	case domain.VMComponentNVRAM:
		return vm.CopyRegularFile(ctx, source, dest)
	case domain.VMComponentSWTPM:
		// swtpm owns this lock while its state backend is active. Do not copy even
		// an apparently stopped domain's state if its emulator still holds the lock.
		lockPath := filepath.Join(source, ".lock")
		if err := vm.CheckContainedPath(d.cfg.InstancesDir, lockPath); err != nil {
			return err
		}
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		stateLock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		if err = syscall.FcntlFlock(lock.Fd(), syscall.F_SETLK, &stateLock); err != nil {
			return vm.ProviderError(domain.VMErrorConflict, err)
		}
		// swtpm uses POSIX F_SETLK, not BSD flock; closing this fd releases it.
		return vm.PackTPM(ctx, source, dest)
	default:
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
}
