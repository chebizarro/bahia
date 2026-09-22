package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// VMPermissionChecker is implemented by auth.RBAC. Operator eligibility is an
// independent injected policy; a principal role string alone never grants it.
type VMPermissionChecker interface {
	CheckPermission(context.Context, *auth.Principal, uuid.UUID, domain.Permission) error
}
type PersistentVMServiceConfig struct {
	Repository   repository.VirtualizationRepository
	Provider     domain.PersistentVMProvider
	Permissions  VMPermissionChecker
	Operator     func(context.Context, *auth.Principal) error
	Services     repository.ServiceRepository
	Environments repository.EnvironmentRepository
	Units        repository.DeploymentUnitRepository
	Bootstrap    *VMBootstrapService
	Now          func() time.Time
	// OnOperation is a wake-up hint after a durable write, not an event outbox.
	// E must project the repository change journal and recover it at startup.
	OnOperation func(context.Context, domain.VMOperation)
	OnChange    func(context.Context, repository.VirtualizationResourceRef)
}
type PersistentVMService struct{ cfg PersistentVMServiceConfig }

func NewPersistentVMService(cfg PersistentVMServiceConfig) (*PersistentVMService, error) {
	if cfg.Repository == nil || cfg.Provider == nil || cfg.Permissions == nil || cfg.Operator == nil {
		return nil, domain.ErrInvalidValue
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PersistentVMService{cfg: cfg}, nil
}

type VMOperationRequest struct {
	OrgID              uuid.UUID                `json:"org_id"`
	DeploymentID       uuid.UUID                `json:"deployment_id"`
	ExpectedGeneration int64                    `json:"expected_generation"`
	IdempotencyKey     string                   `json:"idempotency_key"`
	Reason             string                   `json:"reason"`
	Kind               domain.VMOperationKind   `json:"kind"`
	CheckpointID       *uuid.UUID               `json:"checkpoint_id,omitempty"`
	ExportID           *uuid.UUID               `json:"export_id,omitempty"`
	CloneTargetID      *uuid.UUID               `json:"clone_target_id,omitempty"`
	DeleteTarget       domain.VMDeleteTarget    `json:"delete_target,omitempty"`
	DataDisposition    domain.VMDataDisposition `json:"data_disposition,omitempty"`
	AllowForceStop     bool                     `json:"allow_force_stop"`
	ApprovalID         *uuid.UUID               `json:"approval_id,omitempty"`
	// Desired is a full, next-generation revision, never a patch to provider XML.
	Desired *domain.PersistentVMDeployment `json:"desired,omitempty"`
}
type VMCreateRequest struct {
	Deployment     domain.PersistentVMDeployment `json:"deployment"`
	IdempotencyKey string                        `json:"idempotency_key"`
	Reason         string                        `json:"reason"`
}

func vmActor(p *auth.Principal) string {
	if p == nil {
		return ""
	}
	if p.PubKey != "" {
		return strings.ToLower(p.PubKey)
	}
	return p.Subject
}
func (s *PersistentVMService) authorize(ctx context.Context, p *auth.Principal, org uuid.UUID, permission domain.Permission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.cfg.Permissions == nil || s.cfg.Operator == nil || p == nil || !p.IsAuthenticated() || vmActor(p) == "" || org == uuid.Nil {
		return &auth.AccessDeniedError{Reason: "authenticated VM operator required", OrgID: org}
	}
	if permission != domain.PermReadDeployments {
		if err := s.cfg.Operator(ctx, p); err != nil {
			return &auth.AccessDeniedError{Reason: "VM operator required", OrgID: org}
		}
	}
	return s.cfg.Permissions.CheckPermission(ctx, p, org, permission)
}
func (s *PersistentVMService) references(ctx context.Context, p *auth.Principal, v *domain.PersistentVMDeployment) (*domain.VirtualizationHost, *domain.VMImage, error) {
	h, err := s.cfg.Repository.Hosts().Get(ctx, v.OrgID, v.HostID)
	if err != nil {
		return nil, nil, err
	}
	i, err := s.cfg.Repository.Images().Get(ctx, v.OrgID, v.ImageID)
	if err != nil {
		return nil, nil, err
	}
	clean := *v
	clean.Observation = nil
	clean.ObservationCursor = nil
	host := *h
	host.Observation = nil
	host.ObservationCursor = nil
	// Governed profile ranges and aggregate ceilings are exclusively domain policy.
	if err = domain.ValidateVMDeploymentReferences(&clean, &host, i); err != nil {
		return nil, nil, err
	}
	if v.ServiceID != nil {
		if s.cfg.Services == nil || s.cfg.Environments == nil {
			return nil, nil, domain.ErrInvalidValue
		}
		svc, e := s.cfg.Services.GetByID(ctx, *v.ServiceID)
		if e != nil {
			return nil, nil, e
		}
		env, e := s.cfg.Environments.GetByID(ctx, *v.EnvironmentID)
		if e != nil {
			return nil, nil, e
		}
		if svc == nil || env == nil || svc.OrgID != v.OrgID || env.OrgID != v.OrgID {
			return nil, nil, domain.ErrInvalidValue
		}
	}
	if v.DeploymentUnitID != nil {
		if s.cfg.Units == nil {
			return nil, nil, domain.ErrInvalidValue
		}
		unit, e := s.cfg.Units.GetByID(ctx, *v.DeploymentUnitID)
		if e != nil {
			return nil, nil, e
		}
		if unit == nil || unit.EnvironmentID != *v.EnvironmentID {
			return nil, nil, domain.ErrInvalidValue
		}
	}
	if len(v.Bootstrap) > 0 {
		if s.cfg.Bootstrap == nil {
			return nil, nil, domain.ErrInvalidValue
		}
		if err = s.cfg.Bootstrap.Authorize(ctx, p, *v); err != nil {
			return nil, nil, err
		}
	}
	return h, i, nil
}
func vmHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func vmRequestHash(p *auth.Principal, req VMOperationRequest) (string, error) {
	req.ApprovalID = nil
	if req.Kind == domain.VMOperationDelete && req.DeleteTarget == domain.VMDeleteDeployment && req.DataDisposition.Effective() == domain.VMDataRetain {
		// Preserve pre-disposition request hashes and treat explicit retain as default.
		req.DataDisposition = ""
	}
	return vmHash(struct {
		Actor   string
		Request VMOperationRequest
	}{vmActor(p), req})
}
func (s *PersistentVMService) boundRequestHash(ctx context.Context, p *auth.Principal, req VMOperationRequest) (string, *domain.PersistentVMDeployment, error) {
	hash, err := vmRequestHash(p, req)
	if err != nil || req.CloneTargetID == nil {
		return hash, nil, err
	}
	target, err := s.cfg.Repository.Deployments().Get(ctx, req.OrgID, *req.CloneTargetID)
	if err != nil {
		return "", nil, err
	}
	hash, err = vmHash(struct {
		RequestHash      string
		TargetGeneration int64
	}{hash, target.Generation})
	return hash, target, err
}

func (s *PersistentVMService) replay(ctx context.Context, p *auth.Principal, req VMOperationRequest) (*domain.VMOperation, error) {
	hash, _, err := s.boundRequestHash(ctx, p, req)
	if err != nil {
		return nil, err
	}
	for offset := 0; ; offset += 100 {
		ops, e := s.cfg.Repository.ListOperations(ctx, req.OrgID, uuid.Nil, 100, offset)
		if e != nil {
			return nil, e
		}
		for _, op := range ops {
			if op.IdempotencyKey == req.IdempotencyKey {
				if op.RequestHash != hash || op.Actor != vmActor(p) || op.ResourceID != req.DeploymentID {
					return nil, repository.ErrConflict
				}
				return &op, nil
			}
		}
		if len(ops) < 100 {
			return nil, nil
		}
	}
}
func vmReservation(v domain.PersistentVMDeployment, kind domain.VirtualizationResourceKind, id uuid.UUID, capacity domain.VMCapacity) domain.VMCapacityReservation {
	return domain.VMCapacityReservation{ID: uuid.NewSHA1(id, []byte("bahia-vm-capacity")), OrgID: v.OrgID, HostID: v.HostID, ResourceID: id, ResourceKind: kind, LifecycleClass: domain.VMLifecyclePersistent, Capacity: capacity}
}
func (s *PersistentVMService) changed(ctx context.Context, org uuid.UUID, kind domain.VirtualizationResourceKind, id uuid.UUID) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx, repository.VirtualizationResourceRef{OrgID: org, Kind: kind, ID: id})
	}
}

