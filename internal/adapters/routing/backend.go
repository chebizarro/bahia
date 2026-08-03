package routing

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
)

// Backend applies an already-canonical public route plan. Implementations must
// compensate partial provider mutations before returning an error.
type Backend interface {
	Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error
	Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error
}

type Resolver interface {
	Resolve(ref string) (Backend, bool)
}

type StaticResolver map[string]Backend

func (r StaticResolver) Resolve(ref string) (Backend, bool) {
	b, ok := r[ref]
	return b, ok && b != nil
}
