package firecracker

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (d *Driver) WatchPersistent(ctx context.Context, id uuid.UUID, changed func()) error {
	if id == uuid.Nil || changed == nil {
		return vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	manager, ok := d.cfg.Processes.(PersistentProcessManager)
	if !ok {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	current, err := d.InspectPersistent(ctx, id)
	if err != nil {
		return err
	}
	if current.State != domain.VMRuntimeRunning {
		// The app rebinds this watcher after its next admitted power operation.
		<-ctx.Done()
		return ctx.Err()
	}
	record, err := d.readRecord(id.String())
	if err != nil || record == nil {
		return vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	watch, err := manager.WatchExit(ctx, record.VMMIdentity, record.Marker)
	if err != nil {
		return err
	}
	changed()
	err = watch.Wait(ctx)
	closeErr := watch.Close()
	if ctx.Err() == nil {
		changed()
	}
	return errors.Join(err, closeErr)
}
