package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/readmodel"
	"github.com/openagentsinc/bahia/internal/repository"
)

type vmContextMembers struct {
	org  uuid.UUID
	role domain.Role
}

func (m vmContextMembers) GetMember(_ context.Context, org uuid.UUID, key string) (*domain.OrgMember, error) {
	if org != m.org {
		return nil, repository.ErrNotFound
	}
	return &domain.OrgMember{OrgID: org, Pubkey: key, Role: m.role}, nil
}
func (m vmContextMembers) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}

type vmAdmissionFixture struct {
	principal VirtualizationPrincipal
	method    string
	key       string
	calls     int
	err       error
}

func (s *vmAdmissionFixture) MutatePersistentVM(_ context.Context, p VirtualizationPrincipal, method string, m VirtualizationMutation) (VirtualizationAdmission, error) {
	s.calls++
	s.principal = p
	s.method = method
	s.key = m.IdempotencyKey
	return VirtualizationAdmission{ResourceID: m.ID, OperationID: uuid.New(), Generation: 2}, s.err
}
func (s *vmAdmissionFixture) MutateExecutionPlane(ctx context.Context, p VirtualizationPrincipal, method string, m VirtualizationMutation) (VirtualizationAdmission, error) {
	return s.MutatePersistentVM(ctx, p, method, m)
}
func vmContextRequest(t *testing.T, payload any) ContextVMRequest {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	key, err := nostr.PubKeyFromHex(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return ContextVMRequest{Event: &nostr.Event{PubKey: key}, RPC: ContextVMJSONRPCRequest{JSONRPC: "2.0", Params: data}}
}
func TestVirtualizationPrincipalAndUnavailable(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	h := &VirtualizationHandlers{RBAC: auth.NewRBAC(vmContextMembers{org, domain.RoleDeployer}), CanonicalAuthor: strings.Repeat("a", 64), ProjectionReady: true}
	req := vmContextRequest(t, VirtualizationMutation{OrgID: org, ID: id, ExpectedGeneration: 1, Operation: domain.VMOperationStart})
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", req); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
		t.Fatal(err)
	}
	svc := &vmAdmissionFixture{}
	h.Persistent = svc
	h.Planes = svc
	result, err := h.Handle(context.Background(), "persistent-vm/operate", req)
	if err != nil {
		t.Fatal(err)
	}
	ack := result.(VirtualizationAcknowledgment)
	if ack.ResourceID != id || ack.Generation != 2 || ack.OperationDTag == "" || ack.Author != h.CanonicalAuthor || ack.Status != "accepted" {
		t.Fatalf("bad admission: %+v", ack)
	}
	if svc.principal.OrgID != org || svc.principal.PubKey != req.Event.PubKey.Hex() {
		t.Fatal("wrong identity")
	}
	h.ProjectionReady = false
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", req); err == nil {
		t.Fatal("admission without durable projection")
	}
	h.ProjectionReady = true
	if _, err := h.Handle(context.Background(), "execution-plane/reconcile", req); err != nil {
		t.Fatal(err)
	}
	nilReq := req
	nilReq.Event = nil
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", nilReq); err == nil {
		t.Fatal("nil principal")
	}
	spoof := vmContextRequest(t, map[string]any{"org_id": org, "id": id, "actor": "owner"})
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", spoof); err == nil {
		t.Fatal("actor injection")
	}
	cross := vmContextRequest(t, VirtualizationMutation{OrgID: uuid.New(), ID: id})
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", cross); err == nil {
		t.Fatal("cross tenant")
	}
	h.RBAC = nil
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", req); err == nil {
		t.Fatal("missing RBAC")
	}
	if svc.calls != 2 {
		t.Fatalf("unauthorized service calls %d", svc.calls)
	}
}
func TestVirtualizationMutationErrorsNeverExposeProviderEvidence(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	svc := &vmAdmissionFixture{err: &domain.VMProviderError{Code: domain.VMErrorUnavailable, Cause: errors.New("password=sentinel /private/libvirt")}}
	h := &VirtualizationHandlers{RBAC: auth.NewRBAC(vmContextMembers{org, domain.RoleDeployer}), Persistent: svc, CanonicalAuthor: strings.Repeat("a", 64), ProjectionReady: true}
	_, err := h.Handle(context.Background(), "persistent-vm/operate", vmContextRequest(t, VirtualizationMutation{OrgID: org, ID: id}))
	if err == nil || strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), "libvirt") {
		t.Fatal(err)
	}
	h.RBAC = auth.NewRBAC(vmContextMembers{org, domain.RoleViewer})
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", vmContextRequest(t, VirtualizationMutation{OrgID: org, ID: id})); err == nil {
		t.Fatal("viewer mutation")
	}
}
