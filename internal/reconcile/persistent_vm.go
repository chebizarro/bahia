package reconcile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type PersistentVMReconcilerConfig struct {
	Repository repository.VirtualizationRepository
	Provider   domain.PersistentVMProvider
	// Service and Principal enable governed automatic power convergence. Nil is
	// observe-only, never an authorization bypass or implicit system principal.
	Service   *service.PersistentVMService
	Principal func(context.Context, uuid.UUID) (*auth.Principal, error)
	Now       func() time.Time
	// Hooks are post-persistence hints. E's journal projection remains durable truth.
	OnObservation func(context.Context, domain.PersistentVMDeployment, domain.VMObservation)
	OnSample      func(context.Context, domain.PersistentVMDeployment, domain.VMObservation)
	OnInventory   func(context.Context, domain.VirtualizationHost, []domain.VMInventoryEntry)
}
type PersistentVMReconciler struct{ cfg PersistentVMReconcilerConfig }

func NewPersistentVMReconciler(cfg PersistentVMReconcilerConfig) (*PersistentVMReconciler, error) {
	if cfg.Repository == nil || cfg.Provider == nil || (cfg.Service == nil) != (cfg.Principal == nil) {
		return nil, domain.ErrInvalidValue
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PersistentVMReconciler{cfg}, nil
}

// BeginSession fences pre-restart callbacks using the persisted cursor, including
// a rotation whose process crashed before its first observation was written.
func (r *PersistentVMReconciler) BeginSession(ctx context.Context, org, id uuid.UUID) (uuid.UUID, error) {
	var next uuid.UUID
	err := r.cfg.Repository.WithOperationLock(ctx, org, id, func(ctx context.Context) error {
		v, err := r.cfg.Repository.Deployments().Get(ctx, org, id)
		if err != nil {
			return err
		}
		expected := uuid.Nil
		if v.ObservationCursor != nil {
			expected = v.ObservationCursor.SessionID
		}
		next = uuid.New()
		return r.cfg.Repository.RotateObservationSession(ctx, repository.VirtualizationResourceRef{OrgID: org, Kind: domain.PersistentVMResource, ID: id}, v.Generation, expected, next)
	})
	return next, err
}
func (r *PersistentVMReconciler) Observe(ctx context.Context, org, id uuid.UUID) (*domain.VMObservation, error) {
	var out *domain.VMObservation
	err := r.cfg.Repository.WithOperationLock(ctx, org, id, func(ctx context.Context) error {
		v, err := r.cfg.Repository.Deployments().Get(ctx, org, id)
		if err != nil {
			return err
		}
		h, err := r.cfg.Repository.Hosts().Get(ctx, org, v.HostID)
		if err != nil {
			return err
		}
		image, err := r.cfg.Repository.Images().Get(ctx, org, v.ImageID)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, time.Duration(h.OperationLimits.InspectSeconds)*time.Second)
		obs, inspectErr := r.cfg.Provider.Inspect(bounded, v.Identity)
		cancel()
		if inspectErr != nil || obs == nil {
			obs = &domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationUnavailable, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Ownership: domain.VMOwnershipUnknown, Diagnostic: domain.VMDiagnostic{Code: domain.VMErrorUnavailable}}
		} else if !reflect.DeepEqual(obs.Identity, v.Identity) || obs.LifecycleClass != v.LifecycleClass {
			return domain.ErrInvalidValue
		}
		normalized := VMObservationAxes(*v, *image, *obs)
		out, err = service.PersistVMInspection(ctx, r.cfg.Repository, *v, normalized, r.cfg.Now())
		if err != nil {
			return err
		}
		r.emit(ctx, *v, *out)
		return nil
	})
	return out, err
}

