package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
)

// VirtualizationPrincipal comes only from the transport-validated signed request
// (or authenticated gift-wrap rumor). No actor, role or tenant is inferred from
// a VM name or accepted from the mutation body.
type VirtualizationPrincipal struct {
	OrgID          uuid.UUID
	PubKey         string
	RequestEventID string
}
type VirtualizationMutation struct {
	OrgID              uuid.UUID                        `json:"org_id"`
	ID                 uuid.UUID                        `json:"id,omitempty"`
	ExpectedGeneration int64                            `json:"expected_generation,omitempty"`
	IdempotencyKey     string                           `json:"idempotency_key,omitempty"`
	Reason             string                           `json:"reason,omitempty"`
	VM                 *domain.PersistentVMDeployment   `json:"vm,omitempty"`
	Image              *domain.VMImage                  `json:"image,omitempty"`
	Plane              *domain.ExecutionPlaneDeployment `json:"plane,omitempty"`
	Operation          domain.VMOperationKind           `json:"operation,omitempty"`
	ApprovalID         *uuid.UUID                       `json:"approval_id,omitempty"`
	CheckpointID       *uuid.UUID                       `json:"checkpoint_id,omitempty"`
	ExportID           *uuid.UUID                       `json:"export_id,omitempty"`
	CloneTargetID      *uuid.UUID                       `json:"clone_target_id,omitempty"`
	DeleteTarget       domain.VMDeleteTarget            `json:"delete_target,omitempty"`
	DataDisposition    domain.VMDataDisposition         `json:"data_disposition,omitempty"`
	AllowForceStop     bool                             `json:"allow_force_stop,omitempty"`
}

// C/D adapters authorize and durably admit effects, including two-person approval
// and SecretRef validation. Handlers never call a provider or mutate repositories.
type PersistentVMMutations interface {
	MutatePersistentVM(context.Context, VirtualizationPrincipal, string, VirtualizationMutation) (VirtualizationAdmission, error)
}
type ExecutionPlaneMutations interface {
	MutateExecutionPlane(context.Context, VirtualizationPrincipal, string, VirtualizationMutation) (VirtualizationAdmission, error)
}
type VirtualizationAdmission struct {
	ResourceID  uuid.UUID
	OperationID uuid.UUID
	Generation  int64
}
type VirtualizationAcknowledgment struct {
	Status        string    `json:"status"`
	ResourceID    uuid.UUID `json:"resource_id"`
	OperationID   uuid.UUID `json:"operation_id"`
	Generation    int64     `json:"generation"`
	StateKind     int       `json:"state_kind"`
	AuditKind     int       `json:"audit_kind"`
	StateDTag     string    `json:"state_d_tag"`
	OperationDTag string    `json:"operation_d_tag,omitempty"`
	Author        string    `json:"author"`
	OrgID         uuid.UUID `json:"org_id"`
}
type VirtualizationHandlers struct {
	Query      readmodel.VirtualizationQuery
	RBAC       *auth.RBAC
	Persistent PersistentVMMutations
	Planes     ExecutionPlaneMutations
	// CanonicalAuthor is required for mutation admission so acknowledgments always
	// identify the actual configured projection signer, not the caller's key.
	CanonicalAuthor     string
	ProjectionReady     bool
	ProjectionAvailable func() bool
}

var virtualizationFamilies = map[string]domain.VirtualizationResourceKind{
	"virtualization-host": domain.VirtualizationHostResource, "vm-image": domain.VMImageResource, "persistent-vm": domain.PersistentVMResource, "execution-plane": domain.ExecutionPlaneResource, "vm-checkpoint": domain.VMCheckpointResource, "vm-export": domain.VMExportResource, "vm-operation": domain.VMOperationResource,
}

