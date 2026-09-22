package libvirt

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

func (d *Driver) WatchPersistent(ctx context.Context, id uuid.UUID, changed func()) error {
	if d.cfg.Events == nil || id == uuid.Nil || changed == nil {
		return vm.ProviderError(domain.VMErrorUnavailable, nil)
	}
	backoff := 250 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sub, err := d.cfg.Events.Subscribe(ctx, id, false)
		if err == nil {
			// Inspect after successful registration to cover changes in the startup gap.
			changed()
			backoff = 250 * time.Millisecond
			for {
				event, nextErr := sub.Next(ctx)
				if nextErr != nil {
					break
				}
				if event.ID == id {
					changed()
				}
			}
			if closeErr := sub.Close(); closeErr != nil && ctx.Err() == nil {
				return vm.ProviderError(domain.VMErrorUnavailable, closeErr)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Loss is also a hint to re-inspect, never evidence of stop or deletion.
		changed()
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}