func (s *PersistentVMService) notify(ctx context.Context, op domain.VMOperation) {
	s.changed(ctx, op.OrgID, domain.VMOperationResource, op.ID)
	if s.cfg.OnOperation != nil {
		s.cfg.OnOperation(ctx, op)
	}
}

func (s *PersistentVMService) CreateDeployment(ctx context.Context, p *auth.Principal, request VMCreateRequest) (*domain.PersistentVMDeployment, *domain.VMOperation, error) {
	v := request.Deployment
	if err := s.authorize(ctx, p, v.OrgID, domain.PermWriteDeployments); err != nil {
		return nil, nil, err
	}
	if v.Generation != 1 || v.Observation != nil || v.ObservationCursor != nil || v.BootstrapApplied || len(v.Connections) > 0 {
		return nil, nil, domain.ErrInvalidValue
	}
	v.CreatedBy = vmActor(p)
	v.CreatedAt = time.Time{}
	v.UpdatedAt = time.Time{}
	req := VMOperationRequest{OrgID: v.OrgID, DeploymentID: v.ID, ExpectedGeneration: 0, Kind: domain.VMOperationDefine, IdempotencyKey: request.IdempotencyKey, Reason: request.Reason, Desired: &v}
	if old, err := s.replay(ctx, p, req); err != nil {
		return nil, nil, err
	} else if old != nil {
		stored, e := s.cfg.Repository.Deployments().Get(ctx, v.OrgID, v.ID)
		return stored, old, e
	}
	h, _, err := s.references(ctx, p, &v)
	if err != nil {
		return nil, nil, err
	}
	// Initial networking must be non-destructive. Bridge/passthrough changes use
	// an approved next-generation plan against an existing resource.
	if v.Network.Mode == domain.VMNetworkBridged || len(v.Network.PassthroughDeviceRefs) > 0 {
		return nil, nil, vmError(domain.VMErrorApprovalRequired)
	}
	obs, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return nil, nil, err
	}
	if obs.RuntimeState == nil || *obs.RuntimeState != domain.VMRuntimeAbsent {
		return nil, nil, vmError(domain.VMErrorForeign)
	}
	op, err := s.operation(p, req, *h, v.Generation, nil, obs)
	if err != nil {
		return nil, nil, err
	}
	result, err := s.cfg.Repository.AdmitOperation(ctx, repository.VMOperationAdmission{Operation: *op, ExpectedGeneration: 0, Desired: &v, Reservations: []domain.VMCapacityReservation{vmReservation(v, domain.PersistentVMResource, v.ID, v.Allocation)}})
	if err != nil {
		return nil, nil, err
	}
	s.notify(ctx, result.Operation)
	stored, err := s.cfg.Repository.Deployments().Get(ctx, v.OrgID, v.ID)
	return stored, &result.Operation, err
}

