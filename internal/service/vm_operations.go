package service

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// VMOperationWorker runs only under the application's lifecycle context. Process
// is invoked by admission/journal/provider signals; Recover is the startup scan.
// No request goroutine, timer, or provider acknowledgment implies completion.
type VMOperationWorker struct{ service *PersistentVMService }

func NewVMOperationWorker(service *PersistentVMService) (*VMOperationWorker, error) {
	if service == nil || service.cfg.Repository == nil || service.cfg.Provider == nil {
		return nil, domain.ErrInvalidValue
	}
	return &VMOperationWorker{service: service}, nil
}
func (w *VMOperationWorker) Recover(ctx context.Context, org uuid.UUID) error {
	var failures []error
	for offset := 0; ; offset += 100 {
		rows, err := w.service.cfg.Repository.ListOperations(ctx, org, uuid.Nil, 100, offset)
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
		for _, op := range rows {
			if op.Phase != domain.VMOperationAwaitingApproval {
				if err = w.Process(ctx, org, op.ID); err != nil {
					failures = append(failures, err)
				}
			}
		}
		if len(rows) < 100 {
			return errors.Join(failures...)
		}
	}
}
func (w *VMOperationWorker) Process(ctx context.Context, org, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := w.service.cfg.Repository
	op, err := r.GetOperation(ctx, org, id)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{op.ResourceID}
	if op.CloneTargetID != nil {
		ids = append(ids, *op.CloneTargetID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	var lock func(int, context.Context) error
	lock = func(n int, ctx context.Context) error {
		if n == len(ids) {
			return w.processLocked(ctx, org, id)
		}
		return r.WithOperationLock(ctx, org, ids[n], func(ctx context.Context) error { return lock(n+1, ctx) })
	}
	return lock(0, ctx)
}
func (w *VMOperationWorker) transition(ctx context.Context, op *domain.VMOperation, phase domain.VMOperationPhase, code domain.VMErrorCode, refs []uuid.UUID) (*domain.VMOperation, error) {
	next, err := w.service.cfg.Repository.TransitionOperation(ctx, repository.VMOperationTransition{OrgID: op.OrgID, OperationID: op.ID, ExpectedPhase: op.Phase, ExpectedRevision: op.Generation, Phase: phase, Outcome: domain.VMDiagnostic{Code: code}, PreparedStorageRefs: refs})
	if err == nil {
		w.service.notify(ctx, *next)
	}
	return next, err
}
func (w *VMOperationWorker) unconfirmed(ctx context.Context, op *domain.VMOperation) error {
	durable, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(domain.DefaultVMOperationLimits().InspectSeconds)*time.Second)
	defer cancel()
	if op.Phase != domain.VMOperationUnconfirmed {
		if _, err := w.transition(durable, op, domain.VMOperationUnconfirmed, domain.VMErrorUnconfirmed, nil); err != nil {
			return errors.Join(vmError(domain.VMErrorUnconfirmed), err)
		}
	}
	return vmError(domain.VMErrorUnconfirmed)
}
func (w *VMOperationWorker) fail(ctx context.Context, op *domain.VMOperation, code domain.VMErrorCode) error {
	_, err := w.transition(ctx, op, domain.VMOperationFailed, code, nil)
	if err != nil {
		return err
	}
	return vmError(code)
}
func (w *VMOperationWorker) processLocked(ctx context.Context, org, id uuid.UUID) error {
	s, r := w.service, w.service.cfg.Repository
	op, err := r.GetOperation(ctx, org, id)
	if err != nil {
		return err
	}
	if op.Phase == domain.VMOperationAwaitingApproval {
		return nil
	}
	v, err := r.Deployments().Get(ctx, org, op.ResourceID)
	if err != nil {
		return err
	}
	if op.Phase.Terminal() {
		if op.Phase == domain.VMOperationSucceeded && op.Kind == domain.VMOperationDelete && v.Generation == op.ResourceGeneration {
			return w.releaseDeleted(ctx, *v, *op)
		}
		return nil
	}
	if v.Generation != op.ResourceGeneration {
		return repository.ErrConflict
	}
	h, err := r.Hosts().Get(ctx, org, v.HostID)
	if err != nil {
		return err
	}
	if op.Phase != domain.VMOperationAccepted {
		if op.Phase == domain.VMOperationExecuting {
			op, err = w.transition(ctx, op, domain.VMOperationUnconfirmed, domain.VMErrorUnconfirmed, nil)
			if err != nil {
				return err
			}
		}
		if op.Phase == domain.VMOperationUnconfirmed {
			op, err = w.transition(ctx, op, domain.VMOperationVerifying, "", nil)
			if err != nil {
				return err
			}
		}
		// Never replay a command from an interrupted operation. An exact marker and
		// postcondition can settle it; otherwise keep the exclusive unconfirmed slot.
		return w.verify(ctx, *h, *v, op, nil)
	}
	if err = s.mutationFence(ctx, org, op.ResourceID, op.CloneTargetID, op.ID); err != nil {
		return err
	}
	if !op.Deadline.After(s.cfg.Now()) {
		return w.fail(ctx, op, domain.VMErrorInvalid)
	}
	p := vmExecutionPrincipal(op.Actor)
	if err = s.authorize(ctx, p, org, domain.PermWriteDeployments); err != nil {
		return w.fail(ctx, op, domain.VMErrorInvalid)
	}
	h, image, err := s.references(ctx, p, v)
	if err != nil {
		return w.fail(ctx, op, domain.VMErrorInvalid)
	}
	observation, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return err
	}
	if op.RequiredTier == domain.VMApprovalDestructive {
		if op.ApprovalID == nil {
			return w.fail(ctx, op, domain.VMErrorApprovalRequired)
		}
		a, e := r.GetApproval(ctx, org, *op.ApprovalID)
		if e != nil {
			return e
		}
		if !vmApprovalValid(a, *op, observation.Diagnostic.EvidenceDigest, s.cfg.Now()) {
			return w.fail(ctx, op, domain.VMErrorApprovalRequired)
		}
	}
	if observation.Marker != nil && op.Kind != domain.VMOperationAdopt && observation.Marker.AppliedGeneration > op.ExpectedGeneration {
		return w.fail(ctx, op, domain.VMErrorConflict)
	}
	if *observation.RuntimeState != domain.VMRuntimeAbsent && op.Kind != domain.VMOperationAdopt && observation.Ownership != domain.VMOwned {
		return w.fail(ctx, op, domain.VMErrorForeign)
	}
	if _, err = s.artifactReferences(ctx, p, *v, *op, observation); err != nil {
		return w.fail(ctx, op, domain.VMErrorConflict)
	}
	req := domain.VMProviderOperation{Host: *h, Deployment: *v, Image: *image, Operation: *op}
	req.Deployment.Observation = nil
	req.Deployment.ObservationCursor = nil
	if op.Plan != nil {
		req.Plan = *op.Plan
	}
	if op.CheckpointID != nil {
		req.Checkpoint, err = r.Checkpoints().Get(ctx, org, *op.CheckpointID)
		if err != nil {
			return err
		}
	}
	if op.ExportID != nil {
		req.Export, err = r.Exports().Get(ctx, org, *op.ExportID)
		if err != nil {
			return err
		}
	}
	if op.CloneTargetID != nil {
		req.CloneTarget, err = r.Deployments().Get(ctx, org, *op.CloneTargetID)
		if err != nil {
			return err
		}
		targetObs, e := vmInspect(ctx, s.cfg.Provider, *h, req.CloneTarget.Identity)
		if e != nil {
			return e
		}
		if *targetObs.RuntimeState != domain.VMRuntimeAbsent {
			return w.fail(ctx, op, domain.VMErrorConflict)
		}
	}
	refs := vmPreparedStorage(*v, *op)
	op, err = w.transition(ctx, op, domain.VMOperationExecuting, "", refs)
	if err != nil {
		return err
	}
	req.Operation = *op
	if len(v.Bootstrap) > 0 && !v.BootstrapApplied && op.Kind == domain.VMOperationDefine {
		req.Bootstrap = s.cfg.Bootstrap.Delivery(*v, *op)
	}
	execution, cancel := context.WithDeadline(ctx, op.Deadline)
	execution, boundCancel := context.WithTimeout(execution, vmOperationDuration(*h, op.Kind))
	result, executeErr := s.cfg.Provider.Execute(execution, req)
	boundCancel()
	cancel()
	if executeErr != nil {
		var pe *domain.VMProviderError
		if ctx.Err() == nil && errors.As(executeErr, &pe) && !pe.Unconfirmed && pe.Code != domain.VMErrorUnconfirmed {
			code := vmSafeCode(pe.Code)
			if code == domain.VMErrorUnconfirmed {
				return w.unconfirmed(ctx, op)
			}
			return w.fail(ctx, op, code)
		}
		return w.unconfirmed(ctx, op)
	}
	if result == nil || result.OperationID != op.ID || result.LifecycleClass != domain.VMLifecyclePersistent || !result.Confirmed {
		return w.unconfirmed(ctx, op)
	}
	op, err = w.transition(ctx, op, domain.VMOperationVerifying, "", nil)
	if err != nil {
		return err
	}
	if err = w.persistArtifacts(ctx, req, result); err != nil {
		return w.unconfirmed(ctx, op)
	}
	return w.verify(ctx, *h, *v, op, result)
}
func vmSafeCode(code domain.VMErrorCode) domain.VMErrorCode {
	switch code {
	case domain.VMErrorInvalid, domain.VMErrorConflict, domain.VMErrorUnavailable, domain.VMErrorUnsupported, domain.VMErrorForeign, domain.VMErrorApprovalRequired, domain.VMErrorQuota, domain.VMErrorUnconfirmed, domain.VMErrorIntegrity:
		return code
	}
	return domain.VMErrorUnconfirmed
}
func vmPreparedStorage(v domain.PersistentVMDeployment, op domain.VMOperation) []uuid.UUID {
	if op.Kind != domain.VMOperationDefine && op.Kind != domain.VMOperationCheckpoint && op.Kind != domain.VMOperationClone && op.Kind != domain.VMOperationRestore && op.Kind != domain.VMOperationExport {
		return nil
	}
	kinds := []domain.VMComponentKind{domain.VMComponentDisk}
	if v.Provider == domain.VMProviderFirecracker {
		kinds = []domain.VMComponentKind{domain.VMComponentKernel, domain.VMComponentRootFS}
	} else {
		if v.Firmware == domain.VMFirmwareUEFI {
			kinds = append(kinds, domain.VMComponentNVRAM)
		}
		if v.TPM.Enabled {
			kinds = append(kinds, domain.VMComponentSWTPM)
		}
	}
	refs := make([]uuid.UUID, 0, len(kinds))
	for _, kind := range kinds {
		refs = append(refs, uuid.NewSHA1(op.ID, []byte(kind)))
	}
	return refs
}
func (w *VMOperationWorker) persistArtifacts(ctx context.Context, q domain.VMProviderOperation, result *domain.VMProviderResult) error {
	r := w.service.cfg.Repository
	if q.Operation.Kind == domain.VMOperationCheckpoint {
		c := result.Checkpoint
		if c == nil || q.Checkpoint == nil || c.ID != q.Checkpoint.ID || c.OrgID != q.Operation.OrgID || c.State != domain.VMArtifactReady || domain.ValidateVMCheckpoint(c) != nil {
			return vmError(domain.VMErrorIntegrity)
		}
		next := *c
		next.VirtualizationResourceMeta = q.Checkpoint.VirtualizationResourceMeta
		next.Generation++
		if err := r.Checkpoints().Update(ctx, &next, q.Checkpoint.Generation); err != nil {
			return err
		}
	}
	if q.Operation.Kind == domain.VMOperationExport {
		e := result.Export
		if e == nil || q.Export == nil || e.ID != q.Export.ID || e.OrgID != q.Operation.OrgID || e.State != domain.VMArtifactReady || domain.ValidateVMExport(e) != nil {
			return vmError(domain.VMErrorIntegrity)
		}
		next := *e
		next.VirtualizationResourceMeta = q.Export.VirtualizationResourceMeta
		next.Generation++
		if err := r.Exports().Update(ctx, &next, q.Export.Generation); err != nil {
			return err
		}
	}
	if q.Operation.Kind == domain.VMOperationDelete {
		switch q.Operation.DeleteTarget {
		case domain.VMDeleteCheckpoint:
			c := q.Checkpoint
			proof := result.Checkpoint
			if c == nil || proof == nil || domain.ValidateVMCheckpoint(proof) != nil || proof.ID != c.ID || proof.OrgID != c.OrgID || proof.DeploymentID != c.DeploymentID || !reflect.DeepEqual(proof.Identity, c.Identity) || proof.State != domain.VMArtifactDeleted || proof.ManifestDigest != c.ManifestDigest || !reflect.DeepEqual(proof.Components, c.Components) {
				return domain.ErrInvalidValue
			}
			for _, state := range []domain.VMArtifactState{domain.VMArtifactDeleting, domain.VMArtifactDeleted} {
				next := *c
				next.Generation++
				next.State = state
				if err := r.Checkpoints().Update(ctx, &next, c.Generation); err != nil {
					return err
				}
				c = &next
			}
		case domain.VMDeleteExport:
			e := q.Export
			proof := result.Export
			if e == nil || proof == nil || domain.ValidateVMExport(proof) != nil || proof.ID != e.ID || proof.OrgID != e.OrgID || proof.CheckpointID != e.CheckpointID || proof.StorageRef != e.StorageRef || proof.State != domain.VMArtifactDeleted || proof.ManifestDigest != e.ManifestDigest || !reflect.DeepEqual(proof.Components, e.Components) {
				return domain.ErrInvalidValue
			}
			for _, state := range []domain.VMArtifactState{domain.VMArtifactDeleting, domain.VMArtifactDeleted} {
				next := *e
				next.Generation++
				next.State = state
				if err := r.Exports().Update(ctx, &next, e.Generation); err != nil {
					return err
				}
				e = &next
			}
		}
	}
	return nil
}
func (w *VMOperationWorker) verify(ctx context.Context, h domain.VirtualizationHost, v domain.PersistentVMDeployment, op *domain.VMOperation, result *domain.VMProviderResult) error {
	r := w.service.cfg.Repository
	if op.CloneTargetID != nil {
		target, err := r.Deployments().Get(ctx, v.OrgID, *op.CloneTargetID)
		if err != nil {
			return err
		}
		v = *target
	}
	observation, err := vmInspect(ctx, w.service.cfg.Provider, h, v.Identity)
	if err != nil {
		return w.unconfirmed(ctx, op)
	}
	if _, err = PersistVMInspection(ctx, r, v, *observation, w.service.cfg.Now()); err != nil {
		return w.unconfirmed(ctx, op)
	}
	artifact := op.Kind == domain.VMOperationCheckpoint || op.Kind == domain.VMOperationExport || (op.Kind == domain.VMOperationDelete && op.DeleteTarget != domain.VMDeleteDeployment)
	if artifact {
		ready := false
		if op.Kind == domain.VMOperationCheckpoint || op.DeleteTarget == domain.VMDeleteCheckpoint {
			c, e := r.Checkpoints().Get(ctx, v.OrgID, *op.CheckpointID)
			if e != nil {
				return e
			}
			ready = c.State == domain.VMArtifactReady && c.DeploymentGeneration == op.ResourceGeneration
			if op.Kind == domain.VMOperationDelete {
				ready = c.State == domain.VMArtifactDeleted
			}
		}
		if op.Kind == domain.VMOperationExport || op.DeleteTarget == domain.VMDeleteExport {
			e, err := r.Exports().Get(ctx, v.OrgID, *op.ExportID)
			if err != nil {
				return err
			}
			ready = e.State == domain.VMArtifactReady
			if op.Kind == domain.VMOperationDelete {
				ready = e.State == domain.VMArtifactDeleted
			}
		}
		if !ready {
			return w.unconfirmed(ctx, op)
		}
	} else {
		if !vmOperationSatisfied(v, *op, *observation) {
			return w.unconfirmed(ctx, op)
		}
		switch op.Kind {
		case domain.VMOperationDefine, domain.VMOperationAdopt, domain.VMOperationClone, domain.VMOperationRestore:
			image, err := r.Images().Get(ctx, v.OrgID, v.ImageID)
			if err != nil {
				return w.unconfirmed(ctx, op)
			}
			if observation.Drift == domain.VMDriftDrifted || observation.AppliedConfigDigest != v.ConfigDigest || observation.AppliedImageDigest != image.ManifestDigest {
				return w.unconfirmed(ctx, op)
			}
		}
	}
	completed, err := w.transition(ctx, op, domain.VMOperationSucceeded, "", []uuid.UUID{})
	if err != nil {
		return w.unconfirmed(ctx, op)
	}
	if completed.Kind == domain.VMOperationDelete {
		return w.releaseDeleted(ctx, v, *completed)
	}
	return nil
}
func vmOperationSatisfied(v domain.PersistentVMDeployment, op domain.VMOperation, o domain.VMObservation) bool {
	if o.RuntimeState == nil || o.Availability != domain.VMObservationAvailable || !reflect.DeepEqual(v.Identity, o.Identity) {
		return false
	}
	state := *o.RuntimeState
	if op.Kind == domain.VMOperationDelete {
		return state == domain.VMRuntimeAbsent
	}
	m := o.Marker
	if o.Ownership != domain.VMOwned || m == nil || m.OperationID != op.ID || m.AppliedGeneration != v.Generation || !reflect.DeepEqual(m.VMResourceIdentity, v.Identity) {
		return false
	}
	switch op.Kind {
	case domain.VMOperationStart, domain.VMOperationReboot:
		return state == domain.VMRuntimeRunning
	case domain.VMOperationGracefulStop, domain.VMOperationClone, domain.VMOperationRestore:
		return state == domain.VMRuntimeStopped
	default:
		return state == domain.VMRuntimeStopped || state == domain.VMRuntimeRunning
	}
}
func (w *VMOperationWorker) releaseDeleted(ctx context.Context, v domain.PersistentVMDeployment, op domain.VMOperation) error {
	id, kind := op.ResourceID, domain.PersistentVMResource
	if op.DeleteTarget == domain.VMDeleteCheckpoint {
		id = *op.CheckpointID
		kind = domain.VMCheckpointResource
	}
	if op.DeleteTarget == domain.VMDeleteExport {
		id = *op.ExportID
		kind = domain.VMExportResource
	}
	rows, err := w.service.cfg.Repository.ListReservations(ctx, v.OrgID, v.HostID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ResourceID == id && row.ResourceKind == kind {
			err := w.service.cfg.Repository.ReleaseCapacity(ctx, v.OrgID, row.ID)
			if err == nil {
				w.service.changed(ctx, v.OrgID, kind, id)
			}
			return err
		}
	}
	return nil
}

