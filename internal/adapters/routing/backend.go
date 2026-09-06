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

// Compensation restores the exact provider state captured immediately before
// a successful apply.
type Compensation func(ctx context.Context) error

// CompensatingBackend exposes the successful apply's inverse so a composite
// can roll back an earlier provider when a later provider fails.
type CompensatingBackend interface {
	Backend
	ApplyWithCompensation(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (Compensation, error)
}

type Resolver interface {
	Resolve(ref string) (Backend, bool)
}

type StaticResolver map[string]Backend

func (r StaticResolver) Resolve(ref string) (Backend, bool) {
	b, ok := r[ref]
	return b, ok && b != nil
}
