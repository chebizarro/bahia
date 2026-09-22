package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/domain"
)

const vmApprovalColumns = `id,org_id,resource_id,lifecycle_class,generation,request_hash,provider_fingerprint,tier,requester,approver,reason,created_at,expires_at,consumed_at,schema_version`

func (r *PgVirtualizationRepository) CreateApproval(ctx context.Context, a *domain.VMApproval) error {
	if err := domain.ValidateVMApproval(a); err != nil {
		return err
	}
	if a.ConsumedAt != nil || !a.ExpiresAt.After(time.Now()) || a.CreatedAt.After(time.Now()) {
		return domain.ErrInvalidValue
	}
	return r.vmTx(ctx, a.OrgID, func(tx pgx.Tx) error {
		v, err := vmRead[domain.PersistentVMDeployment](ctx, tx, domain.PersistentVMResource, a.OrgID, a.ResourceID, true)
		if err != nil {
			return err
		}
		if v.Generation != a.Generation {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO vm_operation_approvals (`+vmApprovalColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULL,1)`, a.ID, a.OrgID, a.ResourceID, a.LifecycleClass, a.Generation, a.RequestHash, a.ProviderFingerprint, a.Tier, a.Requester, a.Approver, a.Reason, a.CreatedAt, a.ExpiresAt)
		if err != nil {
			return err
		}
		return vmJournalWithApproval(ctx, tx, VirtualizationResourceRef{v.OrgID, domain.PersistentVMResource, v.ID}, "approval_created", &a.ID)
	})
}
func vmReadApproval(ctx context.Context, q pgQueryer, org, id uuid.UUID, lock bool) (*domain.VMApproval, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	a := new(domain.VMApproval)
	err := q.QueryRow(ctx, `SELECT `+vmApprovalColumns+` FROM vm_operation_approvals WHERE org_id=$1 AND id=$2`+suffix, org, id).Scan(&a.ID, &a.OrgID, &a.ResourceID, &a.LifecycleClass, &a.Generation, &a.RequestHash, &a.ProviderFingerprint, &a.Tier, &a.Requester, &a.Approver, &a.Reason, &a.CreatedAt, &a.ExpiresAt, &a.ConsumedAt, &a.SchemaVersion)
	if err != nil {
		return nil, vmDBError(err)
	}
	if err = domain.ValidateVMApproval(a); err != nil {
		return nil, err
	}
	return a, nil
}
func (r *PgVirtualizationRepository) GetApproval(ctx context.Context, org, id uuid.UUID) (*domain.VMApproval, error) {
	return vmReadApproval(ctx, r.pool, org, id, false)
}
func vmConsumeApproval(ctx context.Context, q pgQueryer, o *domain.VMOperation) error {
	if o.RequiredTier != domain.VMApprovalDestructive {
		if o.ApprovalID != nil {
			return domain.ErrInvalidValue
		}
		return nil
	}
	if o.ApprovalID == nil {
		return fmt.Errorf("%w: %s", ErrConflict, domain.VMErrorApprovalRequired)
	}
	a, err := vmReadApproval(ctx, q, o.OrgID, *o.ApprovalID, true)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if a.ResourceID != o.ResourceID || a.LifecycleClass != o.LifecycleClass || a.Generation != o.ExpectedGeneration || a.RequestHash != o.RequestHash || a.ProviderFingerprint != o.ProviderFingerprint || a.Requester != o.Actor || a.Tier != o.RequiredTier || a.ConsumedAt != nil || !a.ExpiresAt.After(now) {
		return fmt.Errorf("%w: approval does not bind this request", ErrConflict)
	}
	if _, err = q.Exec(ctx, `UPDATE vm_operation_approvals SET consumed_at=$3 WHERE org_id=$1 AND id=$2 AND consumed_at IS NULL`, a.OrgID, a.ID, now); err != nil {
		return err
	}
	return vmJournalWithApproval(ctx, q, VirtualizationResourceRef{o.OrgID, domain.PersistentVMResource, o.ResourceID}, "approval_consumed", &a.ID)
}
func (r *PgVirtualizationRepository) AdmitOperation(ctx context.Context, input VMOperationAdmission) (*VMOperationAdmissionResult, error) {
	// Clone pointer-bearing input; aborted transactions do not mutate caller state.
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var a VMOperationAdmission
	if err = json.Unmarshal(encoded, &a); err != nil {
		return nil, err
	}
	o := &a.Operation
	if err = domain.ValidateVMOperation(o); err != nil {
		return nil, err
	}
	if o.Generation != 1 || o.ExpectedGeneration != a.ExpectedGeneration || (o.Phase != domain.VMOperationAccepted && o.Phase != domain.VMOperationAwaitingApproval) {
		return nil, domain.ErrInvalidValue
	}
	result := new(VMOperationAdmissionResult)
	err = r.vmTx(ctx, o.OrgID, func(tx pgx.Tx) error {
		var existingID uuid.UUID
		e := tx.QueryRow(ctx, `SELECT id FROM vm_operations WHERE org_id=$1 AND idempotency_key=$2`, o.OrgID, o.IdempotencyKey).Scan(&existingID)
		if e == nil {
			existing, e := vmRead[domain.VMOperation](ctx, tx, domain.VMOperationResource, o.OrgID, existingID, false)
			if e != nil {
				return e
			}
			if existing.RequestHash != o.RequestHash || existing.ResourceID != o.ResourceID || existing.Actor != o.Actor || existing.Kind != o.Kind || existing.ExpectedGeneration != o.ExpectedGeneration || existing.ResourceGeneration != o.ResourceGeneration || existing.AllowForceStop != o.AllowForceStop || existing.DeleteTarget != o.DeleteTarget || existing.DataDisposition.Effective() != o.DataDisposition.Effective() || !reflect.DeepEqual(existing.CheckpointID, o.CheckpointID) || !reflect.DeepEqual(existing.ExportID, o.ExportID) || !reflect.DeepEqual(existing.CloneTargetID, o.CloneTargetID) {
				return ErrConflict
			}
			result.Operation = *existing
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if !o.Deadline.After(time.Now()) {
			return domain.ErrInvalidValue
		}
		// Same advisory key as WithOperationLock: no revision may be admitted while a
		// provider operation is executing, even if an external caller marked it terminal.
		var locked bool
		if e = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,67))`, o.OrgID.String()+":"+o.ResourceID.String()).Scan(&locked); e != nil {
			return e
		}
		if !locked {
			return ErrConflict
		}
		if o.Phase == domain.VMOperationAwaitingApproval && (a.Desired != nil || len(a.Reservations) != 0 || o.RequiredTier != domain.VMApprovalDestructive || o.ApprovalID != nil) {
			return domain.ErrInvalidValue
		}
		if a.Desired != nil {
			if a.Desired.ID != o.ResourceID || a.Desired.OrgID != o.OrgID || a.Desired.Generation != o.ResourceGeneration {
				return domain.ErrInvalidValue
			}
			if a.ExpectedGeneration == 0 {
				if o.Kind != domain.VMOperationDefine {
					return domain.ErrInvalidValue
				}
				if e = vmCreate(ctx, tx, domain.PersistentVMResource, a.Desired); e != nil {
					return e
				}
			} else if e = vmUpdate(ctx, tx, domain.PersistentVMResource, a.Desired, a.ExpectedGeneration, o.RequiredTier == domain.VMApprovalDestructive && o.Kind == domain.VMOperationDefine && o.Plan != nil && o.Plan.RecreateRequired); e != nil {
				return e
			}
		} else if a.ExpectedGeneration != o.ResourceGeneration {
			return domain.ErrInvalidValue
		}
		v, e := vmRead[domain.PersistentVMDeployment](ctx, tx, domain.PersistentVMResource, o.OrgID, o.ResourceID, true)
		if e != nil {
			return e
		}
		if v.Generation != o.ResourceGeneration {
			return ErrConflict
		}
		if e = vmOperationReferences(ctx, tx, o, v); e != nil {
			return e
		}
		for _, res := range a.Reservations {
			if res.OrgID != o.OrgID || res.HostID != v.HostID || !vmOperationReservationTarget(o, res) {
				return domain.ErrInvalidValue
			}
			if e = vmReserve(ctx, tx, res); e != nil {
				return e
			}
		}
		if o.Phase == domain.VMOperationAccepted {
			if e = vmRequireOperationCapacity(ctx, tx, o, v); e != nil {
				return e
			}
			if e = vmConsumeApproval(ctx, tx, o); e != nil {
				return e
			}
		}
		if e = vmCreate(ctx, tx, domain.VMOperationResource, o); e != nil {
			return e
		}
		result.Operation = *o
		result.Created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func vmOperationReservationTarget(o *domain.VMOperation, r domain.VMCapacityReservation) bool {
	switch r.ResourceKind {
	case domain.PersistentVMResource:
		return r.ResourceID == o.ResourceID || (o.CloneTargetID != nil && r.ResourceID == *o.CloneTargetID)
	case domain.VMCheckpointResource:
		return o.CheckpointID != nil && r.ResourceID == *o.CheckpointID
	case domain.VMExportResource:
		return o.ExportID != nil && r.ResourceID == *o.ExportID
	}
	return false
}
func vmOperationReferences(ctx context.Context, q pgQueryer, o *domain.VMOperation, v *domain.PersistentVMDeployment) error {
	if o.CheckpointID != nil {
		c, err := vmRead[domain.VMCheckpoint](ctx, q, domain.VMCheckpointResource, o.OrgID, *o.CheckpointID, false)
		if err != nil {
			return err
		}
		if c.DeploymentID != v.ID {
			return domain.ErrInvalidValue
		}
		if o.Kind != domain.VMOperationCheckpoint && o.Kind != domain.VMOperationDelete && c.State != domain.VMArtifactReady {
			return domain.ErrInvalidValue
		}
	}
	if o.ExportID != nil {
		e, err := vmRead[domain.VMExport](ctx, q, domain.VMExportResource, o.OrgID, *o.ExportID, false)
		if err != nil {
			return err
		}
		if o.CheckpointID == nil || e.CheckpointID != *o.CheckpointID {
			return domain.ErrInvalidValue
		}
	}
	if o.CloneTargetID != nil {
		c, err := vmRead[domain.PersistentVMDeployment](ctx, q, domain.PersistentVMResource, o.OrgID, *o.CloneTargetID, false)
		if err != nil {
			return err
		}
		if c.ID == v.ID || c.HostID != v.HostID || c.DesiredPower != domain.VMDesiredStopped || c.Identity.ProviderResourceID == v.Identity.ProviderResourceID {
			return domain.ErrInvalidValue
		}
	}
	return nil
}
func vmRequireOperationCapacity(ctx context.Context, q pgQueryer, o *domain.VMOperation, v *domain.PersistentVMDeployment) error {
	// Cleanup can finish after the parent VM's capacity has been released.
	if o.Kind == domain.VMOperationDelete && o.DeleteTarget != domain.VMDeleteDeployment {
		return nil
	}
	var reserved domain.VMCapacity
	err := q.QueryRow(ctx, `SELECT vcpu,memory_bytes,disk_bytes FROM virtualization_capacity_reservations WHERE org_id=$1 AND resource_kind='persistent_vm' AND resource_id=$2 AND host_id=$3`, v.OrgID, v.ID, v.HostID).Scan(&reserved.VCPU, &reserved.MemoryBytes, &reserved.DiskBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: persistent capacity reservation required", ErrConflict)
	}
	if err != nil {
		return err
	}
	if !v.Allocation.Fits(reserved) {
		return ErrConflict
	}
	var target *uuid.UUID
	var kind domain.VirtualizationResourceKind
	switch o.Kind {
	case domain.VMOperationCheckpoint:
		target = o.CheckpointID
		kind = domain.VMCheckpointResource
	case domain.VMOperationExport:
		target = o.ExportID
		kind = domain.VMExportResource
	case domain.VMOperationClone:
		target = o.CloneTargetID
		kind = domain.PersistentVMResource
	}
	if target != nil {
		var exists bool
		if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM virtualization_capacity_reservations WHERE org_id=$1 AND resource_kind=$2 AND resource_id=$3)`, o.OrgID, kind, *target).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: output capacity reservation required", ErrConflict)
		}
	}
	return nil
}
func (r *PgVirtualizationRepository) GetOperation(ctx context.Context, org, id uuid.UUID) (*domain.VMOperation, error) {
	return vmRead[domain.VMOperation](ctx, r.pool, domain.VMOperationResource, org, id, false)
}
func (r *PgVirtualizationRepository) ListOperations(ctx context.Context, org, resource uuid.UUID, limit, offset int) ([]domain.VMOperation, error) {
	if org == uuid.Nil || vmPage(limit, offset) != nil {
		return nil, domain.ErrInvalidValue
	}
	rows, err := r.pool.Query(ctx, `SELECT document FROM vm_operations WHERE org_id=$1 AND ($2='00000000-0000-0000-0000-000000000000'::uuid OR resource_id=$2) ORDER BY created_at,id LIMIT $3 OFFSET $4`, org, resource, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.VMOperation{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var o domain.VMOperation
		if err = domain.DecodeVirtualizationDocument(data, &o); err != nil {
			return nil, err
		}
		if err = domain.ValidateVMOperation(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func (r *PgVirtualizationRepository) TransitionOperation(ctx context.Context, t VMOperationTransition) (*domain.VMOperation, error) {
	var out *domain.VMOperation
	err := r.vmTx(ctx, t.OrgID, func(tx pgx.Tx) error {
		o, err := vmRead[domain.VMOperation](ctx, tx, domain.VMOperationResource, t.OrgID, t.OperationID, true)
		if err != nil {
			return err
		}
		if o.Phase != t.ExpectedPhase || o.Generation != t.ExpectedRevision {
			return ErrConflict
		}
		if !domain.VMOperationTransitionAllowed(o.Phase, t.Phase) {
			return domain.ErrInvalidValue
		}
		v, err := vmRead[domain.PersistentVMDeployment](ctx, tx, domain.PersistentVMResource, o.OrgID, o.ResourceID, true)
		if err != nil {
			return err
		}
		if v.Generation != o.ResourceGeneration {
			return ErrConflict
		}
		if t.Phase == domain.VMOperationAccepted {
			o.ApprovalID = t.ApprovalID
			if err = vmRequireOperationCapacity(ctx, tx, o, v); err != nil {
				return err
			}
			if err = vmConsumeApproval(ctx, tx, o); err != nil {
				return err
			}
		} else if t.ApprovalID != nil {
			return domain.ErrInvalidValue
		}
		if t.Phase == domain.VMOperationExecuting && !o.Deadline.After(time.Now()) {
			return ErrConflict
		}
		if t.Phase == domain.VMOperationSucceeded {
			if err = vmVerifyOperationCompletion(ctx, tx, o, v); err != nil {
				return err
			}
		}
		o.Phase = t.Phase
		o.Generation++
		o.UpdatedAt = time.Now().UTC()
		o.Outcome = t.Outcome
		if t.PreparedStorageRefs != nil {
			o.PreparedStorageRefs = t.PreparedStorageRefs
		}
		if o.Phase.Terminal() {
			completed := o.UpdatedAt
			o.CompletedAt = &completed
		}
		vmNormalize(o)
		if err = domain.ValidateVMOperation(o); err != nil {
			return err
		}
		data, err := json.Marshal(o)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE vm_operations SET generation=$3,updated_at=$4,document=$5,completed_at=($5::jsonb->>'completed_at')::timestamptz WHERE org_id=$1 AND id=$2 AND generation=$6 AND phase=$7`, o.OrgID, o.ID, o.Generation, o.UpdatedAt, data, t.ExpectedRevision, t.ExpectedPhase)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if err = vmJournal(ctx, tx, domain.VMOperationResource, o, "updated"); err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func vmVerifyOperationCompletion(ctx context.Context, q pgQueryer, o *domain.VMOperation, v *domain.PersistentVMDeployment) error {
	if o.Kind == domain.VMOperationDelete && o.DeleteTarget == domain.VMDeleteCheckpoint {
		c, err := vmRead[domain.VMCheckpoint](ctx, q, domain.VMCheckpointResource, o.OrgID, *o.CheckpointID, false)
		if err != nil {
			return err
		}
		if c.State != domain.VMArtifactDeleted {
			return ErrConflict
		}
		return nil
	}
	if o.Kind == domain.VMOperationDelete && o.DeleteTarget == domain.VMDeleteExport {
		e, err := vmRead[domain.VMExport](ctx, q, domain.VMExportResource, o.OrgID, *o.ExportID, false)
		if err != nil {
			return err
		}
		if e.State != domain.VMArtifactDeleted {
			return ErrConflict
		}
		return nil
	}
	switch o.Kind {
	case domain.VMOperationCheckpoint:
		c, err := vmRead[domain.VMCheckpoint](ctx, q, domain.VMCheckpointResource, o.OrgID, *o.CheckpointID, false)
		if err != nil {
			return err
		}
		if c.State != domain.VMArtifactReady || c.DeploymentGeneration != o.ResourceGeneration {
			return ErrConflict
		}
		return nil
	case domain.VMOperationExport:
		e, err := vmRead[domain.VMExport](ctx, q, domain.VMExportResource, o.OrgID, *o.ExportID, false)
		if err != nil {
			return err
		}
		if e.State != domain.VMArtifactReady {
			return ErrConflict
		}
		return nil
	case domain.VMOperationClone:
		target, err := vmRead[domain.PersistentVMDeployment](ctx, q, domain.PersistentVMResource, o.OrgID, *o.CloneTargetID, false)
		if err != nil {
			return err
		}
		v = target
	}
	obs := v.Observation
	if obs == nil || obs.ObservedGeneration != v.Generation || obs.Availability != domain.VMObservationAvailable || obs.RuntimeState == nil || obs.ObservedAt.Before(o.CreatedAt) {
		return fmt.Errorf("%w: authoritative observation required", ErrConflict)
	}
	var session uuid.UUID
	if err := q.QueryRow(ctx, `SELECT observation_session FROM persistent_vm_deployments WHERE org_id=$1 AND id=$2`, v.OrgID, v.ID).Scan(&session); err != nil {
		return err
	}
	if obs.SessionID != session {
		return ErrConflict
	}
	if o.Kind == domain.VMOperationDelete {
		if *obs.RuntimeState != domain.VMRuntimeAbsent {
			return ErrConflict
		}
		return nil
	}
	if obs.Ownership != domain.VMOwned || obs.Marker == nil || obs.Marker.OperationID != o.ID || obs.Marker.AppliedGeneration != v.Generation || !reflect.DeepEqual(obs.Identity, v.Identity) {
		return ErrConflict
	}
	switch o.Kind {
	case domain.VMOperationStart, domain.VMOperationReboot:
		if *obs.RuntimeState != domain.VMRuntimeRunning {
			return ErrConflict
		}
	case domain.VMOperationGracefulStop, domain.VMOperationClone, domain.VMOperationRestore:
		if *obs.RuntimeState != domain.VMRuntimeStopped {
			return ErrConflict
		}
	default:
		if *obs.RuntimeState == domain.VMRuntimeAbsent || *obs.RuntimeState == domain.VMRuntimeFailed {
			return ErrConflict
		}
	}
	return nil
}
func (r *PgVirtualizationRepository) WithOperationLock(ctx context.Context, org, resource uuid.UUID, fn func(context.Context) error) (resultErr error) {
	if org == uuid.Nil || resource == uuid.Nil || fn == nil {
		return domain.ErrInvalidValue
	}
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	key := org.String() + ":" + resource.String()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,67))`, key).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrConflict
	}
	// An unlock failure discards the connection, rather than returning a locked
	// session to the pool. This lock cannot fence provider effects after DB loss.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var unlocked bool
		e := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,67))`, key).Scan(&unlocked)
		if e != nil || !unlocked {
			closeErr := conn.Conn().Close(cleanup)
			resultErr = errors.Join(resultErr, &domain.VMProviderError{Code: domain.VMErrorUnconfirmed, Unconfirmed: true, Cause: errors.Join(e, closeErr)})
		}
	}()
	return fn(ctx)
}
func (r *PgVirtualizationRepository) ListChanges(ctx context.Context, org uuid.UUID, after int64, limit int) ([]VirtualizationResourceChange, error) {
	if org == uuid.Nil || after < 0 || vmPage(limit, 0) != nil {
		return nil, domain.ErrInvalidValue
	}
	rows, err := r.pool.Query(ctx, `SELECT sequence,schema_version,org_id,resource_kind,resource_id,generation,lifecycle_classes,change_type,document,observation,approval_id,occurred_at FROM virtualization_resource_changes WHERE org_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, org, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VirtualizationResourceChange{}
	for rows.Next() {
		var c VirtualizationResourceChange
		var classes []byte
		if err = rows.Scan(&c.Sequence, &c.SchemaVersion, &c.OrgID, &c.ResourceKind, &c.ResourceID, &c.Generation, &classes, &c.ChangeType, &c.Document, &c.Observation, &c.ApprovalID, &c.OccurredAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(classes, &c.LifecycleClasses); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
