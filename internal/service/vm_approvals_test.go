package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestVMApprovalTwoPersonBindingAndSingleUse(t *testing.T) {
	s, r, _, v, principal := vmFixture(t)
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(context.Background(), v.OrgID, created.ID))
	request := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "delete", Reason: "decommission", Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment}
	op, err := s.RequestOperation(context.Background(), principal, request)
	require.NoError(t, err)
	_, err = s.ApproveOperation(context.Background(), principal, v.OrgID, op.ID, "self approval")
	require.Error(t, err)
	require.Empty(t, r.approvals)
	approver := *principal
	approver.PubKey = "second-person"
	approver.Subject = "second-person"
	approval, err := s.ApproveOperation(context.Background(), &approver, v.OrgID, op.ID, "approved exact delete")
	require.NoError(t, err)
	stored, err := r.GetApproval(context.Background(), v.OrgID, approval.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.ConsumedAt)
	require.Equal(t, domain.VMApprovalMaxAge, stored.ExpiresAt.Sub(stored.CreatedAt))
	require.Equal(t, op.RequestHash, stored.RequestHash)
	_, err = s.ApproveOperation(context.Background(), &approver, v.OrgID, op.ID, "reuse")
	require.ErrorIs(t, err, repository.ErrConflict)
	require.NoError(t, w.Process(context.Background(), v.OrgID, op.ID))
	request.IdempotencyKey = "reuse"
	request.ApprovalID = &approval.ID
	_, err = s.RequestOperation(context.Background(), principal, request)
	require.Error(t, err)
}
func TestVMApprovalStaleFingerprintGenerationAndExpiry(t *testing.T) {
	for _, change := range []string{"fingerprint", "expiry", "generation"} {
		t.Run(change, func(t *testing.T) {
			s, r, p, v, principal := vmFixture(t)
			_, created := vmCreate(t, s, v, principal)
			w, _ := NewVMOperationWorker(s)
			require.NoError(t, w.Process(context.Background(), v.OrgID, created.ID))
			req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "delete", Reason: "decommission", Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment}
			op, err := s.RequestOperation(context.Background(), principal, req)
			require.NoError(t, err)
			approver := *principal
			approver.PubKey = "second-person"
			approver.Subject = "second-person"
			_, err = s.ApproveOperation(context.Background(), &approver, v.OrgID, op.ID, "approved")
			require.NoError(t, err)
			switch change {
			case "fingerprint":
				o := p.observations[v.Identity.ProviderResourceID]
				o.Diagnostic.EvidenceDigest = vmTestDigest("e")
				p.observations[v.Identity.ProviderResourceID] = o
			case "expiry":
				s.cfg.Now = func() time.Time { return vmTestNow.Add(domain.VMApprovalMaxAge + time.Second) }
			case "generation":
				stored := r.deployments[v.ID]
				stored.Generation++
				r.deployments[v.ID] = stored
			}
			require.Error(t, w.Process(context.Background(), v.OrgID, op.ID))
			require.Len(t, p.executed, 1)
		})
	}
}
func TestVMApprovalPlanThenAtomicAdoption(t *testing.T) {
	s, r, p, v, principal := vmFixture(t)
	ctx := context.Background()
	state := domain.VMRuntimeStopped
	p.observations[v.Identity.ProviderResourceID] = domain.VMObservation{Identity: v.Identity, LifecycleClass: domain.VMLifecyclePersistent, Availability: domain.VMObservationAvailable, RuntimeState: &state, Ownership: domain.VMForeign, Drift: domain.VMDriftUnknown, GuestHealth: domain.VMGuestUnknown, Diagnostic: domain.VMDiagnostic{EvidenceDigest: vmTestDigest("d")}}
	require.NoError(t, s.RegisterAdoption(ctx, principal, v))
	require.Empty(t, r.reservations)
	require.Empty(t, p.executed)
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "adopt", Reason: "adopt exact domain", Kind: domain.VMOperationAdopt}
	_, err := s.RequestOperation(ctx, principal, req)
	require.Error(t, err, "new reservation must be admitted atomically with approval")
	plan, err := s.PlanOperation(ctx, principal, req)
	require.NoError(t, err)
	require.Equal(t, domain.VMApprovalDestructive, plan.RequiredTier)
	approver := *principal
	approver.PubKey = "second-person"
	approver.Subject = "second-person"
	approval, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve foreign adoption")
	require.NoError(t, err)
	req.ApprovalID = &approval.ID
	op, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	require.Len(t, r.reservations, 1)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(ctx, v.OrgID, op.ID))
	require.Len(t, p.executed, 1)
}
func TestVMApprovalReconstructsExactRequestInsteadOfTrustingCallerHash(t *testing.T) {
	s, r, _, v, principal := vmFixture(t)
	ctx := context.Background()
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(ctx, v.OrgID, created.ID))
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment, IdempotencyKey: "delete", Reason: "graceful decommission"}
	approver := *principal
	approver.PubKey = "approver"
	approver.Subject = "approver"
	approved, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "approve without force")
	require.NoError(t, err)
	expected, err := vmRequestHash(principal, req)
	require.NoError(t, err)
	require.Equal(t, expected, approved.RequestHash)
	changed := req
	changed.AllowForceStop = true
	changed.ApprovalID = &approved.ID
	_, err = s.RequestOperation(ctx, principal, changed)
	require.ErrorIs(t, err, repository.ErrConflict)
	stored, err := r.GetApproval(ctx, v.OrgID, approved.ID)
	require.NoError(t, err)
	require.Nil(t, stored.ConsumedAt)
	req.ApprovalID = &approved.ID
	_, err = s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
}

func TestVMApprovalDestructivePlanElevationAndInvalidTargets(t *testing.T) {
	s, r, _, v, principal := vmFixture(t)
	_, created := vmCreate(t, s, v, principal)
	w, _ := NewVMOperationWorker(s)
	require.NoError(t, w.Process(context.Background(), v.OrgID, created.ID))
	// Disable the optional pilot for this generic-host contraction test; the
	// profile-enforced rejection itself is tested by admission tests.
	h := r.hosts[v.HostID]
	h.PilotPolicy = nil
	r.hosts[v.HostID] = h
	desired := r.deployments[v.ID]
	desired.Observation = nil
	desired.ObservationCursor = nil
	desired.Generation++
	desired.Allocation.VCPU--
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "contract", Reason: "CPU contraction", Kind: domain.VMOperationDefine, Desired: &desired}
	plan, err := s.PlanOperation(context.Background(), principal, req)
	require.NoError(t, err)
	require.Equal(t, domain.VMApprovalDestructive, plan.RequiredTier)
	_, err = s.RequestOperation(context.Background(), principal, req)
	require.Error(t, err)
	req.Desired = nil
	req.Kind = domain.VMOperationDelete
	req.DeleteTarget = ""
	_, err = s.RequestOperation(context.Background(), principal, req)
	require.Error(t, err)
	req.Kind = domain.VMOperationGracefulStop
	req.AllowForceStop = true
	_, err = s.RequestOperation(context.Background(), principal, req)
	require.Error(t, err)
	req.AllowForceStop = false
	req.Kind = domain.VMOperationRestore
	foreign := uuid.New()
	req.CheckpointID = &foreign
	_, err = s.RequestOperation(context.Background(), principal, req)
	require.Error(t, err)
}