// RegisterAdoption records an exact, unowned target without touching the provider.
// It is not a define intent: reconciliation never auto-defines a registration
// lacking an admitted define. Adopt is the separate two-person operation.
func (s *PersistentVMService) RegisterAdoption(ctx context.Context, p *auth.Principal, v domain.PersistentVMDeployment) error {
	if err := s.authorize(ctx, p, v.OrgID, domain.PermWriteDeployments); err != nil {
		return err
	}
	if v.Generation != 1 || v.Observation != nil || v.ObservationCursor != nil || v.BootstrapApplied || len(v.Connections) > 0 {
		return domain.ErrInvalidValue
	}
	v.CreatedBy = vmActor(p)
	v.CreatedAt = time.Time{}
	v.UpdatedAt = time.Time{}
	h, _, err := s.references(ctx, p, &v)
	if err != nil {
		return err
	}
	o, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return err
	}
	if o.RuntimeState == nil || *o.RuntimeState == domain.VMRuntimeAbsent || o.Ownership == domain.VMOwned {
		return repository.ErrConflict
	}
	err = s.cfg.Repository.Deployments().Create(ctx, &v)
	if err == nil {
		s.changed(ctx, v.OrgID, domain.PersistentVMResource, v.ID)
	}
	return err
}

// PlanOperation performs admission checks but has no durable or provider effects.
// Its exact hash/fingerprint may be approved before admitting a desired revision.
func (s *PersistentVMService) PlanOperation(ctx context.Context, p *auth.Principal, req VMOperationRequest) (*domain.VMOperation, error) {
	if err := s.authorize(ctx, p, req.OrgID, domain.PermReadDeployments); err != nil {
		return nil, err
	}
	op, _, _, err := s.prepare(ctx, p, req)
	return op, err
}
func (s *PersistentVMService) RequestOperation(ctx context.Context, p *auth.Principal, req VMOperationRequest) (*domain.VMOperation, error) {
	if err := s.authorize(ctx, p, req.OrgID, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	if old, err := s.replay(ctx, p, req); err != nil {
		return nil, err
	} else if old != nil {
		return old, nil
	}
	op, desired, reservations, err := s.prepare(ctx, p, req)
	if err != nil {
		return s.admissionFailure(ctx, p, req, err)
	}
	if op.RequiredTier == domain.VMApprovalDestructive && op.ApprovalID == nil {
		if desired != nil {
			return nil, vmError(domain.VMErrorApprovalRequired)
		}
		covered, e := s.reservationsCovered(ctx, reservations)
		if e != nil {
			return nil, e
		}
		if !covered {
			return nil, vmError(domain.VMErrorApprovalRequired)
		}
		op.Phase = domain.VMOperationAwaitingApproval
		reservations = nil
	}
	result, err := s.admitOperation(ctx, repository.VMOperationAdmission{Operation: *op, ExpectedGeneration: req.ExpectedGeneration, Desired: desired, Reservations: reservations})
	if err != nil {
		return s.admissionFailure(ctx, p, req, err)
	}
	s.notify(ctx, result.Operation)
	return &result.Operation, nil
}

// Admission guard keys use a separate lock namespace from provider execution:
// AdmitOperation acquires the real source lock in its own transaction. All C
// admissions touching a clone target share its guard, so checking durable
// references and admitting a target revision cannot race a clone admission.
// Initial creates cannot yet be clone targets and use repository admission alone.
// These UUIDs are lock keys only, never provider identities or resource records.
func (s *PersistentVMService) admitOperation(ctx context.Context, input repository.VMOperationAdmission) (*repository.VMOperationAdmissionResult, error) {
	op := input.Operation
	ids := []uuid.UUID{op.ResourceID}
	if op.CloneTargetID != nil {
		ids = append(ids, *op.CloneTargetID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	var result *repository.VMOperationAdmissionResult
	var guard func(context.Context, int) error
	guard = func(ctx context.Context, index int) error {
		if index == len(ids) {
			if op.CloneTargetID != nil {
				target, err := s.cfg.Repository.Deployments().Get(ctx, op.OrgID, *op.CloneTargetID)
				if err != nil {
					return err
				}
				if target.Generation != op.CloneTargetGeneration {
					return repository.ErrConflict
				}
			}
			if err := s.mutationFence(ctx, op.OrgID, op.ResourceID, op.CloneTargetID, uuid.Nil); err != nil {
				return err
			}
			var err error
			result, err = s.cfg.Repository.AdmitOperation(ctx, input)
			return err
		}
		key := uuid.NewSHA1(ids[index], []byte("bahia-vm-admission-guard"))
		return s.cfg.Repository.WithOperationLock(ctx, op.OrgID, key, func(ctx context.Context) error { return guard(ctx, index+1) })
	}
	err := guard(ctx, 0)
	return result, err
}

// A concurrent identical admission may advance the desired revision between
// the first replay lookup and prepare. The durable hash remains authoritative.
func (s *PersistentVMService) admissionFailure(ctx context.Context, p *auth.Principal, req VMOperationRequest, cause error) (*domain.VMOperation, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if old, err := s.replay(ctx, p, req); err != nil {
		return nil, err
	} else if old != nil {
		return old, nil
	}
	return nil, cause
}

// The source operation's durable slot also fences its clone target. Every
// worker repeats this check while holding both exact-resource locks, including
// admissions which raced across different repository source-resource slots.
func (s *PersistentVMService) mutationFence(ctx context.Context, org, source uuid.UUID, target *uuid.UUID, self uuid.UUID) error {
	involved := func(id uuid.UUID) bool { return id == source || (target != nil && id == *target) }
	for offset := 0; ; offset += 100 {
		rows, err := s.cfg.Repository.ListOperations(ctx, org, uuid.Nil, 100, offset)
		if err != nil {
			return err
		}
		for _, op := range rows {
			if op.ID == self || op.Phase.Terminal() {
				continue
			}
			if involved(op.ResourceID) || (op.CloneTargetID != nil && involved(*op.CloneTargetID)) {
				return repository.ErrConflict
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}

func (s *PersistentVMService) prepare(ctx context.Context, p *auth.Principal, req VMOperationRequest) (*domain.VMOperation, *domain.PersistentVMDeployment, []domain.VMCapacityReservation, error) {
	v, err := s.cfg.Repository.Deployments().Get(ctx, req.OrgID, req.DeploymentID)
	if err != nil {
		return nil, nil, nil, err
	}
	if req.ExpectedGeneration != v.Generation {
		return nil, nil, nil, repository.ErrConflict
	}
	desired := req.Desired
	var plan *domain.VMChangePlan
	target := *v
	if desired != nil {
		if req.Kind != domain.VMOperationDefine || desired.ID != v.ID || desired.OrgID != v.OrgID || desired.Generation != v.Generation+1 || desired.Observation != nil || desired.ObservationCursor != nil || !reflect.DeepEqual(desired.Identity, v.Identity) || desired.BootstrapApplied != v.BootstrapApplied || !reflect.DeepEqual(desired.Connections, v.Connections) {
			return nil, nil, nil, domain.ErrInvalidValue
		}
		target = *desired
		target.CreatedBy = v.CreatedBy
		target.CreatedAt = v.CreatedAt
		desired = &target
	} else {
		power := v.DesiredPower
		if req.Kind == domain.VMOperationStart {
			power = domain.VMDesiredRunning
		}
		if req.Kind == domain.VMOperationGracefulStop {
			power = domain.VMDesiredStopped
		}
		if power != v.DesiredPower {
			target.DesiredPower = power
			target.Generation++
			target.Observation = nil
			target.ObservationCursor = nil
			desired = &target
		}
	}
	h, i, err := s.references(ctx, p, &target)
	if err != nil {
		return nil, nil, nil, err
	}
	obs, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return nil, nil, nil, err
	}
	if req.Kind != domain.VMOperationAdopt && obs.RuntimeState != nil && *obs.RuntimeState != domain.VMRuntimeAbsent && obs.Ownership != domain.VMOwned {
		return nil, nil, nil, vmError(domain.VMErrorForeign)
	}
	if req.Desired != nil {
		bound, cancel := context.WithTimeout(ctx, time.Duration(h.OperationLimits.InspectSeconds)*time.Second)
		plan, err = s.cfg.Provider.PlanChange(bound, domain.VMChangeRequest{Host: *h, Current: *v, Desired: target, Image: *i, Observation: *obs})
		cancel()
		if err != nil {
			return nil, nil, nil, vmError(domain.VMErrorUnavailable)
		}
		if plan == nil {
			return nil, nil, nil, domain.ErrInvalidValue
		}
		if target.Allocation.VCPU < v.Allocation.VCPU || target.Allocation.MemoryBytes < v.Allocation.MemoryBytes || target.Allocation.DiskBytes < v.Allocation.DiskBytes || !reflect.DeepEqual(target.Network, v.Network) {
			plan.RequiredTier = domain.VMApprovalDestructive
		}
	}
	op, err := s.operation(p, req, *h, target.Generation, plan, obs)
	if err != nil {
		return nil, nil, nil, err
	}
	hash, cloneTarget, err := s.boundRequestHash(ctx, p, req)
	if err != nil {
		return nil, nil, nil, err
	}
	op.RequestHash = hash
	if cloneTarget != nil {
		op.CloneTargetGeneration = cloneTarget.Generation
		if cloneTarget.Network.Mode == domain.VMNetworkBridged || len(cloneTarget.Network.PassthroughDeviceRefs) > 0 {
			op.RequiredTier = domain.VMApprovalDestructive
			if req.ApprovalID == nil {
				op.Deadline = op.Deadline.Add(domain.VMApprovalMaxAge)
			}
		}
	}
	if err := domain.ValidateVMOperation(op); err != nil {
		return nil, nil, nil, err
	}
	reservations := []domain.VMCapacityReservation{vmReservation(target, domain.PersistentVMResource, target.ID, target.Allocation)}
	extra, err := s.artifactReferences(ctx, p, *v, *op, obs)
	if err != nil {
		return nil, nil, nil, err
	}
	reservations = append(reservations, extra...)
	reservations, err = s.capacityChanges(ctx, reservations)
	if err != nil {
		return nil, nil, nil, err
	}
	return op, desired, reservations, nil
}
func (s *PersistentVMService) operation(p *auth.Principal, req VMOperationRequest, h domain.VirtualizationHost, generation int64, plan *domain.VMChangePlan, obs *domain.VMObservation) (*domain.VMOperation, error) {
	if err := vmOperationTargets(req); err != nil {
		return nil, err
	}
	hash, err := vmRequestHash(p, req)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now().UTC()
	tier := domain.MinimumVMApprovalTier(req.Kind)
	if plan != nil && plan.RequiredTier > tier {
		tier = plan.RequiredTier
	}
	deadline := now.Add(vmOperationDuration(h, req.Kind))
	if tier == domain.VMApprovalDestructive && req.ApprovalID == nil {
		deadline = deadline.Add(domain.VMApprovalMaxAge)
	}
	op := &domain.VMOperation{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: 1, ID: uuid.New(), OrgID: req.OrgID, Generation: 1, CreatedBy: vmActor(p)}, LifecycleClass: domain.VMLifecyclePersistent, ResourceID: req.DeploymentID, ResourceGeneration: generation, ExpectedGeneration: req.ExpectedGeneration, IdempotencyKey: req.IdempotencyKey, RequestHash: hash, Actor: vmActor(p), Reason: req.Reason, Kind: req.Kind, Phase: domain.VMOperationAccepted, RequiredTier: tier, ApprovalID: req.ApprovalID, ProviderCorrelationID: uuid.New(), Deadline: deadline, CheckpointID: req.CheckpointID, ExportID: req.ExportID, CloneTargetID: req.CloneTargetID, DeleteTarget: req.DeleteTarget, DataDisposition: req.DataDisposition, AllowForceStop: req.AllowForceStop, Plan: plan}
	if req.Kind == domain.VMOperationDelete && req.DeleteTarget == domain.VMDeleteDeployment {
		op.DataDisposition = req.DataDisposition.Effective()
	}
	if obs != nil {
		op.ProviderFingerprint = obs.Diagnostic.EvidenceDigest
	}
	if err = domain.ValidateVMOperation(op); err != nil {
		return nil, err
	}
	return op, nil
}

func vmOperationTargets(req VMOperationRequest) error {
	if !req.DataDisposition.Valid() || (req.DataDisposition != "" && (req.Kind != domain.VMOperationDelete || req.DeleteTarget != domain.VMDeleteDeployment)) {
		return domain.ErrInvalidValue
	}
	checkpoint, export, clone := false, false, false
	switch req.Kind {
	case domain.VMOperationDefine, domain.VMOperationAdopt, domain.VMOperationStart, domain.VMOperationGracefulStop, domain.VMOperationReboot:
	case domain.VMOperationCheckpoint, domain.VMOperationRestore:
		checkpoint = true
	case domain.VMOperationExport:
		checkpoint, export = true, true
	case domain.VMOperationClone:
		checkpoint, clone = true, true
	case domain.VMOperationDelete:
		switch req.DeleteTarget {
		case domain.VMDeleteDeployment:
		case domain.VMDeleteCheckpoint:
			checkpoint = true
		case domain.VMDeleteExport:
			checkpoint, export = true, true
		default:
			return domain.ErrInvalidValue
		}
	default:
		return domain.ErrInvalidValue
	}
	if checkpoint != (req.CheckpointID != nil) || export != (req.ExportID != nil) || clone != (req.CloneTargetID != nil) {
		return domain.ErrInvalidValue
	}
	return nil
}

// Existing reservations are retained on contraction until verified resource
// deletion. Growth reuses the repository's exact reservation ID.
func (s *PersistentVMService) capacityChanges(ctx context.Context, requested []domain.VMCapacityReservation) ([]domain.VMCapacityReservation, error) {
	var changes []domain.VMCapacityReservation
	for _, want := range requested {
		rows, err := s.cfg.Repository.ListReservations(ctx, want.OrgID, want.HostID)
		if err != nil {
			return nil, err
		}
		covered := false
		for _, row := range rows {
			if row.ResourceKind == want.ResourceKind && row.ResourceID == want.ResourceID {
				want.ID = row.ID
				covered = want.Capacity.Fits(row.Capacity)
				if !covered && !row.Capacity.Fits(want.Capacity) {
					return nil, repository.ErrConflict
				}
				break
			}
		}
		if !covered {
			changes = append(changes, want)
		}
	}
	return changes, nil
}

func vmOperationDuration(h domain.VirtualizationHost, kind domain.VMOperationKind) time.Duration {
	seconds := h.OperationLimits.MutationSeconds
	switch kind {
	case domain.VMOperationGracefulStop:
		seconds = h.OperationLimits.GracefulStopSeconds
	case domain.VMOperationCheckpoint, domain.VMOperationExport, domain.VMOperationClone, domain.VMOperationRestore:
		seconds = h.OperationLimits.TransferSeconds
	}
	return time.Duration(seconds) * time.Second
}
func vmError(code domain.VMErrorCode) error {
	return &domain.VMProviderError{Code: code, Unconfirmed: code == domain.VMErrorUnconfirmed}
}
func vmInspect(ctx context.Context, p domain.PersistentVMProvider, h domain.VirtualizationHost, id domain.VMResourceIdentity) (*domain.VMObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(h.OperationLimits.InspectSeconds)*time.Second)
	defer cancel()
	o, err := p.Inspect(ctx, id)
	if err != nil {
		return nil, vmError(domain.VMErrorUnavailable)
	}
	if o == nil || !reflect.DeepEqual(o.Identity, id) || o.LifecycleClass != domain.VMLifecyclePersistent || o.Availability != domain.VMObservationAvailable || o.RuntimeState == nil {
		return nil, vmError(domain.VMErrorIntegrity)
	}
	if o.Ownership == domain.VMOwned || o.Ownership == domain.VMOrphan {
		if o.Marker == nil || domain.ValidateVMOwnershipMarker(*o.Marker) != nil || !reflect.DeepEqual(o.Marker.VMResourceIdentity, id) {
			return nil, vmError(domain.VMErrorForeign)
		}
	}
	return o, nil
}
func (s *PersistentVMService) Get(ctx context.Context, p *auth.Principal, org, id uuid.UUID) (*domain.PersistentVMDeployment, error) {
	if err := s.authorize(ctx, p, org, domain.PermReadDeployments); err != nil {
		return nil, err
	}
	return s.cfg.Repository.Deployments().Get(ctx, org, id)
}
func (s *PersistentVMService) List(ctx context.Context, p *auth.Principal, org uuid.UUID, limit, offset int) ([]domain.PersistentVMDeployment, error) {
	if err := s.authorize(ctx, p, org, domain.PermReadDeployments); err != nil {
		return nil, err
	}
	return s.cfg.Repository.Deployments().List(ctx, org, limit, offset)
}
func (s *PersistentVMService) Observe(ctx context.Context, p *auth.Principal, org, id uuid.UUID) (*domain.VMObservation, error) {
	v, err := s.Get(ctx, p, org, id)
	if err != nil {
		return nil, err
	}
	return v.Observation, nil
}

func (s *PersistentVMService) GetOperation(ctx context.Context, p *auth.Principal, org, id uuid.UUID) (*domain.VMOperation, error) {
	if err := s.authorize(ctx, p, org, domain.PermReadDeployments); err != nil {
		return nil, err
	}
	return s.cfg.Repository.GetOperation(ctx, org, id)
}
func (s *PersistentVMService) lifecycle(ctx context.Context, p *auth.Principal, r VMOperationRequest, k domain.VMOperationKind) (*domain.VMOperation, error) {
	r.Kind = k
	return s.RequestOperation(ctx, p, r)
}
func (s *PersistentVMService) Adopt(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationAdopt)
}
func (s *PersistentVMService) Start(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationStart)
}
func (s *PersistentVMService) GracefulStop(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationGracefulStop)
}
func (s *PersistentVMService) Reboot(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationReboot)
}
func (s *PersistentVMService) Checkpoint(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationCheckpoint)
}
func (s *PersistentVMService) Clone(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationClone)
}
func (s *PersistentVMService) Restore(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationRestore)
}
func (s *PersistentVMService) Delete(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	return s.lifecycle(ctx, p, r, domain.VMOperationDelete)
}
func (s *PersistentVMService) UpdateDeployment(ctx context.Context, p *auth.Principal, r VMOperationRequest) (*domain.VMOperation, error) {
	if r.Desired == nil {
		return nil, domain.ErrInvalidValue
	}
	return s.lifecycle(ctx, p, r, domain.VMOperationDefine)
}

