package service

import (
	"context"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// ApprovePlan reconstructs the exact request server-side. Never accept a hash
// paired with independently supplied action fields: that could misrepresent the
// action being approved. The named requester must still authenticate at admission.
func (s *PersistentVMService) ApprovePlan(ctx context.Context, p *auth.Principal, requester string, request VMOperationRequest, reason string) (*domain.VMApproval, error) {
	if err := s.authorize(ctx, p, request.OrgID, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	if request.ApprovalID != nil {
		return nil, domain.ErrInvalidValue
	}
	actor := vmExecutionPrincipal(requester)
	if err := s.authorize(ctx, actor, request.OrgID, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	op, desired, _, err := s.prepare(ctx, actor, request)
	if err != nil {
		return nil, err
	}
	return s.approveValidatedPlan(ctx, p, *op, reason, desired)
}

func (s *PersistentVMService) approveValidatedPlan(ctx context.Context, p *auth.Principal, op domain.VMOperation, reason string, desired *domain.PersistentVMDeployment) (*domain.VMApproval, error) {
	if err := s.authorize(ctx, p, op.OrgID, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	if err := s.cfg.Permissions.CheckPermission(ctx, p, op.OrgID, domain.PermApproveDeployments); err != nil {
		return nil, err
	}
	if err := domain.ValidateVMOperation(&op); err != nil {
		return nil, err
	}
	if op.RequiredTier != domain.VMApprovalDestructive || op.Actor == vmActor(p) || op.ApprovalID != nil || (op.Phase != domain.VMOperationAccepted && op.Phase != domain.VMOperationAwaitingApproval) {
		return nil, vmError(domain.VMErrorApprovalRequired)
	}
	v, err := s.cfg.Repository.Deployments().Get(ctx, op.OrgID, op.ResourceID)
	if err != nil {
		return nil, err
	}
	if v.Generation != op.ExpectedGeneration {
		return nil, repository.ErrConflict
	}
	h, err := s.cfg.Repository.Hosts().Get(ctx, op.OrgID, v.HostID)
	if err != nil {
		return nil, err
	}
	obs, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return nil, err
	}
	if obs.Diagnostic.EvidenceDigest != op.ProviderFingerprint {
		return nil, repository.ErrConflict
	}
	if op.Kind == domain.VMOperationAdopt {
		if op.Adoption == nil {
			return nil, vmError(domain.VMErrorIntegrity)
		}
		target := *v
		if desired != nil {
			target = *desired
		}
		image, err := s.cfg.Repository.Images().Get(ctx, op.OrgID, target.ImageID)
		if err != nil {
			return nil, err
		}
		target.Generation = op.ResourceGeneration
		target.ConfigDigest = op.Adoption.ConfigDigest
		measured, err := s.measureAdoption(ctx, *h, target, *image)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(measured, op.Adoption) {
			return nil, repository.ErrConflict
		}
	}
	now := s.cfg.Now().UTC()
	a := &domain.VMApproval{AdoptionDigest: adoptionDigest(op), SchemaVersion: 1, ID: uuid.New(), OrgID: op.OrgID, ResourceID: op.ResourceID, LifecycleClass: op.LifecycleClass, Generation: op.ExpectedGeneration, RequestHash: op.RequestHash, ProviderFingerprint: op.ProviderFingerprint, Tier: domain.VMApprovalDestructive, Requester: op.Actor, Approver: vmActor(p), Reason: reason, CreatedAt: now, ExpiresAt: now.Add(domain.VMApprovalMaxAge)}
	if err = s.cfg.Repository.CreateApproval(ctx, a); err != nil {
		return nil, err
	}
	s.changed(ctx, op.OrgID, domain.PersistentVMResource, op.ResourceID)
	return a, nil
}

func (s *PersistentVMService) ApproveOperation(ctx context.Context, p *auth.Principal, org, id uuid.UUID, reason string) (*domain.VMApproval, error) {
	if err := s.authorize(ctx, p, org, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	op, err := s.cfg.Repository.GetOperation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if op.Phase != domain.VMOperationAwaitingApproval {
		return nil, repository.ErrConflict
	}
	a, err := s.approveValidatedPlan(ctx, p, *op, reason, nil)
	if err != nil {
		return nil, err
	}
	accepted, err := s.cfg.Repository.TransitionOperation(ctx, repository.VMOperationTransition{OrgID: org, OperationID: id, ExpectedPhase: op.Phase, ExpectedRevision: op.Generation, Phase: domain.VMOperationAccepted, ApprovalID: &a.ID})
	if err != nil {
		return nil, err
	}
	s.notify(ctx, *accepted)
	return a, nil
}

func vmApprovalValid(a *domain.VMApproval, op domain.VMOperation, fingerprint string, now time.Time) bool {
	return a != nil && domain.ValidateVMApproval(a) == nil && a.ConsumedAt != nil && a.OrgID == op.OrgID && a.ResourceID == op.ResourceID && a.Generation == op.ExpectedGeneration && a.AdoptionDigest == adoptionDigest(op) && a.RequestHash == op.RequestHash && a.Requester == op.Actor && a.Tier == op.RequiredTier && a.ProviderFingerprint == op.ProviderFingerprint && fingerprint == op.ProviderFingerprint && !a.CreatedAt.After(now) && a.ExpiresAt.After(now)
}

// Pending approvals can only hold already-reserved resources. Plans requiring
// new capacity use ApprovePlan then atomic AdmitOperation, not piecemeal reserve.
func (s *PersistentVMService) reservationsCovered(ctx context.Context, desired []domain.VMCapacityReservation) (bool, error) {
	for _, want := range desired {
		rows, err := s.cfg.Repository.ListReservations(ctx, want.OrgID, want.HostID)
		if err != nil {
			return false, err
		}
		found := false
		for _, row := range rows {
			if row.ResourceKind == want.ResourceKind && row.ResourceID == want.ResourceID && row.HostID == want.HostID && reflect.DeepEqual(row.Capacity, want.Capacity) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}
