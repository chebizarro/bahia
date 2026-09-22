package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestVMAdoptionApprovalBindsWritableMeasurement(t *testing.T) {
	for _, phase := range []string{"admission", "execution"} {
		t.Run(phase, func(t *testing.T) {
			s, r, p, v, principal := vmFixture(t)
			ctx := context.Background()
			state := domain.VMRuntimeStopped
			p.observations[v.Identity.ProviderResourceID] = domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
			require.NoError(t, s.RegisterAdoption(ctx, principal, v))
			req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "enroll", Reason: "enroll measured baseline", Kind: domain.VMOperationAdopt}
			approver := *principal
			approver.PubKey, approver.Subject = "second-person", "second-person"
			a, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve exact storage")
			require.NoError(t, err)
			require.NotEmpty(t, a.AdoptionDigest)
			req.ApprovalID = &a.ID
			if phase == "admission" {
				p.measurementDigest = vmTestDigest("f")
				_, err = s.RequestOperation(ctx, principal, req)
				require.Error(t, err)
				stored, err := r.GetApproval(ctx, v.OrgID, a.ID)
				require.NoError(t, err)
				require.Nil(t, stored.ConsumedAt)
			} else {
				op, err := s.RequestOperation(ctx, principal, req)
				require.NoError(t, err)
				p.measurementDigest = vmTestDigest("f")
				worker, err := NewVMOperationWorker(s)
				require.NoError(t, err)
				require.Error(t, worker.Process(ctx, v.OrgID, op.ID))
			}
			require.Empty(t, p.executed, "stale writable measurement reached provider mutation")
		})
	}
}

func TestVMAdoptionPreInventoryRevisionRequiresMeasuredApproval(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	// Existing registration with a historical caller-selected pin, not an applied
	// inventory. Preparing an adoption must produce a separately approved revision.
	require.NoError(t, r.Deployments().Create(ctx, &v))
	state := domain.VMRuntimeStopped
	p.observations[v.Identity.ProviderResourceID] = domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "old-enrollment", Reason: "enroll old registration", Kind: domain.VMOperationAdopt}
	plan, err := s.PlanOperation(ctx, principal, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, plan.ResourceGeneration)
	require.NotEqual(t, v.ConfigDigest, plan.Adoption.ConfigDigest)
	stored, err := r.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.Equal(t, v.ConfigDigest, stored.ConfigDigest)
	approver := *principal
	approver.PubKey, approver.Subject = "second-person", "second-person"
	a, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve measured revision")
	require.NoError(t, err)
	req.ApprovalID = &a.ID
	op, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	stored, err = r.Deployments().Get(ctx, v.OrgID, v.ID)
	require.NoError(t, err)
	require.Equal(t, op.Adoption.ConfigDigest, stored.ConfigDigest)
	require.EqualValues(t, 2, stored.Generation)
	worker, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.NoError(t, worker.Process(ctx, v.OrgID, op.ID))
	require.Len(t, p.executed, 1)
}

func TestVMAdoptionExplicitSnapshotRevision(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	require.NoError(t, r.Deployments().Create(ctx, &v))
	state := domain.VMRuntimeStopped
	p.observations[v.Identity.ProviderResourceID] = domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
	image := r.images[v.ImageID]
	image.ID = uuid.New()
	image.ManifestDigest = vmTestDigest("f")
	r.images[image.ID] = image
	next := v
	next.Generation++
	next.ImageID = image.ID
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "snapshot-enrollment", Reason: "enroll trusted current snapshot", Kind: domain.VMOperationAdopt, Desired: &next}
	approver := *principal
	approver.PubKey, approver.Subject = "second-person", "second-person"
	a, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve measured snapshot")
	require.NoError(t, err)
	req.ApprovalID = &a.ID
	op, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, op.ResourceGeneration)
	require.Equal(t, image.ManifestDigest, op.Adoption.ImageDigest)
	w, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.NoError(t, w.Process(ctx, v.OrgID, op.ID))
}

func TestVMAdoptionRecoveryRemeasuresWithoutReplaying(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	state := domain.VMRuntimeStopped
	p.observations[v.Identity.ProviderResourceID] = domain.VMObservation{Identity: v.Identity, LifecycleClass: v.LifecycleClass, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
	require.NoError(t, s.RegisterAdoption(ctx, principal, v))
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "interrupted-enrollment", Reason: "enroll measured baseline", Kind: domain.VMOperationAdopt}
	approver := *principal
	approver.PubKey, approver.Subject = "second-person", "second-person"
	a, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve exact baseline")
	require.NoError(t, err)
	req.ApprovalID = &a.ID
	op, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	p.execute = func(_ context.Context, q domain.VMProviderOperation) (*domain.VMProviderResult, error) {
		p.complete(q)
		return nil, context.DeadlineExceeded
	}
	w, err := NewVMOperationWorker(s)
	require.NoError(t, err)
	require.Error(t, w.Process(ctx, v.OrgID, op.ID))
	p.measurementDigest = vmTestDigest("f")
	require.Error(t, w.Process(ctx, v.OrgID, op.ID))
	stored, err := r.GetOperation(ctx, v.OrgID, op.ID)
	require.NoError(t, err)
	require.Equal(t, domain.VMOperationUnconfirmed, stored.Phase)
	p.measurementDigest = ""
	require.NoError(t, w.Process(ctx, v.OrgID, op.ID))
	require.Len(t, p.executed, 1, "recovery replayed ownership mutation")
}
