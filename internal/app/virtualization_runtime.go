package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/reconcile"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// ProviderChanges carries hints only. Every callback triggers a fresh, fenced
// inspection; neither event names nor provider-supplied observations are trusted.
type ProviderChanges interface {
	WatchPersistentVM(context.Context, domain.VMResourceIdentity, func()) error
}
type VirtualizationServices struct {
	Provider    domain.PersistentVMProvider
	Host        *domain.VirtualizationHost
	Bootstrap   *service.VMBootstrapService
	VMConfig    service.PersistentVMServiceConfig
	PlaneClient domain.ExecutionPlaneClient
	PlanePolicy service.ExecutionPlaneAdmissionPolicy
	Endpoints   []domain.ExecutionPlaneEndpoint
	Workers     repository.WorkerRepository
	Principal   func(context.Context, uuid.UUID) (*auth.Principal, error)
	Logger      *zap.Logger
	policy      *virtualizationPolicy
}
type vmWork struct {
	org, id uuid.UUID
	kind    domain.VirtualizationResourceKind
}
type virtualizationRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type virtualizationRuntime struct {
	repo          repository.VirtualizationRepository
	organizations readmodel.VirtualizationOrganizations
	bus           events.Publisher
	host          *domain.VirtualizationHost
	provider      domain.PersistentVMProvider
	vmService     *service.PersistentVMService
	worker        *service.VMOperationWorker
	vmReconciler  *reconcile.PersistentVMReconciler
	planeService  *service.ExecutionPlaneService
	planes        *reconcile.ExecutionPlaneReconciler
	endpoints     []domain.ExecutionPlaneEndpoint
	policy        *virtualizationPolicy
	logger        *zap.Logger
	ready         atomic.Bool
	started       chan struct{}
	mu            sync.Mutex
	pending       map[vmWork]struct{}
	capabilities  map[uuid.UUID][]domain.ExecutionPlaneCapability
	wake          chan struct{}
	runs          map[vmWork]virtualizationRun
}

