package vm

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type persistentEvents interface {
	WatchPersistent(context.Context, uuid.UUID, func()) error
}

// WatchPersistentVM reports exact-resource wakeups, never authoritative state.
// It uses the same driver and trusted identity as Execute; there is no fallback.
func (p *PersistentProvider) WatchPersistentVM(ctx context.Context, id domain.VMResourceIdentity, changed func()) error {
	if err := p.checkIdentity(id); err != nil {
		return err
	}
	source, ok := p.driver.(persistentEvents)
	if !ok || changed == nil {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	return source.WatchPersistent(ctx, id.ProviderResourceID, changed)
}
