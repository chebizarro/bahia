//go:build integration

package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func vmPGOperation(v *domain.PersistentVMDeployment, kind domain.VMOperationKind) domain.VMOperation {
	operation := domain.VMOperation{VirtualizationResourceMeta: vmPGMeta(v.OrgID), LifecycleClass: v.LifecycleClass, ResourceID: v.ID, ExpectedGeneration: v.Generation, ResourceGeneration: v.Generation, IdempotencyKey: uuid.NewString(), RequestHash: vmPGDigest(), Actor: "operator", Reason: "requested operation", Kind: kind, Phase: domain.VMOperationAccepted, RequiredTier: domain.MinimumVMApprovalTier(kind), ProviderFingerprint: vmPGDigest(), ProviderCorrelationID: uuid.New(), Deadline: time.Now().UTC().Add(time.Hour)}
	if kind == domain.VMOperationDelete {
		operation.DeleteTarget = domain.VMDeleteDeployment
	}
	return operation
}
func vmPGApproval(t *testing.T, r *PgVirtualizationRepository, o domain.VMOperation) *domain.VMApproval {
	t.Helper()
	now := time.Now().UTC()
	a := &domain.VMApproval{SchemaVersion: 1, ID: uuid.New(), OrgID: o.OrgID, ResourceID: o.ResourceID, LifecycleClass: o.LifecycleClass, Generation: o.ExpectedGeneration, RequestHash: o.RequestHash, ProviderFingerprint: o.ProviderFingerprint, Tier: domain.VMApprovalDestructive, Requester: o.Actor, Approver: "second-operator", Reason: "approved requested operation", CreatedAt: now, ExpiresAt: now.Add(domain.VMApprovalMaxAge)}
	require.NoError(t, r.CreateApproval(context.Background(), a))
	return a
}
func vmPGTransition(t *testing.T, r *PgVirtualizationRepository, o *domain.VMOperation, phase domain.VMOperationPhase) *domain.VMOperation {
	t.Helper()
	out, err := r.TransitionOperation(context.Background(), VMOperationTransition{OrgID: o.OrgID, OperationID: o.ID, ExpectedPhase: o.Phase, ExpectedRevision: o.Generation, Phase: phase})
	require.NoError(t, err)
	return out
}
func TestVMControlPlanePostgresAdmissionReplayApprovalAndCompletion(t *testing.T) {
	pool, r := vmPostgres(t)
	_, _, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	require.NoError(t, r.ReserveCapacity(ctx, vmPGReservation(v)))
	op := vmPGOperation(v, domain.VMOperationDelete)
	approval := vmPGApproval(t, r, op)
	op.ApprovalID = &approval.ID
	input := VMOperationAdmission{Operation: op, ExpectedGeneration: 1}
	first, err := r.AdmitOperation(ctx, input)
	require.NoError(t, err)
	require.True(t, first.Created)
	replay, err := r.AdmitOperation(ctx, input)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, first.Operation.ID, replay.Operation.ID)
	stored, err := r.GetApproval(ctx, v.OrgID, approval.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ConsumedAt)
	mismatch := input
	mismatch.Operation.RequestHash = "sha256:" + strings.Repeat("f", 64)
	_, err = r.AdmitOperation(ctx, mismatch)
	require.ErrorIs(t, err, ErrConflict)
	overlap := VMOperationAdmission{Operation: vmPGOperation(v, domain.VMOperationStart), ExpectedGeneration: 1}
	_, err = r.AdmitOperation(ctx, overlap)
	require.ErrorIs(t, err, ErrConflict)
	_, err = r.TransitionOperation(ctx, VMOperationTransition{OrgID: op.OrgID, OperationID: op.ID, ExpectedPhase: domain.VMOperationAccepted, ExpectedRevision: 1, Phase: domain.VMOperationSucceeded})
	require.Error(t, err)
	executing := vmPGTransition(t, r, &first.Operation, domain.VMOperationExecuting)
	uncertain := vmPGTransition(t, r, executing, domain.VMOperationUnconfirmed)
	_, err = r.AdmitOperation(ctx, overlap)
	require.ErrorIs(t, err, ErrConflict, "unconfirmed retains exclusive slot")
	verifying := vmPGTransition(t, r, uncertain, domain.VMOperationVerifying)
	_, err = r.TransitionOperation(ctx, VMOperationTransition{OrgID: op.OrgID, OperationID: op.ID, ExpectedPhase: verifying.Phase, ExpectedRevision: verifying.Generation, Phase: domain.VMOperationSucceeded})
	require.ErrorIs(t, err, ErrConflict, "acknowledgment is not completion")
	vmPGObserve(t, r, v, op.ID, domain.VMRuntimeAbsent)
	done := vmPGTransition(t, r, verifying, domain.VMOperationSucceeded)
	require.NotNil(t, done.CompletedAt)
	reuse := vmPGOperation(v, domain.VMOperationDelete)
	reuse.ApprovalID = &approval.ID
	_, err = r.AdmitOperation(ctx, VMOperationAdmission{Operation: reuse, ExpectedGeneration: 1})
	require.ErrorIs(t, err, ErrConflict, "approval single use")
}
func TestVMControlPlanePostgresAdmissionAtomicRollback(t *testing.T) {
	pool, r := vmPostgres(t)
	h, i, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	before, err := r.ListChanges(ctx, h.OrgID, 0, 100)
	require.NoError(t, err)
	next := vmPGDeployment(h, i)
	next.Allocation.VCPU = 17
	op := vmPGOperation(next, domain.VMOperationDefine)
	op.ExpectedGeneration = 0
	_, err = r.AdmitOperation(ctx, VMOperationAdmission{Operation: op, ExpectedGeneration: 0, Desired: next, Reservations: []domain.VMCapacityReservation{vmPGReservation(next)}})
	require.ErrorIs(t, err, ErrConflict)
	_, err = r.Deployments().Get(ctx, next.OrgID, next.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = r.GetOperation(ctx, op.OrgID, op.ID)
	require.ErrorIs(t, err, ErrNotFound)
	after, err := r.ListChanges(ctx, h.OrgID, 0, 100)
	require.NoError(t, err)
	require.Equal(t, before, after, "failed transaction has no change history")
	require.NoError(t, r.ReserveCapacity(ctx, vmPGReservation(v)))
	active := vmPGOperation(v, domain.VMOperationStart)
	_, err = r.AdmitOperation(ctx, VMOperationAdmission{Operation: active, ExpectedGeneration: 1})
	require.NoError(t, err)
	destructive := vmPGOperation(v, domain.VMOperationDelete)
	approval := vmPGApproval(t, r, destructive)
	destructive.ApprovalID = &approval.ID
	_, err = r.AdmitOperation(ctx, VMOperationAdmission{Operation: destructive, ExpectedGeneration: 1})
	require.ErrorIs(t, err, ErrConflict)
	a, err := r.GetApproval(ctx, v.OrgID, approval.ID)
	require.NoError(t, err)
	require.Nil(t, a.ConsumedAt, "overlap failure must roll approval consumption back")
}
func TestVMControlPlanePostgresConcurrentDedupAndOperationLock(t *testing.T) {
	pool, r := vmPostgres(t)
	_, _, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	require.NoError(t, r.ReserveCapacity(ctx, vmPGReservation(v)))
	op := vmPGOperation(v, domain.VMOperationStart)
	input := VMOperationAdmission{Operation: op, ExpectedGeneration: 1}
	start := make(chan struct{})
	type result struct {
		value *VMOperationAdmissionResult
		err   error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			value, err := r.AdmitOperation(ctx, input)
			results <- result{value, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for res := range results {
		require.NoError(t, res.err)
		if res.value.Created {
			created++
		}
		require.Equal(t, op.ID, res.value.Operation.ID)
	}
	require.Equal(t, 1, created)
	vmPGTransition(t, r, &op, domain.VMOperationCancelled)
	next := vmPGOperation(v, domain.VMOperationStart)
	require.NoError(t, r.WithOperationLock(ctx, v.OrgID, v.ID, func(ctx context.Context) error {
		_, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: next, ExpectedGeneration: 1})
		require.ErrorIs(t, err, ErrConflict)
		require.ErrorIs(t, r.WithOperationLock(ctx, v.OrgID, v.ID, func(context.Context) error { t.Fatal("overlapping provider executed"); return nil }), ErrConflict)
		return nil
	}))
	_, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: next, ExpectedGeneration: 1})
	require.NoError(t, err)
}
func TestVMControlPlanePostgresApprovedRecreateKeepsResourceID(t *testing.T) {
	pool, r := vmPostgres(t)
	_, _, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	reservation := vmPGReservation(v)
	require.NoError(t, r.ReserveCapacity(ctx, reservation))
	desired := *v
	desired.Generation = 2
	desired.Identity.ProviderResourceID = uuid.New()
	require.ErrorIs(t, r.Deployments().Update(ctx, &desired, 1), ErrConflict, "plain updates cannot rebind ownership")
	op := vmPGOperation(v, domain.VMOperationDefine)
	op.ResourceGeneration = 2
	op.RequiredTier = domain.VMApprovalDestructive
	op.Plan = &domain.VMChangePlan{LifecycleClass: v.LifecycleClass, ExpectedGeneration: 1, CurrentConfigDigest: v.ConfigDigest, DesiredConfigDigest: desired.ConfigDigest, RequiredTier: domain.VMApprovalDestructive, RecreateRequired: true}
	approval := vmPGApproval(t, r, op)
	op.ApprovalID = &approval.ID
	admission := VMOperationAdmission{Operation: op, ExpectedGeneration: 1, Desired: &desired, Reservations: []domain.VMCapacityReservation{vmPGReservation(&desired)}}
	_, err := r.AdmitOperation(ctx, admission)
	require.ErrorIs(t, err, ErrConflict, "approval alone is not proof old resource is gone")
	vmPGObserve(t, r, v, uuid.New(), domain.VMRuntimeAbsent)
	require.NoError(t, r.ReleaseCapacity(ctx, v.OrgID, reservation.ID))
	admitted, err := r.AdmitOperation(ctx, admission)
	require.NoError(t, err)
	require.True(t, admitted.Created)
	stored, err := r.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.Equal(t, v.ID, stored.ID)
	require.Equal(t, desired.Identity, stored.Identity)
	require.Equal(t, int64(2), stored.Generation)
	require.Nil(t, stored.Observation, "old provider observations must not leak into rebound identity")
}

