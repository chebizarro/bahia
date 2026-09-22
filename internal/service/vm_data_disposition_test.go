package service

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestVMDeleteDataDispositionApprovalAndProviderPropagation(t *testing.T) {
	for _, disposition := range []domain.VMDataDisposition{"", domain.VMDataRetain, domain.VMDataExport, domain.VMDataDelete} {
		t.Run(string(disposition), func(t *testing.T) {
			s, _, provider, v, principal := vmFixture(t)
			ctx := context.Background()
			_, created := vmCreate(t, s, v, principal)
			worker, err := NewVMOperationWorker(s)
			require.NoError(t, err)
			require.NoError(t, worker.Process(ctx, v.OrgID, created.ID))
			request := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment, DataDisposition: disposition, IdempotencyKey: "delete-data", Reason: "decommission"}
			op, err := s.RequestOperation(ctx, principal, request)
			require.NoError(t, err)
			require.Equal(t, disposition.Effective(), op.DataDisposition)
			require.Equal(t, domain.VMApprovalDestructive, op.RequiredTier)
			require.Equal(t, domain.VMOperationAwaitingApproval, op.Phase)
			require.NoError(t, worker.Process(ctx, v.OrgID, op.ID))
			require.Len(t, provider.executed, 1)
			_, err = s.ApproveOperation(ctx, principal, v.OrgID, op.ID, "self")
			require.Error(t, err)
			approver := *principal
			approver.PubKey, approver.Subject = "second-person", "second-person"
			_, err = s.ApproveOperation(ctx, &approver, v.OrgID, op.ID, "reviewed data disposition")
			require.NoError(t, err)
			require.NoError(t, worker.Process(ctx, v.OrgID, op.ID))
			require.Len(t, provider.executed, 2)
			require.Equal(t, disposition.Effective(), provider.executed[1].Operation.DataDisposition)
		})
	}
}

func TestVMDeleteDispositionBoundToApprovalAndIdempotency(t *testing.T) {
	s, _, _, v, principal := vmFixture(t)
	ctx := context.Background()
	_, created := vmCreate(t, s, v, principal)
	worker, _ := NewVMOperationWorker(s)
	require.NoError(t, worker.Process(ctx, v.OrgID, created.ID))
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment, IdempotencyKey: "delete", Reason: "retain guest data"}
	approver := *principal
	approver.PubKey, approver.Subject = "second-person", "second-person"
	approval, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "retain only")
	require.NoError(t, err)
	req.ApprovalID = &approval.ID
	req.DataDisposition = domain.VMDataDelete
	_, err = s.RequestOperation(ctx, principal, req)
	require.ErrorIs(t, err, repository.ErrConflict)
	req.DataDisposition = domain.VMDataRetain
	op, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	req.DataDisposition = ""
	replay, err := s.RequestOperation(ctx, principal, req)
	require.NoError(t, err)
	require.Equal(t, op.ID, replay.ID)
	req.DataDisposition = domain.VMDataDelete
	_, err = s.RequestOperation(ctx, principal, req)
	require.ErrorIs(t, err, repository.ErrConflict)
}

func TestVMDataDispositionRejectsInvalidAndUnrelatedActions(t *testing.T) {
	for _, req := range []VMOperationRequest{
		{Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteDeployment, DataDisposition: "erase"},
		{Kind: domain.VMOperationStart, DataDisposition: domain.VMDataDelete},
		{Kind: domain.VMOperationDelete, DeleteTarget: domain.VMDeleteCheckpoint, DataDisposition: domain.VMDataRetain},
	} {
		require.ErrorIs(t, vmOperationTargets(req), domain.ErrInvalidValue)
	}
}
