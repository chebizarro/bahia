package routing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// CompositeBackend applies the public provider first and the internal provider
// second. A later internal failure restores its own mutation through the
// Backend contract, then this composite invokes the public provider inverse.
type CompositeBackend struct {
	applyMu  sync.Mutex
	public   CompensatingBackend
	internal Backend
}

func NewCompositeBackend(public CompensatingBackend, internal Backend) (*CompositeBackend, error) {
	if public == nil || internal == nil {
		return nil, fmt.Errorf("public and internal routing backends are required")
	}
	return &CompositeBackend{public: public, internal: internal}, nil
}

func (b *CompositeBackend) Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if b == nil || plan == nil {
		return fmt.Errorf("composite public route plan is required")
	}
	if err := b.public.Check(ctx, plan); err != nil {
		return fmt.Errorf("check public route: %w", err)
	}
	if err := b.internal.Check(ctx, plan); err != nil {
		return fmt.Errorf("check internal route: %w", err)
	}
	return nil
}

func (b *CompositeBackend) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if b == nil || plan == nil {
		return fmt.Errorf("composite public route plan is required")
	}
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	// Re-check the local collision/certificate state immediately before the
	// remote mutation so a known-local failure cannot change Cloudflare.
	if err := b.internal.Check(ctx, plan); err != nil {
		return fmt.Errorf("check internal route before public apply: %w", err)
	}
	compensatePublic, err := b.public.ApplyWithCompensation(ctx, plan)
	if err != nil {
		return fmt.Errorf("apply public route: %w", err)
	}
	if compensatePublic == nil {
		return fmt.Errorf("apply public route: backend returned no compensation")
	}
	if err := b.internal.Apply(ctx, plan); err != nil {
		internalErr := fmt.Errorf("apply internal route: %w", err)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if compensationErr := compensatePublic(cleanupCtx); compensationErr != nil {
			return errors.Join(internalErr, fmt.Errorf("compensate public route after internal failure: %w", compensationErr))
		}
		return fmt.Errorf("%w; previous public route restored", internalErr)
	}
	return nil
}
