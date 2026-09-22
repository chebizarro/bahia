package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type vmAdmission struct{ runtime *virtualizationRuntime }

func vmIntentPrincipal(p controlplane.VirtualizationPrincipal) *auth.Principal {
	return &auth.Principal{Subject: p.PubKey, PubKey: p.PubKey, Method: auth.MethodNIP98}
}
func operationAdmission(op *domain.VMOperation, err error) (controlplane.VirtualizationAdmission, error) {
	if err != nil {
		return controlplane.VirtualizationAdmission{}, err
	}
	if op == nil {
		return controlplane.VirtualizationAdmission{}, readmodel.ErrVirtualizationUnavailable
	}
	return controlplane.VirtualizationAdmission{ResourceID: op.ResourceID, OperationID: op.ID, Generation: op.ResourceGeneration}, nil
}
func (a vmAdmission) MutatePersistentVM(ctx context.Context, actor controlplane.VirtualizationPrincipal, method string, m controlplane.VirtualizationMutation) (controlplane.VirtualizationAdmission, error) {
	r := a.runtime
	if !r.ready.Load() {
		return controlplane.VirtualizationAdmission{}, readmodel.ErrVirtualizationUnavailable
	}
	p := vmIntentPrincipal(actor)
	req := service.VMOperationRequest{OrgID: actor.OrgID, DeploymentID: m.ID, ExpectedGeneration: m.ExpectedGeneration, IdempotencyKey: m.IdempotencyKey, Reason: m.Reason, Kind: m.Operation, CheckpointID: m.CheckpointID, ExportID: m.ExportID, CloneTargetID: m.CloneTargetID, DeleteTarget: m.DeleteTarget, DataDisposition: m.DataDisposition, AllowForceStop: m.AllowForceStop, ApprovalID: m.ApprovalID, Desired: m.VM}
	switch method {
	case "persistent-vm/register-adoption":
		if m.VM == nil || (m.ID != uuid.Nil && m.ID != m.VM.ID) || m.ExpectedGeneration != 0 || m.Operation != "" || m.ApprovalID != nil || m.CheckpointID != nil || m.ExportID != nil || m.CloneTargetID != nil || m.DeleteTarget != "" || m.DataDisposition != "" || m.AllowForceStop || m.Image != nil || m.Plane != nil {
			return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
		}
		if err := r.vmService.RegisterAdoption(ctx, p, *m.VM); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		return controlplane.VirtualizationAdmission{ResourceID: m.VM.ID, Generation: 1}, nil
	case "persistent-vm/create":
		if m.VM == nil {
			return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
		}
		_, op, err := r.vmService.CreateDeployment(ctx, p, service.VMCreateRequest{Deployment: *m.VM, IdempotencyKey: m.IdempotencyKey, Reason: m.Reason})
		return operationAdmission(op, err)
	case "persistent-vm/update":
		op, err := r.vmService.UpdateDeployment(ctx, p, req)
		return operationAdmission(op, err)
	case "persistent-vm/operate":
		op, err := r.vmService.RequestOperation(ctx, p, req)
		return operationAdmission(op, err)
	case "vm-operation/approve-plan":
		approval, err := r.vmService.ApprovePlan(ctx, p, m.Requester, req, m.ApprovalReason)
		if err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		return controlplane.VirtualizationAdmission{ResourceID: approval.ResourceID, Generation: approval.Generation, ApprovalID: &approval.ID}, nil
	case "vm-operation/approve":
		if _, err := r.vmService.ApproveOperation(ctx, p, actor.OrgID, m.ID, m.Reason); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		op, err := r.vmService.GetOperation(ctx, p, actor.OrgID, m.ID)
		return operationAdmission(op, err)
	case "vm-operation/cancel":
		op, err := r.vmService.CancelOperation(ctx, p, actor.OrgID, m.ID)
		return operationAdmission(op, err)
	case "vm-image/register":
		if m.Image == nil || m.Image.OrgID != actor.OrgID || r.policy == nil || r.host == nil {
			return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
		}
		if err := r.policy.AuthorizeExecutionPlane(ctx, p, actor.OrgID); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		host, err := r.repo.Hosts().Get(ctx, actor.OrgID, r.host.ID)
		if err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		if err := r.policy.VerifyPlaneProvenance(ctx, host, m.Image.ManifestDigest, m.Image.Provenance); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		image := *m.Image
		image.CreatedBy = p.PubKey
		if err := r.repo.Images().Create(ctx, &image); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		r.changed(ctx, repository.VirtualizationResourceRef{OrgID: actor.OrgID, Kind: domain.VMImageResource, ID: image.ID})
		return controlplane.VirtualizationAdmission{ResourceID: image.ID, OperationID: image.ID, Generation: image.Generation}, nil
	default:
		return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
	}
}

type planeAdmission struct{ runtime *virtualizationRuntime }

func (a planeAdmission) MutateExecutionPlane(ctx context.Context, actor controlplane.VirtualizationPrincipal, action string, m controlplane.VirtualizationMutation) (controlplane.VirtualizationAdmission, error) {
	r := a.runtime
	if !r.ready.Load() {
		return controlplane.VirtualizationAdmission{}, readmodel.ErrVirtualizationUnavailable
	}
	p := vmIntentPrincipal(actor)
	var plane *domain.ExecutionPlaneDeployment
	var err error
	switch action {
	case "create", "update":
		if m.Plane == nil || m.Plane.OrgID != actor.OrgID {
			return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
		}
		if err = r.endpointAllowed(*m.Plane); err != nil {
			return controlplane.VirtualizationAdmission{}, err
		}
		if action == "create" {
			plane, err = r.planeService.Create(ctx, p, *m.Plane)
		} else {
			plane, err = r.planeService.Update(ctx, p, *m.Plane, m.ExpectedGeneration)
		}
	case "reconcile":
		plane, err = r.planeService.Get(ctx, p, actor.OrgID, m.ID)
		if err == nil && plane.Generation != m.ExpectedGeneration {
			err = repository.ErrConflict
		}
	default:
		return controlplane.VirtualizationAdmission{}, domain.ErrInvalidValue
	}
	// D can commit desired state and fail capacity admission. Still project the
	// committed revision; do not start an apply or acknowledge successful admission.
	if plane != nil {
		r.changed(ctx, repository.VirtualizationResourceRef{OrgID: plane.OrgID, Kind: domain.ExecutionPlaneResource, ID: plane.ID})
	}
	if err != nil {
		return controlplane.VirtualizationAdmission{}, err
	}
	if err = r.endpointAllowed(*plane); err != nil {
		return controlplane.VirtualizationAdmission{}, err
	}
	r.enqueue(vmWork{org: plane.OrgID, id: plane.ID, kind: domain.ExecutionPlaneResource})
	operation := uuid.NewSHA1(plane.ID, []byte(fmt.Sprintf("execution-plane-apply:%d", plane.Generation)))
	return controlplane.VirtualizationAdmission{ResourceID: plane.ID, OperationID: operation, Generation: plane.Generation}, nil
}
func (r *virtualizationRuntime) endpointAllowed(p domain.ExecutionPlaneDeployment) error {
	for _, e := range r.endpoints {
		if e.HostID == p.HostID && e.EndpointRef == p.ManagementEndpointRef && e.Author == p.ManagementAuthor {
			return nil
		}
	}
	return readmodel.ErrVirtualizationUnavailable
}