// VirtualizationMethods is the method catalog for registration and discovery.
func VirtualizationMethods() []string {
	methods := []string{"virtualization-host/list", "virtualization-host/get", "vm-image/list", "vm-image/get", "vm-image/register", "persistent-vm/list", "persistent-vm/get", "persistent-vm/create", "persistent-vm/update", "persistent-vm/operate", "execution-plane/list", "execution-plane/get", "execution-plane/create", "execution-plane/update", "execution-plane/reconcile", "vm-checkpoint/list", "vm-checkpoint/get", "vm-export/list", "vm-export/get", "vm-operation/get", "vm-operation/approve", "vm-operation/cancel"}
	return methods
}
func (h *VirtualizationHandlers) Register(t *EncryptedRequestTransport) {
	if t == nil {
		return
	}
	for _, method := range VirtualizationMethods() {
		method := method
		t.RegisterContextVMHandler(method, func(ctx context.Context, r ContextVMRequest) (any, error) { return h.Handle(ctx, method, r) })
	}
}
func strictVirtualizationParams(data json.RawMessage, out any) error {
	if len(data) == 0 || len(data) > 1<<20 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return domain.ErrInvalidValue
	}
	// _meta is transport-owned ContextVM metadata, not resource input. Keep
	// canonical progress-token clients compatible without forwarding arbitrary
	// metadata to admission services.
	if _, err := contextVMIdempotencyKey(data); err != nil {
		return domain.ErrInvalidValue
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return domain.ErrInvalidValue
	}
	delete(fields, "_meta")
	data, err := json.Marshal(fields)
	if err != nil {
		return domain.ErrInvalidValue
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return domain.ErrInvalidValue
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return domain.ErrInvalidValue
	}
	return nil
}
func (h *VirtualizationHandlers) principal(ctx context.Context, r ContextVMRequest, org uuid.UUID, write bool) (VirtualizationPrincipal, error) {
	if r.Event == nil || r.Event.PubKey == (nostr.PubKey{}) || org == uuid.Nil {
		return VirtualizationPrincipal{}, errors.New("authenticated principal and org_id required")
	}
	if h.RBAC == nil {
		return VirtualizationPrincipal{}, readmodel.ErrVirtualizationUnavailable
	}
	p := requestPrincipal(EncryptedRequest{Event: r.Event})
	perm := domain.PermReadDeployments
	if write {
		perm = domain.PermWriteDeployments
	}
	if err := h.RBAC.CheckPermission(ctx, p, org, perm); err != nil {
		return VirtualizationPrincipal{}, errors.New("access denied")
	}
	return VirtualizationPrincipal{org, p.PubKey, r.Event.ID.Hex()}, nil
}
func (h *VirtualizationHandlers) Handle(ctx context.Context, method string, r ContextVMRequest) (any, error) {
	parts := strings.Split(method, "/")
	if len(parts) != 2 {
		return nil, domain.ErrInvalidValue
	}
	kind, ok := virtualizationFamilies[parts[0]]
	if !ok {
		return nil, domain.ErrInvalidValue
	}
	action := parts[1]
	if action == "get" || action == "list" {
		var q struct {
			OrgID  uuid.UUID `json:"org_id"`
			ID     uuid.UUID `json:"id,omitempty"`
			Limit  int       `json:"limit,omitempty"`
			Offset int       `json:"offset,omitempty"`
		}
		if err := strictVirtualizationParams(r.RPC.Params, &q); err != nil {
			return nil, err
		}
		if _, err := h.principal(ctx, r, q.OrgID, false); err != nil {
			return nil, err
		}
		if action == "get" {
			v, err := h.Query.Get(ctx, q.OrgID, kind, q.ID)
			return v, publicVirtualizationError(err)
		}
		if q.Limit == 0 {
			q.Limit = 50
		}
		v, err := h.Query.List(ctx, q.OrgID, kind, q.Limit, q.Offset)
		return v, publicVirtualizationError(err)
	}
	valid := false
	for _, m := range VirtualizationMethods() {
		if m == method {
			valid = true
			break
		}
	}
	if !valid {
		return nil, domain.ErrInvalidValue
	}
	var mutation VirtualizationMutation
	if err := strictVirtualizationParams(r.RPC.Params, &mutation); err != nil {
		return nil, err
	}
	idempotencyKey, err := contextVMIdempotencyKey(r.RPC.Params)
	if err != nil {
		return nil, domain.ErrInvalidValue
	}
	mutation.IdempotencyKey = idempotencyKey
	principal, err := h.principal(ctx, r, mutation.OrgID, true)
	if err != nil {
		return nil, err
	}
	if !h.ProjectionReady || (h.ProjectionAvailable != nil && !h.ProjectionAvailable()) {
		return nil, readmodel.ErrVirtualizationUnavailable
	}
	if _, err := nostr.PubKeyFromHex(h.CanonicalAuthor); err != nil {
		return nil, readmodel.ErrVirtualizationUnavailable
	}
	// Reject spoofed tenant/creator/observations before handing typed desired data
	// to the service. Service adapters still perform full admission validation.
	if mutation.VM != nil {
		v := mutation.VM
		if v.OrgID != principal.OrgID || (action == "create" && v.CreatedBy != "" && v.CreatedBy != principal.PubKey) || v.Observation != nil {
			return nil, domain.ErrInvalidValue
		}
		if action == "create" || action == "register" {
			v.CreatedBy = principal.PubKey
		}
	}
	if mutation.Image != nil {
		v := mutation.Image
		if v.OrgID != principal.OrgID || (v.CreatedBy != "" && v.CreatedBy != principal.PubKey) {
			return nil, domain.ErrInvalidValue
		}
		if action == "create" || action == "register" {
			v.CreatedBy = principal.PubKey
		}
	}
	if mutation.Plane != nil {
		v := mutation.Plane
		if v.OrgID != principal.OrgID || (action == "create" && v.CreatedBy != "" && v.CreatedBy != principal.PubKey) || v.Observation != nil {
			return nil, domain.ErrInvalidValue
		}
		if action == "create" || action == "register" {
			v.CreatedBy = principal.PubKey
		}
	}
	var admission VirtualizationAdmission
	if kind == domain.ExecutionPlaneResource {
		if h.Planes == nil {
			return nil, readmodel.ErrVirtualizationUnavailable
		}
		admission, err = h.Planes.MutateExecutionPlane(ctx, principal, action, mutation)
	} else {
		if h.Persistent == nil {
			return nil, readmodel.ErrVirtualizationUnavailable
		}
		admission, err = h.Persistent.MutatePersistentVM(ctx, principal, method, mutation)
	}
	if err != nil {
		return nil, publicVirtualizationError(err)
	}
	if admission.ResourceID == uuid.Nil || admission.Generation < 1 || admission.OperationID == uuid.Nil {
		return nil, readmodel.ErrVirtualizationUnavailable
	}
	// Operation actions acknowledge the persistent resource plus the operation's
	// own coordinate; execution-plane IDs remain plane correlation IDs, not VM jobs.
	resourceKind := kind
	if kind == domain.VMOperationResource {
		resourceKind = domain.PersistentVMResource
	}
	coordinate, err := dto.VirtualizationCoordinate(resourceKind, admission.ResourceID)
	if err != nil {
		return nil, err
	}
	ack := VirtualizationAcknowledgment{Status: "accepted", ResourceID: admission.ResourceID, OperationID: admission.OperationID, Generation: admission.Generation, StateKind: kinds.CASControlState, AuditKind: kinds.CASAudit, StateDTag: coordinate, Author: h.CanonicalAuthor, OrgID: principal.OrgID}
	if resourceKind == domain.PersistentVMResource {
		ack.OperationDTag, _ = dto.VirtualizationCoordinate(domain.VMOperationResource, admission.OperationID)
	}
	return ack, nil
}
func publicVirtualizationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return errors.New("resource not found")
	}
	if errors.Is(err, repository.ErrConflict) {
		return errors.New("virtualization conflict")
	}
	if errors.Is(err, domain.ErrInvalidValue) {
		return domain.ErrInvalidValue
	}
	var provider *domain.VMProviderError
	if errors.As(err, &provider) {
		code := dto.PublicVMDiagnostic(provider.Code)
		if code == "" {
			return readmodel.ErrVirtualizationUnavailable
		}
		return errors.New(string(code))
	}
	return readmodel.ErrVirtualizationUnavailable
}
