package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

type vmReconcileResources[T any] struct {
	repository.VirtualizationResources[T]
	get func(context.Context, uuid.UUID, uuid.UUID) (*T, error)
}

func (r vmReconcileResources[T]) Get(ctx context.Context, org, id uuid.UUID) (*T, error) {
	return r.get(ctx, org, id)
}

type vmReconcileRepo struct {
	repository.VirtualizationRepository
	mu        sync.Mutex
	operation sync.Mutex
	v         domain.PersistentVMDeployment
	h         domain.VirtualizationHost
	i         domain.VMImage
	ops       []domain.VMOperation
}

func (r *vmReconcileRepo) Deployments() repository.PersistentVMDeploymentRepository {
	return vmReconcileResources[domain.PersistentVMDeployment]{get: func(_ context.Context, org, id uuid.UUID) (*domain.PersistentVMDeployment, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if org != r.v.OrgID || id != r.v.ID {
			return nil, repository.ErrNotFound
		}
		v := r.v
		return &v, nil
	}}
}
func (r *vmReconcileRepo) Hosts() repository.VirtualizationHostRepository {
	return vmReconcileResources[domain.VirtualizationHost]{get: func(_ context.Context, org, id uuid.UUID) (*domain.VirtualizationHost, error) {
		h := r.h
		return &h, nil
	}}
}
func (r *vmReconcileRepo) Images() repository.VMImageRepository {
	return vmReconcileResources[domain.VMImage]{get: func(_ context.Context, org, id uuid.UUID) (*domain.VMImage, error) { i := r.i; return &i, nil }}
}
func (r *vmReconcileRepo) WithOperationLock(ctx context.Context, org, id uuid.UUID, fn func(context.Context) error) error {
	r.operation.Lock()
	defer r.operation.Unlock()
	return fn(ctx)
}
func (r *vmReconcileRepo) RotateObservationSession(_ context.Context, ref repository.VirtualizationResourceRef, generation int64, expected, next uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := uuid.Nil
	if r.v.ObservationCursor != nil {
		old = r.v.ObservationCursor.SessionID
	}
	if r.v.OrgID != ref.OrgID || r.v.ID != ref.ID || r.v.Generation != generation || old != expected {
		return repository.ErrConflict
	}
	r.v.ObservationCursor = &domain.VMObservationCursor{SessionID: next}
	return nil
}
func (r *vmReconcileRepo) AcceptVMObservation(_ context.Context, org, id uuid.UUID, o domain.VMObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if org != r.v.OrgID || id != r.v.ID || r.v.ObservationCursor == nil || o.SessionID != r.v.ObservationCursor.SessionID || o.Sequence <= r.v.ObservationCursor.Sequence {
		return repository.ErrConflict
	}
	if err := domain.ValidateVMObservation(&r.v, &o); err != nil {
		return err
	}
	r.v.Observation = &o
	r.v.ObservationCursor = &domain.VMObservationCursor{SessionID: o.SessionID, Sequence: o.Sequence}
	return nil
}
func (r *vmReconcileRepo) ListOperations(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.VMOperation, error) {
	return r.ops, nil
}

type vmReconcileProvider struct {
	domain.PersistentVMProvider
	o       domain.VMObservation
	entries []domain.VMInventoryEntry
	err     error
}

