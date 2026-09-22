package firecracker

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// Subscribe before launch so even a delayed socket creation wakes readiness.
// VMM exit and transport loss are failures, never readiness; launch identity is
// retained on timeout for subsequent exact-resource reconciliation.
func (d *Driver) startPersistentVMM(ctx context.Context, name string) (retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	events, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { retErr = vm.JoinCleanupError(retErr, events.Close()) }()
	if err = events.Add(d.instanceDir(name)); err != nil {
		return err
	}
	if err = d.startVMM(ctx, name); err != nil {
		return err
	}
	record, err := d.readRecord(name)
	if err != nil || record == nil {
		return vm.ProviderError(domain.VMErrorIntegrity, err)
	}
	manager, ok := d.cfg.Processes.(PersistentProcessManager)
	if !ok {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	exit, err := manager.WatchExit(ctx, record.VMMIdentity, record.Marker)
	if err != nil {
		return err
	}
	defer func() { retErr = vm.JoinCleanupError(retErr, exit.Close()) }()
	exited := make(chan error, 1)
	go func() { exited <- exit.Wait(ctx) }()
	defer func() { cancel(); <-exited }()
	for {
		alive, err := manager.InspectProcess(ctx, record.VMMIdentity, record.Marker)
		if err != nil || !alive {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		state, probeErr := inspectAPI(ctx, record.Marker)
		if probeErr == nil && state == domain.VMRuntimeRunning {
			return nil
		}
		select {
		case <-ctx.Done():
			return vm.ProviderError(domain.VMErrorUnconfirmed, ctx.Err())
		case err := <-exited:
			// Keep the completion available for the joined cleanup above.
			exited <- err
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		case err, ok := <-events.Errors:
			if !ok {
				return vm.ProviderError(domain.VMErrorUnconfirmed, nil)
			}
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		case event, ok := <-events.Events:
			if !ok {
				return vm.ProviderError(domain.VMErrorUnconfirmed, nil)
			}
			if event.Name != record.Marker && filepath.Base(event.Name) != consoleLogFileName {
				continue
			}
		}
	}
}
