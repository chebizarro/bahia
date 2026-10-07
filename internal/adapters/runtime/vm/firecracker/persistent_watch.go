package firecracker

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
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

	// Watch for process exit.
	watch, err := manager.WatchExit(ctx, record.VMMIdentity, record.Marker)
	if err != nil {
		return err
	}

	// Watch the instance directory for file events (API socket state changes,
	// console.log writes) so that health transitions trigger re-observation
	// even when the process stays alive. This closes the gap where readiness
	// could miss the API socket until another file event arrived (.40 item 5).
	watcher, watcherErr := fsnotify.NewWatcher()
	if watcherErr == nil {
		watcherErr = watcher.Add(d.instanceDir(id.String()))
	}
	if watcherErr != nil {
		// fsnotify is best-effort for health probing; process exit alone is
		// still correct for the critical "process died" path. Log but do not
		// fail — the watcher may be nil on unsupported platforms.
		watcher = nil
	}

	changed()
	err = d.watchLoop(ctx, record.Marker, watch, watcher, changed)
	closeErr := watch.Close()
	if watcher != nil {
		closeErr = errors.Join(closeErr, watcher.Close())
	}
	return errors.Join(err, closeErr)
}

// watchLoop multiplexes process exit and fsnotify health events. It fires
// changed() on any state transition (process exit, API socket modification,
// console.log write) so the app layer re-inspects and reconciles.
func (d *Driver) watchLoop(ctx context.Context, marker string, exit ProcessExit, watcher *fsnotify.Watcher, changed func()) error {
	exitCh := make(chan error, 1)
	go func() { exitCh <- exit.Wait(ctx) }()

	// Nil-safe channels: when watcher is nil these are nil channels that
	// never fire, which is correct — we just wait on exit + ctx.
	var fsEvents <-chan fsnotify.Event
	var fsErrors <-chan error
	if watcher != nil {
		fsEvents = watcher.Events
		fsErrors = watcher.Errors
	}

	// Track the last known API state to fire changed() only on transitions,
	// not on every console.log write. The initial state is "running" since
	// we verified that in WatchPersistent before entering the loop.
	lastState := domain.VMRuntimeRunning

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exitCh:
			// Process exited — always a state transition.
			if ctx.Err() == nil {
				changed()
			}
			return err
		case event, ok := <-fsEvents:
			if !ok {
				// Watcher closed; fall back to exit-only watching.
				fsEvents = nil
				fsErrors = nil
				continue
			}
			// Only re-probe on API socket or console.log changes.
			if event.Name != marker && filepath.Base(event.Name) != consoleLogFileName {
				continue
			}
			// Probe the API to detect health transitions.
			state, probeErr := inspectAPI(ctx, marker)
			if probeErr != nil {
				// API unreachable: if the previous state was running,
				// this is a health transition (e.g. socket removed).
				if lastState == domain.VMRuntimeRunning {
					lastState = ""
					if ctx.Err() == nil {
						changed()
					}
				}
				continue
			}
			if state != lastState {
				lastState = state
				if ctx.Err() == nil {
					changed()
				}
			}
		case _, ok := <-fsErrors:
			if !ok {
				fsEvents = nil
				fsErrors = nil
			}
			// fsnotify errors are transient; continue watching exit.
		}
	}
}