// AcceptObservation preserves a provider callback's generation/session/sequence.
// It cannot rotate or re-stamp a stale message into the current session.
func (r *PersistentVMReconciler) AcceptObservation(ctx context.Context, org, id uuid.UUID, o domain.VMObservation) error {
	v, err := r.cfg.Repository.Deployments().Get(ctx, org, id)
	if err != nil {
		return err
	}
	if v.ObservationCursor == nil || o.SessionID != v.ObservationCursor.SessionID || o.Sequence <= v.ObservationCursor.Sequence || o.ObservedGeneration != v.Generation {
		return repository.ErrConflict
	}
	image, err := r.cfg.Repository.Images().Get(ctx, org, v.ImageID)
	if err != nil {
		return err
	}
	o = VMObservationAxes(*v, *image, o)
	if o.Availability == domain.VMObservationUnavailable && v.Observation != nil {
		o.RuntimeState = v.Observation.RuntimeState
		o.RuntimeObservedAt = v.Observation.RuntimeObservedAt
	}
	if err = domain.ValidateVMObservation(v, &o); err != nil {
		return err
	}
	if err = r.cfg.Repository.AcceptVMObservation(ctx, org, id, o); err != nil {
		return err
	}
	r.emit(ctx, *v, o)
	return nil
}
func (r *PersistentVMReconciler) emit(ctx context.Context, v domain.PersistentVMDeployment, o domain.VMObservation) {
	if r.cfg.OnSample != nil {
		r.cfg.OnSample(ctx, v, o)
	}
	if r.cfg.OnObservation != nil && VMObservationMaterialChange(v.Observation, &o) {
		r.cfg.OnObservation(ctx, v, o)
	}
}

