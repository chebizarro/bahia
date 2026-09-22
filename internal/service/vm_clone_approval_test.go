package service

import (
	"context"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestVMCloneNetworkApprovalBindsExactTargetRevision(t *testing.T) {
	for _, scenario := range []string{"bridged", "passthrough", "changed before admission", "changed before execution"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, provider, v, principal := vmFixture(t)
			ctx := context.Background()
			h := r.hosts[v.HostID]
			h.PilotPolicy = nil
			h.Capacity.DiskBytes, h.Quota.DiskBytes = 1<<40, 1<<40
			r.hosts[h.ID] = h
			_, created := vmCreate(t, s, v, principal)
			worker, _ := NewVMOperationWorker(s)
			require.NoError(t, worker.Process(ctx, v.OrgID, created.ID))
			image := r.images[v.ImageID]
			c := domain.VMCheckpoint{VirtualizationResourceMeta: vmTestMeta(v.OrgID), LifecycleClass: domain.VMLifecyclePersistent, DeploymentID: v.ID, DeploymentGeneration: 1, ImageID: v.ImageID, ImageDigest: image.ManifestDigest, ConfigDigest: v.ConfigDigest, Identity: v.Identity, Consistency: domain.VMCheckpointCold, State: domain.VMArtifactCreating, Firmware: v.Firmware, RetainUntil: vmTestNow.AddDate(0, 1, 0)}
			require.NoError(t, s.RegisterCheckpoint(ctx, principal, c))
			op, err := s.Checkpoint(ctx, principal, VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, IdempotencyKey: "checkpoint", Reason: "backup", CheckpointID: &c.ID})
			require.NoError(t, err)
			require.NoError(t, worker.Process(ctx, v.OrgID, op.ID))
			target := v
			target.ID = uuid.New()
			target.Identity.DeploymentID, target.Identity.ProviderResourceID = target.ID, uuid.New()
			network := uuid.New()
			target.Network = domain.VMNetwork{Mode: domain.VMNetworkBridged, NetworkRef: &network}
			if scenario == "passthrough" {
				target.Network = domain.VMNetwork{Mode: domain.VMNetworkIsolated, PassthroughDeviceRefs: []uuid.UUID{uuid.New()}}
			}
			require.NoError(t, s.RegisterCloneTarget(ctx, principal, target))
			req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 1, Kind: domain.VMOperationClone, IdempotencyKey: "clone", Reason: "clone", CheckpointID: &c.ID, CloneTargetID: &target.ID}
			plan, err := s.PlanOperation(ctx, principal, req)
			require.NoError(t, err)
			require.Equal(t, domain.VMApprovalDestructive, plan.RequiredTier)
			require.Equal(t, target.Generation, plan.CloneTargetGeneration)
			_, err = s.Clone(ctx, principal, req)
			require.Error(t, err)
			require.Len(t, provider.executed, 2)
			_, err = s.ApprovePlan(ctx, principal, principal.PubKey, req, "self")
			require.Error(t, err)
			approver := *principal
			approver.PubKey, approver.Subject = "second-person", "second-person"
			approval, err := s.ApprovePlan(ctx, &approver, principal.PubKey, req, "reviewed target network")
			require.NoError(t, err)
			req.ApprovalID = &approval.ID
			change := func() { target.Generation++; target.DisplayName = "revised target"; r.deployments[target.ID] = target }
			if scenario == "changed before admission" {
				change()
			}
			clone, err := s.Clone(ctx, principal, req)
			if scenario == "changed before admission" {
				require.ErrorIs(t, err, repository.ErrConflict)
				return
			}
			require.NoError(t, err)
			if scenario == "changed before execution" {
				change()
			}
			err = worker.Process(ctx, v.OrgID, clone.ID)
			if scenario == "changed before execution" {
				require.Error(t, err)
				require.Len(t, provider.executed, 2)
				return
			}
			require.NoError(t, err)
			require.Len(t, provider.executed, 3)
		})
	}
}