func TestVMControlPlanePostgresApprovalBindingAndAwaiting(t *testing.T) {
	pool, r := vmPostgres(t)
	_, _, v := vmPGFixtures(t, pool, r)
	ctx := context.Background()
	require.NoError(t, r.ReserveCapacity(ctx, vmPGReservation(v)))
	op := vmPGOperation(v, domain.VMOperationDelete)
	a := vmPGApproval(t, r, op)
	op.Phase = domain.VMOperationAwaitingApproval
	admitted, err := r.AdmitOperation(ctx, VMOperationAdmission{Operation: op, ExpectedGeneration: 1})
	require.NoError(t, err)
	bad := *a
	bad.ID = uuid.New()
	bad.RequestHash = "sha256:" + strings.Repeat("e", 64)
	require.NoError(t, r.CreateApproval(ctx, &bad))
	transition := VMOperationTransition{OrgID: op.OrgID, OperationID: op.ID, ExpectedPhase: op.Phase, ExpectedRevision: 1, Phase: domain.VMOperationAccepted, ApprovalID: &bad.ID}
	_, err = r.TransitionOperation(ctx, transition)
	require.ErrorIs(t, err, ErrConflict)
	transition.ApprovalID = &a.ID
	accepted, err := r.TransitionOperation(ctx, transition)
	require.NoError(t, err)
	require.Equal(t, admitted.Operation.ID, accepted.ID)
	_, err = r.TransitionOperation(ctx, transition)
	require.ErrorIs(t, err, ErrConflict, "phase CAS")
	a, err = r.GetApproval(ctx, a.OrgID, a.ID)
	require.NoError(t, err)
	require.NotNil(t, a.ConsumedAt)
}
