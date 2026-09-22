package app

import (
	"context"

	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
)

// VirtualizationDependencies is the E-owned composition seam. C/D service
// adapters implement these admission interfaces and receive this same Bus in
// their constructors; they publish committed-change wakeups, never snapshots.
// Missing services are intentionally unavailable, not repository/provider fallbacks.
type VirtualizationDependencies struct {
	Repository      repository.VirtualizationRepository
	PersistentVM    controlplane.PersistentVMMutations
	ExecutionPlane  controlplane.ExecutionPlaneMutations
	RBAC            *auth.RBAC
	Bus             events.Publisher
	Store           readmodel.VirtualizationProjectionStore
	Publisher       readmodel.VirtualizationSignedPublisher
	Organizations   readmodel.VirtualizationOrganizations
	CanonicalAuthor string
}
type Virtualization struct {
	Query         readmodel.VirtualizationQuery
	Handlers      *controlplane.VirtualizationHandlers
	Projector     *readmodel.VirtualizationProjector
	Metrics       *telemetry.VirtualizationCollector
	organizations readmodel.VirtualizationOrganizations
}

func NewVirtualization(deps VirtualizationDependencies) (*Virtualization, error) {
	v := &Virtualization{Query: readmodel.VirtualizationQuery{Repository: deps.Repository}, organizations: deps.Organizations}
	v.Handlers = &controlplane.VirtualizationHandlers{Query: v.Query, RBAC: deps.RBAC, Persistent: deps.PersistentVM, Planes: deps.ExecutionPlane, CanonicalAuthor: deps.CanonicalAuthor}
	if deps.Repository == nil || deps.Store == nil || deps.Publisher == nil || deps.Bus == nil || deps.Organizations == nil || deps.CanonicalAuthor == "" {
		return v, nil
	}
	p, err := readmodel.NewVirtualizationProjector(deps.Repository, deps.Store, deps.Publisher, deps.CanonicalAuthor)
	if err != nil {
		return nil, err
	}
	subscriber, ok := deps.Bus.(events.ErrorSubscriber)
	if !ok {
		p.Close()
		return nil, readmodel.ErrVirtualizationUnavailable
	}
	if err := p.Subscribe(subscriber); err != nil {
		p.Close()
		return nil, err
	}
	v.Metrics = telemetry.NewVirtualizationCollector(deps.Repository, deps.Organizations)
	v.Metrics.Subscribe(subscriber)
	v.Projector = p
	v.Handlers.ProjectionReady = true
	v.Handlers.ProjectionAvailable = p.Available
	return v, nil
}
func (v *Virtualization) Name() string { return "virtualization-projection" }
func (v *Virtualization) Run(ctx context.Context) error {
	if v.Projector == nil {
		return readmodel.ErrVirtualizationUnavailable
	}
	defer v.Close()
	if err := v.Metrics.Collect(ctx); err != nil {
		return err
	}
	return v.Projector.Run(ctx, v.organizations)
}
func (v *Virtualization) Close() {
	if v.Metrics != nil {
		v.Metrics.Close()
	}
	if v.Projector != nil {
		v.Projector.Close()
	}
}