func newVirtualizationRuntime(deps VirtualizationDependencies, cfg VirtualizationServices) (*virtualizationRuntime, error) {
	r := &virtualizationRuntime{repo: deps.Repository, organizations: deps.Organizations, bus: deps.Bus, host: cfg.Host, provider: cfg.Provider, endpoints: cfg.Endpoints, policy: cfg.policy, logger: cfg.Logger, pending: map[vmWork]struct{}{}, capabilities: map[uuid.UUID][]domain.ExecutionPlaneCapability{}, wake: make(chan struct{}, 1), started: make(chan struct{}), runs: map[vmWork]virtualizationRun{}}
	if r.logger == nil {
		r.logger = zap.NewNop()
	}
	if cfg.Provider != nil {
		c := cfg.VMConfig
		c.Repository, c.Provider, c.Bootstrap = deps.Repository, cfg.Provider, cfg.Bootstrap
		c.OnChange = r.changed
		c.OnOperation = r.operation
		var err error
		r.vmService, err = service.NewPersistentVMService(c)
		if err != nil {
			return nil, err
		}
		r.worker, err = service.NewVMOperationWorker(r.vmService)
		if err != nil {
			return nil, err
		}
		rc := reconcile.PersistentVMReconcilerConfig{Repository: deps.Repository, Provider: cfg.Provider, OnObservation: r.observed, OnSample: r.sample, OnInventory: r.inventory}
		if cfg.Principal != nil {
			rc.Service, rc.Principal = r.vmService, cfg.Principal
		}
		r.vmReconciler, err = reconcile.NewPersistentVMReconciler(rc)
		if err != nil {
			return nil, err
		}
	}
	if cfg.PlaneClient != nil && cfg.PlanePolicy != nil && cfg.Workers != nil && len(cfg.Endpoints) > 0 {
		r.planeService = service.NewExecutionPlaneService(deps.Repository, cfg.PlanePolicy)
		r.planes = reconcile.NewExecutionPlaneReconciler(deps.Repository, cfg.PlaneClient, r.planeService, cfg.Workers, r.planeChanged)
	}
	return r, nil
}
func (r *virtualizationRuntime) enqueue(w vmWork) {
	r.mu.Lock()
	r.pending[w] = struct{}{}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *virtualizationRuntime) changed(ctx context.Context, ref repository.VirtualizationResourceRef) {
	typ := events.EventVirtualizationResourceChanged
	if ref.Kind == domain.VMOperationResource {
		typ = events.EventVMOperationTransitioned
	}
	// Request cancellation after commit cannot suppress publication of the wakeup.
	r.bus.Publish(context.WithoutCancel(ctx), events.Event{Type: typ, Data: events.VirtualizationChange{OrgID: ref.OrgID.String(), ResourceID: ref.ID.String()}})
}
func (r *virtualizationRuntime) metric(ctx context.Context, name string, l telemetry.VirtualizationLabels, value float64) {
	if err := telemetry.RecordVirtualization(ctx, name, l, value); err != nil {
		r.report(err)
	}
}
func (r *virtualizationRuntime) operation(ctx context.Context, op domain.VMOperation) {
	if op.Phase == domain.VMOperationAccepted {
		r.enqueue(vmWork{op.OrgID, op.ID, domain.VMOperationResource})
	}
	if !op.Phase.Terminal() {
		return
	}
	result := "failure"
	if op.Phase == domain.VMOperationSucceeded {
		result = "success"
	}
	labels := telemetry.VirtualizationLabels{LifecycleClass: string(op.LifecycleClass), Operation: string(op.Kind), Result: result, Reason: string(op.Outcome.Code)}
	r.metric(ctx, "operations_total", labels, 1)
	if op.CompletedAt != nil {
		r.metric(ctx, "operation_duration_seconds", labels, max(0, op.CompletedAt.Sub(op.CreatedAt).Seconds()))
	}
	r.enqueue(vmWork{op.OrgID, op.ResourceID, domain.PersistentVMResource})
}
func (r *virtualizationRuntime) observed(ctx context.Context, v domain.PersistentVMDeployment, _ domain.VMObservation) {
	r.changed(ctx, repository.VirtualizationResourceRef{OrgID: v.OrgID, Kind: domain.PersistentVMResource, ID: v.ID})
}
func (r *virtualizationRuntime) sample(ctx context.Context, v domain.PersistentVMDeployment, o domain.VMObservation) {
	// Every persisted sample wakes the aggregate collector, even without drift.
	r.changed(ctx, repository.VirtualizationResourceRef{OrgID: v.OrgID, Kind: domain.PersistentVMResource, ID: v.ID})
	if o.GuestHealth == domain.VMGuestHealthy || o.GuestHealth == domain.VMGuestUnhealthy {
		result := "success"
		if o.GuestHealth == domain.VMGuestUnhealthy {
			result = "failure"
		}
		r.metric(ctx, "guest_agent_probes_total", telemetry.VirtualizationLabels{Provider: string(v.Provider), LifecycleClass: string(v.LifecycleClass), Result: result}, 1)
	}
}
func (r *virtualizationRuntime) inventory(ctx context.Context, h domain.VirtualizationHost, entries []domain.VMInventoryEntry) {
	// Inventory includes foreign/orphan resources, unlike the desired repository.
	counts := map[domain.VMOwnershipClass]float64{domain.VMOwned: 0, domain.VMOrphan: 0, domain.VMForeign: 0}
	for _, e := range entries {
		counts[e.Ownership]++
	}
	for state, count := range counts {
		l := telemetry.VirtualizationLabels{Provider: string(h.Provider), State: string(state)}
		r.metric(ctx, "inventory", l, count)
		if state == domain.VMOrphan {
			r.metric(ctx, "orphans", l, count)
		}
	}
	r.changed(ctx, repository.VirtualizationResourceRef{OrgID: h.OrgID, Kind: domain.VirtualizationHostResource, ID: h.ID})
}
func (r *virtualizationRuntime) planeChanged(ctx context.Context, c reconcile.ExecutionPlaneChange) error {
	l := telemetry.VirtualizationLabels{Provider: "loom", Operation: "probe", Reason: string(c.Diagnostic.Code)}
	switch c.Kind {
	case "probe_observed":
		l.Result = "success"
		r.metric(ctx, "plane_probes_total", l, 1)
	case "probe_failed":
		l.Result = "failure"
		r.metric(ctx, "plane_probes_total", l, 1)
	}
	r.mu.Lock()
	previous := len(r.capabilities[c.PlaneID])
	r.capabilities[c.PlaneID] = append([]domain.ExecutionPlaneCapability(nil), c.Verified.Capabilities...)
	counts := map[domain.VMLifecycleClass]float64{domain.VMLifecycleLoomQEMU: 0, domain.VMLifecycleLoomFirecracker: 0}
	for _, capabilities := range r.capabilities {
		for _, capability := range capabilities {
			counts[capability.LifecycleClass]++
		}
	}
	for class, count := range counts {
		r.metric(ctx, "plane_capabilities", telemetry.VirtualizationLabels{Provider: "loom", LifecycleClass: string(class)}, count)
	}
	if previous > len(c.Verified.Capabilities) {
		r.metric(ctx, "plane_capability_retractions_total", l, float64(previous-len(c.Verified.Capabilities)))
	}
	r.mu.Unlock()
	r.bus.Publish(context.WithoutCancel(ctx), events.Event{Type: events.EventExecutionPlaneProbeChanged, Data: events.VirtualizationChange{OrgID: c.OrgID.String(), ResourceID: c.PlaneID.String()}})
	return nil
}
func (r *virtualizationRuntime) report(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("virtualization reconciliation unavailable", zap.Error(telemetry.SanitizedVirtualizationError(err)))
	}
}
func (r *virtualizationRuntime) replace(ctx context.Context, key vmWork, run func(context.Context) error) {
	if previous, ok := r.runs[key]; ok {
		previous.cancel()
		<-previous.done
	}
	child, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.runs[key] = virtualizationRun{cancel, done}
	go func() { defer close(done); defer cancel(); r.report(run(child)) }()
}
func (r *virtualizationRuntime) observe(ctx context.Context, key vmWork) error {
	if r.vmReconciler == nil {
		return nil
	}
	v, err := r.repo.Deployments().Get(ctx, key.org, key.id)
	if err != nil {
		return err
	}
	if r.host != nil && (v.HostID != r.host.ID || v.OrgID != r.host.OrgID) {
		return nil
	}
	if _, err = r.vmReconciler.Reconcile(ctx, key.org, key.id); err != nil {
		return err
	}
	current, err := r.repo.Deployments().Get(ctx, key.org, key.id)
	if err != nil {
		return err
	}
	if current.Observation != nil && current.Observation.RuntimeState != nil && *current.Observation.RuntimeState == domain.VMRuntimeAbsent {
		if previous, ok := r.runs[key]; ok {
			previous.cancel()
			<-previous.done
			delete(r.runs, key)
		}
		return nil
	}
	// Provider callbacks only enqueue; worker/reconcile never run in provider locks.
	if source, ok := r.provider.(ProviderChanges); ok {
		r.replace(ctx, key, func(ctx context.Context) error {
			return source.WatchPersistentVM(ctx, v.Identity, func() { r.enqueue(vmWork{key.org, key.id, domain.VirtualizationHostResource}) })
		})
	}
	return nil
}
func (r *virtualizationRuntime) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		r.ready.Store(false)
		cancel()
		for _, child := range r.runs {
			child.cancel()
		}
		for _, child := range r.runs {
			<-child.done
		}
	}()
	orgs, err := r.organizations.List(ctx)
	if err != nil {
		return err
	}
	for _, org := range orgs {
		if r.worker != nil {
			// Recover is the only startup operation scan. It verifies interrupted work
			// without replaying provider effects, and ignores terminal operations.
			r.report(r.worker.Recover(ctx, org.ID))
			rows, err := virtualizationList(ctx, r.repo.Deployments(), org.ID)
			if err != nil {
				return err
			}
			for _, v := range rows {
				if r.host != nil && (v.HostID != r.host.ID || v.OrgID != r.host.OrgID) {
					continue
				}
				if _, err := r.vmReconciler.BeginSession(ctx, org.ID, v.ID); err != nil {
					return err
				}
				r.enqueue(vmWork{org.ID, v.ID, domain.PersistentVMResource})
			}
		}
		if r.planes != nil {
			rows, err := virtualizationList(ctx, r.repo.ExecutionPlanes(), org.ID)
			if err != nil {
				return err
			}
			for _, p := range rows {
				r.enqueue(vmWork{org.ID, p.ID, domain.ExecutionPlaneResource})
			}
		}
	}
	if r.vmReconciler != nil && r.host != nil {
		_, err := r.vmReconciler.Inventory(ctx, r.host.OrgID, r.host.ID)
		r.report(err)
	}
	r.ready.Store(true)
	close(r.started)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			r.mu.Lock()
			pending := r.pending
			r.pending = map[vmWork]struct{}{}
			r.mu.Unlock()
			for w := range pending {
				if ctx.Err() != nil {
					return nil
				}
				switch w.kind {
				case domain.VMOperationResource:
					r.report(r.worker.Process(ctx, w.org, w.id))
				case domain.PersistentVMResource:
					r.report(r.observe(ctx, w))
				case domain.VirtualizationHostResource:
					// Refresh the exact resource and settle interrupted operations on an event.
					_, err := r.vmReconciler.Reconcile(ctx, w.org, w.id)
					r.report(err)
					ops, err := r.repo.ListOperations(ctx, w.org, w.id, 100, 0)
					r.report(err)
					for _, op := range ops {
						if !op.Phase.Terminal() && op.Phase != domain.VMOperationAwaitingApproval {
							r.report(r.worker.Process(ctx, w.org, op.ID))
						}
					}
				case domain.ExecutionPlaneResource:
					plane, err := r.repo.ExecutionPlanes().Get(ctx, w.org, w.id)
					if err != nil {
						r.report(err)
						continue
					}
					if err = r.endpointAllowed(*plane); err != nil {
						r.report(err)
						continue
					}
					r.replace(ctx, w, func(ctx context.Context) error { return r.planes.Run(ctx, w.org, w.id) })
				}
			}
		}
	}
}
func virtualizationList[T any](ctx context.Context, repo repository.VirtualizationResources[T], org uuid.UUID) ([]T, error) {
	var out []T
	for offset := 0; ; offset += 100 {
		rows, err := repo.List(ctx, org, 100, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 100 {
			return out, nil
		}
	}
}
