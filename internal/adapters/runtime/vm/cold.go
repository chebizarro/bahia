package vm

import (
	"context"
	"sync/atomic"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ColdCopyDriver registers a transition watch before the stopped-state recheck.
// The watch must invalidate on every transition or loss, even a start/stop cycle
// whose final state is stopped. No driver capability means no cold checkpoint.
type ColdCopyDriver interface {
	BeginColdCopy(context.Context, *PersistentResource) (ColdCopyGuard, error)
}
type ColdCopyGuard interface {
	Context() context.Context
	Check(context.Context) error
	Close() error
}

// InvalidatingCopy joins its event reader on close and never interprets stream
// closure/cancellation as proof that the resource remained stopped.
type InvalidatingCopy struct {
	ctx     context.Context
	invalid atomic.Bool
	cancel  context.CancelFunc
	done    chan struct{}
	close   func() error
	verify  func(context.Context) error
}

func NewInvalidatingCopy(ctx context.Context, next func(context.Context) error, closeWatch func() error, verify func(context.Context) error) *InvalidatingCopy {
	ctx, cancel := context.WithCancel(ctx)
	g := &InvalidatingCopy{ctx: ctx, cancel: cancel, done: make(chan struct{}), close: closeWatch, verify: verify}
	go func() {
		defer close(g.done)
		_ = next(ctx)
		g.invalid.Store(true)
	}()
	return g
}
func (g *InvalidatingCopy) Context() context.Context { return g.ctx }
func (g *InvalidatingCopy) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.verify(ctx); err != nil {
		return err
	}
	if g.invalid.Load() {
		return ProviderError(domain.VMErrorConflict, nil)
	}
	return nil
}
func (g *InvalidatingCopy) Close() error {
	g.cancel()
	err := g.close()
	<-g.done
	return err
}