func (p *vmReconcileProvider) Inspect(context.Context, domain.VMResourceIdentity) (*domain.VMObservation, error) {
	o := p.o
	return &o, p.err
}
func (p *vmReconcileProvider) Inventory(context.Context, domain.VirtualizationHost) ([]domain.VMInventoryEntry, error) {
	return append([]domain.VMInventoryEntry(nil), p.entries...), p.err
}
func vmReconcileFixture(t *testing.T) (*PersistentVMReconciler, *vmReconcileRepo, *vmReconcileProvider) {
	t.Helper()
	org, id, host := uuid.New(), uuid.New(), uuid.New()
	digest := "sha256:" + strings.Repeat("a", 64)
	v := domain.PersistentVMDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: id, OrgID: org, Generation: 1, CreatedBy: "operator"}, LifecycleClass: domain.VMLifecyclePersistent, HostID: host, ImageID: uuid.New(), ConfigDigest: digest, DesiredPower: domain.VMDesiredRunning}
	v.Identity = domain.VMResourceIdentity{InstallationID: uuid.New(), OrgID: org, HostID: host, DeploymentID: id, Provider: domain.VMProviderLibvirt, ProviderResourceID: uuid.New(), LifecycleClass: domain.VMLifecyclePersistent}
	i := domain.VMImage{ManifestDigest: digest}
	i.ID = v.ImageID
	i.OrgID = org
	h := domain.VirtualizationHost{InstallationID: v.Identity.InstallationID, Provider: domain.VMProviderLibvirt, OperationLimits: domain.DefaultVMOperationLimits()}
	h.ID = host
	h.OrgID = org
	state := domain.VMRuntimeRunning
	o := domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Drift: domain.VMDriftInSync, GuestHealth: domain.VMGuestHealthy, Ownership: domain.VMOwned, AppliedConfigDigest: digest, AppliedImageDigest: digest, Marker: &domain.VMOwnershipMarker{SchemaVersion: 2, VMResourceIdentity: v.Identity, AppliedGeneration: 1, OperationID: uuid.New(), ConfigDigest: digest, ImageDigest: digest}}
	repo := &vmReconcileRepo{v: v, h: h, i: i}
	provider := &vmReconcileProvider{o: o}
	reconciler, err := NewPersistentVMReconciler(PersistentVMReconcilerConfig{Repository: repo, Provider: provider, Now: func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }})
	require.NoError(t, err)
	return reconciler, repo, provider
}
func TestPersistentVMAxesStateMatrix(t *testing.T) {
	_, r, p := vmReconcileFixture(t)
	for _, state := range []domain.VMRuntimeState{domain.VMRuntimeAbsent, domain.VMRuntimeStopped, domain.VMRuntimeRunning, domain.VMRuntimePaused, domain.VMRuntimeFailed} {
		for _, health := range []domain.VMGuestHealth{domain.VMGuestHealthy, domain.VMGuestUnhealthy} {
			for _, drifted := range []bool{false, true} {
				o := p.o
				o.RuntimeState = &state
				o.GuestHealth = health
				if drifted {
					o.AppliedImageDigest = "sha256:" + strings.Repeat("b", 64)
				}
				out := VMObservationAxes(r.v, r.i, o)
				require.Equal(t, state, *out.RuntimeState)
				require.Equal(t, health, out.GuestHealth)
				if drifted || state == domain.VMRuntimeAbsent {
					require.Equal(t, domain.VMDriftDrifted, out.Drift)
				} else {
					require.Equal(t, domain.VMDriftInSync, out.Drift)
				}
			}
		}
	}
	o := p.o
	o.Drift = domain.VMDriftDrifted
	require.Equal(t, domain.VMDriftDrifted, VMObservationAxes(r.v, r.i, o).Drift, "live config drift survives unchanged metadata hashes")
}
func TestPersistentVMObservationDedupUnavailableAndStaleFencing(t *testing.T) {
	reconciler, r, p := vmReconcileFixture(t)
	ctx := context.Background()
	events, samples := 0, 0
	reconciler.cfg.OnObservation = func(context.Context, domain.PersistentVMDeployment, domain.VMObservation) { events++ }
	reconciler.cfg.OnSample = func(context.Context, domain.PersistentVMDeployment, domain.VMObservation) { samples++ }
	first, err := reconciler.Observe(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	second, err := reconciler.Observe(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.Greater(t, second.Sequence, first.Sequence)
	require.Equal(t, 1, events)
	require.Equal(t, 2, samples)
	require.ErrorIs(t, reconciler.AcceptObservation(ctx, r.v.OrgID, r.v.ID, *first), repository.ErrConflict)
	stale := *second
	stale.Sequence++
	stale.ObservedGeneration--
	require.ErrorIs(t, reconciler.AcceptObservation(ctx, r.v.OrgID, r.v.ID, stale), repository.ErrConflict)
	p.err = errors.New("provider down with private endpoint")
	unavailable, err := reconciler.Observe(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.Equal(t, domain.VMObservationUnavailable, unavailable.Availability)
	require.Equal(t, *first.RuntimeState, *unavailable.RuntimeState)
	require.Equal(t, first.RuntimeObservedAt, unavailable.RuntimeObservedAt)
	require.Equal(t, domain.VMDriftUnknown, unavailable.Drift)
	require.Equal(t, domain.VMGuestUnknown, unavailable.GuestHealth)
	require.Empty(t, unavailable.Diagnostic.EvidenceDigest)
	require.Equal(t, 2, events)
	session, err := reconciler.BeginSession(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.NotEqual(t, unavailable.SessionID, session)
	require.ErrorIs(t, reconciler.AcceptObservation(ctx, r.v.OrgID, r.v.ID, *unavailable), repository.ErrConflict)
	p.err = nil
	_, err = reconciler.Observe(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
}
func TestPersistentVMRestartBeforeFirstObservation(t *testing.T) {
	reconciler, r, _ := vmReconcileFixture(t)
	ctx := context.Background()
	interrupted, err := reconciler.BeginSession(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.Nil(t, r.v.Observation)
	restarted, err := NewPersistentVMReconciler(reconciler.cfg)
	require.NoError(t, err)
	recovered, err := restarted.BeginSession(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.NotEqual(t, interrupted, recovered)
	o, err := restarted.Observe(ctx, r.v.OrgID, r.v.ID)
	require.NoError(t, err)
	require.Equal(t, recovered, o.SessionID)
	require.EqualValues(t, 1, o.Sequence)
}
func TestPersistentVMInventoryOrphanVersusForeign(t *testing.T) {
	reconciler, r, p := vmReconcileFixture(t)
	owned := *p.o.Marker
	orphan := owned
	orphan.DeploymentID = uuid.New()
	orphan.ProviderResourceID = uuid.New()
	foreign := owned
	foreign.InstallationID = uuid.New()
	foreign.ProviderResourceID = uuid.New()
	p.entries = []domain.VMInventoryEntry{{ProviderResourceID: owned.ProviderResourceID, Marker: &owned}, {ProviderResourceID: orphan.ProviderResourceID, Marker: &orphan}, {ProviderResourceID: foreign.ProviderResourceID, Marker: &foreign}, {ProviderResourceID: uuid.New(), Ownership: domain.VMOwned}}
	entries, err := reconciler.Inventory(context.Background(), r.v.OrgID, r.h.ID)
	require.NoError(t, err)
	require.Equal(t, domain.VMOwned, entries[0].Ownership)
	require.Equal(t, domain.VMOrphan, entries[1].Ownership)
	require.Equal(t, domain.VMForeign, entries[2].Ownership)
	require.Equal(t, domain.VMForeign, entries[3].Ownership)
	require.Nil(t, entries[2].Marker)
}
func TestPersistentVMStoppedPausedFailedAndUnhealthyNeverImplicitlyRestart(t *testing.T) {
	reconciler, r, p := vmReconcileFixture(t)
	// Observe-only never admits, irrespective of runtime/health state. Automatic
	// mode additionally requires injected authorization and the recovery budget.
	for _, state := range []domain.VMRuntimeState{domain.VMRuntimeStopped, domain.VMRuntimePaused, domain.VMRuntimeFailed, domain.VMRuntimeRunning} {
		p.o.RuntimeState = &state
		p.o.GuestHealth = domain.VMGuestUnhealthy
		r.v.DesiredPower = domain.VMDesiredStopped
		op, err := reconciler.Reconcile(context.Background(), r.v.OrgID, r.v.ID)
		require.NoError(t, err)
		require.Nil(t, op)
	}
}

type vmReconcilePermissions struct{}

func (vmReconcilePermissions) CheckPermission(context.Context, *auth.Principal, uuid.UUID, domain.Permission) error {
	return nil
}

func TestPersistentVMAutomaticRecoveryGuards(t *testing.T) {
	for _, scenario := range []string{"stopped desired stopped", "running unhealthy", "paused", "failed", "budget exhausted", "active operation", "deleted", "definite failure"} {
		t.Run(scenario, func(t *testing.T) {
			reconciler, r, p := vmReconcileFixture(t)
			ctx := context.Background()
			svc, err := service.NewPersistentVMService(service.PersistentVMServiceConfig{Repository: r, Provider: p, Permissions: vmReconcilePermissions{}, Operator: func(context.Context, *auth.Principal) error { return nil }})
			require.NoError(t, err)
			reconciler.cfg.Service = svc
			principalCalls := 0
			reconciler.cfg.Principal = func(context.Context, uuid.UUID) (*auth.Principal, error) {
				principalCalls++
				return nil, errors.New("must not request actuation")
			}
			state := domain.VMRuntimeStopped
			p.o.RuntimeState = &state
			r.v.Maintenance = domain.VMMaintenancePolicy{AutomaticRecovery: true, MaxRecoveryAttempts: 1, WindowSeconds: 60}
			old := domain.VMOperation{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: uuid.New(), OrgID: r.v.OrgID, CreatedAt: reconciler.cfg.Now()}, ResourceID: r.v.ID, Phase: domain.VMOperationSucceeded, Kind: domain.VMOperationStart}
			switch scenario {
			case "stopped desired stopped":
				r.v.DesiredPower = domain.VMDesiredStopped
			case "running unhealthy":
				state = domain.VMRuntimeRunning
				p.o.GuestHealth = domain.VMGuestUnhealthy
			case "paused":
				state = domain.VMRuntimePaused
			case "failed":
				state = domain.VMRuntimeFailed
			case "budget exhausted":
				old.Reason = "automatic VM power convergence"
			case "active operation":
				old.Phase = domain.VMOperationUnconfirmed
			case "deleted":
				old.Kind = domain.VMOperationDelete
				old.DeleteTarget = domain.VMDeleteDeployment
			case "definite failure":
				old.Phase = domain.VMOperationFailed
				old.Reason = "automatic VM power convergence"
				old.CreatedAt = reconciler.cfg.Now().Add(-2 * time.Minute)
			}
			r.ops = []domain.VMOperation{old}
			op, err := reconciler.Reconcile(ctx, r.v.OrgID, r.v.ID)
			require.NoError(t, err)
			require.Nil(t, op)
			require.Zero(t, principalCalls)
		})
	}
}

func TestPersistentVMInitialPowerIntentIsNotHealthRecovery(t *testing.T) {
	reconciler, r, p := vmReconcileFixture(t)
	svc, err := service.NewPersistentVMService(service.PersistentVMServiceConfig{Repository: r, Provider: p, Permissions: vmReconcilePermissions{}, Operator: func(context.Context, *auth.Principal) error { return nil }})
	require.NoError(t, err)
	reconciler.cfg.Service = svc
	denied := errors.New("automatic principal unavailable")
	calls := 0
	reconciler.cfg.Principal = func(context.Context, uuid.UUID) (*auth.Principal, error) { calls++; return nil, denied }
	state := domain.VMRuntimeStopped
	p.o.RuntimeState = &state
	r.v.Maintenance = domain.VMMaintenancePolicy{}
	r.ops = []domain.VMOperation{{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{ID: uuid.New(), CreatedAt: reconciler.cfg.Now()}, ResourceID: r.v.ID, ResourceGeneration: r.v.Generation, Kind: domain.VMOperationDefine, Phase: domain.VMOperationSucceeded}}
	_, err = reconciler.Reconcile(context.Background(), r.v.OrgID, r.v.ID)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, calls, "initial explicit running intent still goes through authenticated admission without enabling recovery")
}

func TestVMObservationMaterialChanges(t *testing.T) {
	_, _, p := vmReconcileFixture(t)
	before := p.o
	after := before
	after.Sequence = 20
	after.SessionID = uuid.New()
	after.ObservedAt = time.Now()
	after.Usage = &domain.VMResourceUsage{CPUUtilizationRatio: 0.5}
	require.False(t, VMObservationMaterialChange(&before, &after))
	for _, mutate := range []func(*domain.VMObservation){func(o *domain.VMObservation) { o.Drift = domain.VMDriftDrifted }, func(o *domain.VMObservation) { o.GuestHealth = domain.VMGuestUnhealthy }, func(o *domain.VMObservation) { state := domain.VMRuntimePaused; o.RuntimeState = &state }, func(o *domain.VMObservation) { o.Ownership = domain.VMOrphan }, func(o *domain.VMObservation) { o.ConsoleAvailable = true }} {
		after = before
		mutate(&after)
		require.True(t, VMObservationMaterialChange(&before, &after))
	}
}
