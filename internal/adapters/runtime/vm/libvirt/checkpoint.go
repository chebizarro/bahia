package libvirt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type coldTPMLockKey struct{}

func (d *Driver) BeginColdCopy(ctx context.Context, r *vm.PersistentResource) (vm.ColdCopyGuard, error) {
	if r.State != domain.VMRuntimeStopped {
		return nil, vm.ProviderError(domain.VMErrorConflict, nil)
	}
	sub, err := d.cfg.Events.Subscribe(ctx, r.ID, false)
	if err != nil {
		return nil, err
	}
	barrier, ok := sub.(interface{ CheckCold(context.Context) error })
	if !ok {
		sub.Close()
		return nil, vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	var lock *os.File
	if path := r.Components[domain.VMComponentSWTPM]; path != "" {
		lockPath := filepath.Join(path, ".lock")
		if err = vm.CheckContainedPath(d.instanceDir(r.ID.String()), lockPath); err == nil {
			lock, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		}
		if err == nil {
			stateLock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
			err = syscall.FcntlFlock(lock.Fd(), syscall.F_SETLK, &stateLock)
		}
		if err != nil {
			if lock != nil {
				lock.Close()
			}
			sub.Close()
			return nil, vm.ProviderError(domain.VMErrorConflict, err)
		}
		ctx = context.WithValue(ctx, coldTPMLockKey{}, path)
	}
	guard := vm.NewInvalidatingCopy(ctx, func(ctx context.Context) error {
		for {
			event, err := sub.Next(ctx)
			if err != nil || event.ID == r.ID {
				return err
			}
		}
	}, func() error {
		err := sub.Close()
		if lock != nil {
			err = errors.Join(err, lock.Close())
		}
		return err
	}, func(ctx context.Context) error {
		if err := d.recheck(ctx, r); err != nil {
			return err
		}
		return barrier.CheckCold(ctx)
	})
	if err = guard.Check(ctx); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

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
		if locked, _ := ctx.Value(coldTPMLockKey{}).(string); locked == source {
			return vm.PackTPM(ctx, source, dest)
		}
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