// VMObservationAxes derives definition drift only from observed configuration.
// Runtime power and guest health remain independent: unhealthy can be in-sync,
// and a healthy running guest can have the wrong pinned image.
func VMObservationAxes(v domain.PersistentVMDeployment, image domain.VMImage, o domain.VMObservation) domain.VMObservation {
	if o.Availability != domain.VMObservationAvailable {
		o.Drift = domain.VMDriftUnknown
		o.GuestHealth = domain.VMGuestUnknown
		o.Usage = nil
		o.ConsoleAvailable = false
		o.Connections = nil
		return o
	}
	if o.RuntimeState == nil {
		o.Drift = domain.VMDriftUnknown
		return o
	}
	if *o.RuntimeState == domain.VMRuntimeAbsent {
		o.Drift = domain.VMDriftDrifted
		o.Ownership = domain.VMOwnershipUnknown
		o.Marker = nil
		return o
	}
	if o.Ownership != domain.VMOwned || o.AppliedConfigDigest == "" || o.AppliedImageDigest == "" {
		o.Drift = domain.VMDriftUnknown
		return o
	}
	// A provider can detect normalized live-definition drift even when its marker
	// still carries the old desired digest. Never erase that independent evidence.
	if o.Drift == domain.VMDriftDrifted || o.AppliedConfigDigest != v.ConfigDigest || o.AppliedImageDigest != image.ManifestDigest {
		o.Drift = domain.VMDriftDrifted
	} else {
		o.Drift = domain.VMDriftInSync
	}
	return o
}
func (r *PersistentVMReconciler) Reconcile(ctx context.Context, org, id uuid.UUID) (*domain.VMOperation, error) {
	o, err := r.Observe(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.cfg.Service == nil || o.Availability != domain.VMObservationAvailable || o.Ownership != domain.VMOwned || o.RuntimeState == nil || o.Drift != domain.VMDriftInSync {
		return nil, nil
	}
	v, err := r.cfg.Repository.Deployments().Get(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if o.ObservedGeneration != v.Generation {
		return nil, repository.ErrConflict
	}
	var kind domain.VMOperationKind
	switch {
	case v.DesiredPower == domain.VMDesiredStopped && *o.RuntimeState == domain.VMRuntimeRunning:
		kind = domain.VMOperationGracefulStop
	case v.DesiredPower == domain.VMDesiredRunning && *o.RuntimeState == domain.VMRuntimeStopped:
		kind = domain.VMOperationStart
	default:
		return nil, nil // absent, paused, failed and guest-health failures are not restart permission.
	}
	ops, err := r.operations(ctx, org, id)
	if err != nil {
		return nil, err
	}
	var latest *domain.VMOperation
	attempts := 0
	for index := range ops {
		op := &ops[index]
		if !op.Phase.Terminal() {
			return nil, nil
		}
		if latest == nil || op.CreatedAt.After(latest.CreatedAt) || (op.CreatedAt.Equal(latest.CreatedAt) && op.ID.String() > latest.ID.String()) {
			latest = op
		}
		if op.Reason == "automatic VM power convergence" && op.Kind == domain.VMOperationStart && !op.CreatedAt.Before(r.cfg.Now().Add(-time.Duration(v.Maintenance.WindowSeconds)*time.Second)) {
			attempts++
		}
	}
	if latest == nil || (latest.Kind == domain.VMOperationDelete && latest.DeleteTarget == domain.VMDeleteDeployment && latest.Phase == domain.VMOperationSucceeded) {
		return nil, nil
	}
	if latest.Reason == "automatic VM power convergence" && latest.Kind == kind && latest.Phase == domain.VMOperationFailed {
		return nil, nil
	}
	reason := "automatic VM power convergence"
	if kind == domain.VMOperationStart {
		initial := latest.Phase == domain.VMOperationSucceeded && latest.ResourceGeneration == v.Generation && (latest.Kind == domain.VMOperationDefine || latest.Kind == domain.VMOperationAdopt)
		if initial {
			reason = "initial VM power convergence"
		} else if !v.Maintenance.AutomaticRecovery || attempts >= v.Maintenance.MaxRecoveryAttempts {
			return nil, nil
		}
	}
	principal, err := r.cfg.Principal(ctx, org)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("vm-reconcile:%s:%d:%s:%s", id, v.Generation, latest.ID, kind)
	return r.cfg.Service.RequestOperation(ctx, principal, service.VMOperationRequest{OrgID: org, DeploymentID: id, ExpectedGeneration: v.Generation, IdempotencyKey: key, Reason: reason, Kind: kind})
}
func (r *PersistentVMReconciler) operations(ctx context.Context, org, id uuid.UUID) ([]domain.VMOperation, error) {
	var out []domain.VMOperation
	for offset := 0; ; offset += 100 {
		rows, err := r.cfg.Repository.ListOperations(ctx, org, id, 100, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 100 {
			return out, nil
		}
	}
}
func (r *PersistentVMReconciler) Inventory(ctx context.Context, org, hostID uuid.UUID) ([]domain.VMInventoryEntry, error) {
	h, err := r.cfg.Repository.Hosts().Get(ctx, org, hostID)
	if err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, time.Duration(h.OperationLimits.InspectSeconds)*time.Second)
	defer cancel()
	entries, err := r.cfg.Provider.Inventory(bounded, *h)
	if err != nil {
		return nil, &domain.VMProviderError{Code: domain.VMErrorUnavailable}
	}
	for index := range entries {
		e := &entries[index]
		m := e.Marker
		e.Ownership = domain.VMForeign
		if m != nil && domain.ValidateVMOwnershipMarker(*m) == nil && m.InstallationID == h.InstallationID && m.OrgID == org && m.HostID == hostID && m.Provider == h.Provider && m.ProviderResourceID == e.ProviderResourceID && m.LifecycleClass == domain.VMLifecyclePersistent {
			v, readErr := r.cfg.Repository.Deployments().Get(ctx, org, m.DeploymentID)
			switch {
			case errors.Is(readErr, repository.ErrNotFound):
				e.Ownership = domain.VMOrphan
			case readErr != nil:
				return nil, readErr
			case reflect.DeepEqual(v.Identity, m.VMResourceIdentity):
				e.Ownership = domain.VMOwned
			}
		}
		if e.Ownership == domain.VMForeign {
			e.Marker = nil
			e.Observation.Marker = nil
			e.Observation.Connections = nil
		}
		e.Observation.Ownership = e.Ownership
	}
	if r.cfg.OnInventory != nil {
		r.cfg.OnInventory(ctx, *h, entries)
	}
	return entries, nil
}