// PersistVMInspection stamps a synchronous inspection against the revision that
// was inspected. Asynchronous provider messages must retain their original stamp
// and use AcceptVMObservation directly, never this function to relabel stale data.
func PersistVMInspection(ctx context.Context, r repository.VirtualizationRepository, v domain.PersistentVMDeployment, o domain.VMObservation, now time.Time) (*domain.VMObservation, error) {
	if !reflect.DeepEqual(o.Identity, v.Identity) || o.LifecycleClass != v.LifecycleClass {
		return nil, domain.ErrInvalidValue
	}
	cursor := v.ObservationCursor
	if cursor == nil {
		cursor = &domain.VMObservationCursor{SessionID: uuid.New()}
		if err := r.RotateObservationSession(ctx, repository.VirtualizationResourceRef{OrgID: v.OrgID, Kind: domain.PersistentVMResource, ID: v.ID}, v.Generation, uuid.Nil, cursor.SessionID); err != nil {
			return nil, err
		}
	}
	o.VMObservationStamp = domain.VMObservationStamp{SchemaVersion: 1, ObservedGeneration: v.Generation, SessionID: cursor.SessionID, Sequence: cursor.Sequence + 1, ObservedAt: now.UTC()}
	if o.Availability == domain.VMObservationUnavailable {
		o.Drift = domain.VMDriftUnknown
		o.GuestHealth = domain.VMGuestUnknown
		o.Usage = nil
		o.Connections = nil
		o.ConsoleAvailable = false
		o.RuntimeState = nil
		o.RuntimeObservedAt = nil
		if v.Observation != nil {
			o.RuntimeState = v.Observation.RuntimeState
			o.RuntimeObservedAt = v.Observation.RuntimeObservedAt
		}
	} else if o.RuntimeObservedAt == nil {
		o.RuntimeObservedAt = &o.ObservedAt
	}
	if o.RuntimeState != nil && *o.RuntimeState == domain.VMRuntimeAbsent {
		o.Ownership = domain.VMOwnershipUnknown
		o.Marker = nil
	}
	if err := domain.ValidateVMObservation(&v, &o); err != nil {
		return nil, err
	}
	if err := r.AcceptVMObservation(ctx, v.OrgID, v.ID, o); err != nil {
		return nil, err
	}
	return &o, nil
}