// RegisterCheckpoint prepares a cold checkpoint record only. Its storage is
// reserved atomically by Checkpoint admission before the worker touches it.
func (s *PersistentVMService) RegisterCheckpoint(ctx context.Context, p *auth.Principal, c domain.VMCheckpoint) error {
	if err := s.authorize(ctx, p, c.OrgID, domain.PermWriteDeployments); err != nil {
		return err
	}
	v, err := s.cfg.Repository.Deployments().Get(ctx, c.OrgID, c.DeploymentID)
	if err != nil {
		return err
	}
	_, i, err := s.references(ctx, p, v)
	if err != nil {
		return err
	}
	if c.Generation != 1 || c.State != domain.VMArtifactCreating || len(c.Components) != 0 || c.ManifestDigest != "" || c.DeploymentGeneration != v.Generation || !reflect.DeepEqual(c.Identity, v.Identity) || c.ImageID != v.ImageID || c.ImageDigest != i.ManifestDigest || c.ConfigDigest != v.ConfigDigest || c.Firmware != v.Firmware || c.TPMEnabled != v.TPM.Enabled {
		return domain.ErrInvalidValue
	}
	c.CreatedBy = vmActor(p)
	err = s.cfg.Repository.Checkpoints().Create(ctx, &c)
	if err == nil {
		s.changed(ctx, c.OrgID, domain.VMCheckpointResource, c.ID)
	}
	return err
}
func (s *PersistentVMService) RegisterCloneTarget(ctx context.Context, p *auth.Principal, v domain.PersistentVMDeployment) error {
	if err := s.authorize(ctx, p, v.OrgID, domain.PermWriteDeployments); err != nil {
		return err
	}
	if v.Generation != 1 || v.DesiredPower != domain.VMDesiredStopped || v.Observation != nil || v.ObservationCursor != nil || len(v.Connections) != 0 || v.BootstrapApplied || len(v.Bootstrap) != 0 || v.TPM.Enabled {
		return domain.ErrInvalidValue
	}
	v.CreatedBy = vmActor(p)
	h, _, err := s.references(ctx, p, &v)
	if err != nil {
		return err
	}
	o, err := vmInspect(ctx, s.cfg.Provider, *h, v.Identity)
	if err != nil {
		return err
	}
	if *o.RuntimeState != domain.VMRuntimeAbsent {
		return repository.ErrConflict
	}
	err = s.cfg.Repository.Deployments().Create(ctx, &v)
	if err == nil {
		s.changed(ctx, v.OrgID, domain.PersistentVMResource, v.ID)
	}
	return err
}
func (s *PersistentVMService) artifactReferences(ctx context.Context, p *auth.Principal, v domain.PersistentVMDeployment, op domain.VMOperation, obs *domain.VMObservation) ([]domain.VMCapacityReservation, error) {
	var out []domain.VMCapacityReservation
	var checkpoint *domain.VMCheckpoint
	if op.CheckpointID != nil {
		c, err := s.cfg.Repository.Checkpoints().Get(ctx, v.OrgID, *op.CheckpointID)
		if err != nil {
			return nil, err
		}
		checkpoint = c
		if c.DeploymentID != v.ID || !reflect.DeepEqual(c.Identity, v.Identity) {
			return nil, domain.ErrInvalidValue
		}
		if op.Kind == domain.VMOperationCheckpoint {
			if c.State != domain.VMArtifactCreating || c.DeploymentGeneration != v.Generation {
				return nil, repository.ErrConflict
			}
			out = append(out, vmReservation(v, domain.VMCheckpointResource, c.ID, domain.VMCapacity{DiskBytes: v.Allocation.DiskBytes}))
		} else if op.Kind != domain.VMOperationDelete && c.State != domain.VMArtifactReady {
			return nil, repository.ErrConflict
		}
	}
	if op.ExportID != nil {
		e, err := s.cfg.Repository.Exports().Get(ctx, v.OrgID, *op.ExportID)
		if err != nil {
			return nil, err
		}
		if checkpoint == nil || e.CheckpointID != checkpoint.ID {
			return nil, domain.ErrInvalidValue
		}
		if op.Kind == domain.VMOperationExport {
			if e.State != domain.VMArtifactCreating {
				return nil, repository.ErrConflict
			}
			out = append(out, vmReservation(v, domain.VMExportResource, e.ID, domain.VMCapacity{DiskBytes: v.Allocation.DiskBytes}))
		}
	}
	if op.CloneTargetID != nil {
		target, err := s.cfg.Repository.Deployments().Get(ctx, v.OrgID, *op.CloneTargetID)
		if err != nil {
			return nil, err
		}
		if target.Generation != op.CloneTargetGeneration {
			return nil, repository.ErrConflict
		}
		if target.ID == v.ID || target.HostID != v.HostID || target.Identity.ProviderResourceID == v.Identity.ProviderResourceID || target.DesiredPower != domain.VMDesiredStopped || v.TPM.Enabled || target.TPM.Enabled || checkpoint == nil || checkpoint.TPMEnabled || target.ImageID != checkpoint.ImageID || target.ConfigDigest != checkpoint.ConfigDigest {
			return nil, domain.ErrInvalidValue
		}
		if _, _, err = s.references(ctx, p, target); err != nil {
			return nil, err
		}
		out = append(out, vmReservation(*target, domain.PersistentVMResource, target.ID, target.Allocation))
	}
	if op.Kind == domain.VMOperationCheckpoint || op.Kind == domain.VMOperationRestore || op.Kind == domain.VMOperationAdopt || (op.Plan != nil && op.Plan.RequiresStopped) {
		if obs == nil || obs.RuntimeState == nil || *obs.RuntimeState != domain.VMRuntimeStopped {
			return nil, repository.ErrConflict
		}
	}
	if op.Kind == domain.VMOperationRestore && checkpoint != nil && (checkpoint.ImageID != v.ImageID || checkpoint.ConfigDigest != v.ConfigDigest || checkpoint.Firmware != v.Firmware || checkpoint.TPMEnabled != v.TPM.Enabled) {
		return nil, repository.ErrConflict
	}
	if op.Kind == domain.VMOperationReboot && (obs == nil || obs.RuntimeState == nil || *obs.RuntimeState != domain.VMRuntimeRunning) {
		return nil, repository.ErrConflict
	}
	if op.Kind != domain.VMOperationDefine && op.Kind != domain.VMOperationAdopt && op.Kind != domain.VMOperationDelete && obs != nil && obs.RuntimeState != nil && *obs.RuntimeState == domain.VMRuntimeAbsent {
		return nil, repository.ErrConflict
	}
	return out, nil
}

func (s *PersistentVMService) CancelOperation(ctx context.Context, p *auth.Principal, org, id uuid.UUID) (*domain.VMOperation, error) {
	if err := s.authorize(ctx, p, org, domain.PermWriteDeployments); err != nil {
		return nil, err
	}
	op, err := s.cfg.Repository.GetOperation(ctx, org, id)
	if err != nil {
		return nil, err
	}
	// Once execution starts cancellation cannot prove that side effects stopped.
	if op.Phase != domain.VMOperationAccepted && op.Phase != domain.VMOperationAwaitingApproval {
		return nil, vmError(domain.VMErrorUnconfirmed)
	}
	result, err := s.cfg.Repository.TransitionOperation(ctx, repository.VMOperationTransition{OrgID: org, OperationID: id, ExpectedPhase: op.Phase, ExpectedRevision: op.Generation, Phase: domain.VMOperationCancelled})
	if err == nil {
		s.notify(ctx, *result)
	}
	return result, err
}
