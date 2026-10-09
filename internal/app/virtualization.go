package app

import (
	"context"
	"sync"

	"github.com/openagentsinc/bahia/internal/adapters/loom"

	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
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
	Services        *VirtualizationServices
}
type Virtualization struct {
	Query         readmodel.VirtualizationQuery
	Handlers      *controlplane.VirtualizationHandlers
	Projector     *readmodel.VirtualizationProjector
	Metrics       *telemetry.VirtualizationCollector
	organizations readmodel.VirtualizationOrganizations
	runtime       *virtualizationRuntime
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// NewVirtualization deliberately leaves every legacy SQL-backed adapter
// disconnected. A complete dependency set is not proof of a canonical signed
// intent source; re-enabling it would let journal rows authorize publication.
func NewVirtualization(deps VirtualizationDependencies) (*Virtualization, error) {
	return &Virtualization{
		Handlers: &controlplane.VirtualizationHandlers{RBAC: deps.RBAC, CanonicalAuthor: deps.CanonicalAuthor},
	}, nil
}

// virtualizationSuspensionRelevant uses configuration, not database connection
// success: a failed PostgreSQL connection must not hide the suspension warning.
func virtualizationSuspensionRelevant(cfg *config.Config, dbAvailable bool) bool {
	return dbAvailable || cfg.DB.Host != "" || cfg.DB.Name != "" ||
		cfg.Virtualization.PersistentVM.Enabled || len(cfg.Virtualization.PlaneEndpoints) > 0
}

// registerVirtualizationSuspendedHealth reports the missing signed-intent
// recovery source without making optional PostgreSQL state a core readiness gate.
func registerVirtualizationSuspendedHealth(provider *HealthProvider) {
	provider.RegisterCheck("virtualization_canonical_recovery", func() HealthCheck {
		return HealthCheck{Name: "virtualization_canonical_recovery", Status: HealthStatusWarn,
			Message: "virtualization admission and projection suspended until signed-intent recovery replaces the PostgreSQL journal"}
	})
}

func (v *Virtualization) Name() string { return "virtualization-projection" }
func (v *Virtualization) Run(ctx context.Context) error {
	if v.Projector == nil {
		return readmodel.ErrVirtualizationUnavailable
	}
	ctx, cancel := context.WithCancel(ctx)
	v.mu.Lock()
	v.cancel = cancel
	v.mu.Unlock()
	defer cancel()
	defer v.Close()
	if err := v.Metrics.Collect(ctx); err != nil {
		return err
	}
	if v.runtime == nil {
		return v.Projector.Run(ctx, v.organizations)
	}
	// Recover canonical state before opening admission. Both children share one
	// lifecycle and are joined before shutdown returns.
	orgs, err := v.organizations.List(ctx)
	if err != nil {
		return err
	}
	for _, org := range orgs {
		if err := v.Projector.Recover(ctx, org.ID); err != nil {
			return err
		}
	}
	results := make(chan error, 2)
	go func() { results <- v.Projector.Run(ctx, v.organizations) }()
	go func() { results <- v.runtime.Run(ctx) }()
	err = <-results
	cancel()
	other := <-results
	if err != nil {
		return err
	}
	return other
}
func (v *Virtualization) VerifiedPlanes() loom.VerifiedPlaneCapabilitySource {
	if v.runtime == nil || v.runtime.planes == nil {
		return nil
	}
	return v.runtime.planes
}
func (v *Virtualization) Close() {
	v.mu.Lock()
	if v.cancel != nil {
		v.cancel()
	}
	v.mu.Unlock()
	if v.Metrics != nil {
		v.Metrics.Close()
	}
	if v.Projector != nil {
		v.Projector.Close()
	}
}
