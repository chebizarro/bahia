package firecracker

import (
	"context"

	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type coldFileWatch interface {
	Check() error
	Close() error
}
type coldGuard struct {
	ctx      context.Context
	driver   *Driver
	baseline *vm.PersistentResource
	watch    coldFileWatch
}

func (g *coldGuard) Context() context.Context { return g.ctx }
func (g *coldGuard) Close() error             { return g.watch.Close() }
func (g *coldGuard) Check(ctx context.Context) error {
	if err := g.driver.recheckPersistent(ctx, g.baseline); err != nil {
		return err
	}
	return g.watch.Check()
}

// Read the kernel's change queue synchronously at each barrier. Unlike an
// asynchronous fsnotify consumer, this cannot miss an already queued launch or
// write merely because its Go event-reader goroutine has not been scheduled.
func (d *Driver) BeginColdCopy(ctx context.Context, r *vm.PersistentResource) (vm.ColdCopyGuard, error) {
	if r.State != domain.VMRuntimeStopped {
		return nil, vm.ProviderError(domain.VMErrorConflict, nil)
	}
	if err := vm.CheckWritableComponents(d.instanceDir(r.ID.String()), r.Components); err != nil {
		return nil, err
	}
	watch, err := newColdFileWatch([]string{d.instanceDir(r.ID.String()), r.Components[domain.VMComponentRootFS]})
	if err != nil {
		return nil, err
	}
	g := &coldGuard{ctx, d, r, watch}
	if err = g.Check(ctx); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}
