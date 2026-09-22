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
	"go.uber.org/zap"
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
func TestVirtualizationContextVMRegistrationPrincipalAndUnavailable(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	h := &VirtualizationHandlers{RBAC: auth.NewRBAC(vmContextMembers{org, domain.RoleDeployer}), CanonicalAuthor: strings.Repeat("a", 64), ProjectionReady: true}
	transport := NewEncryptedRequestTransport(nil, nil, nil, zap.NewNop())
	h.Register(transport)
	for _, method := range VirtualizationMethods() {
		if transport.contextVMHandlers[method] == nil {
			t.Fatalf("unregistered %s", method)
		}
	}
	req := vmContextRequest(t, VirtualizationMutation{OrgID: org, ID: id, ExpectedGeneration: 1, Operation: domain.VMOperationStart})
	if _, err := h.Handle(context.Background(), "persistent-vm/operate", req); !errors.Is(err, readmodel.ErrVirtualizationUnavailable) {
		t.Fatal(err)
	}
	svc := &vmAdmissionFixture{}
	h.Persistent = svc
	h.Planes = svc
	result, err := transport.contextVMHandlers["persistent-vm/operate"](context.Background(), req)
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
func TestVirtualizationSignedTransportMetadataAndReplay(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	pub := &mockEncryptedPublisher{}
	responder := newResponder(t, pub)
	svc := &vmAdmissionFixture{}
	h := &VirtualizationHandlers{RBAC: auth.NewRBAC(vmContextMembers{org, domain.RoleDeployer}), Persistent: svc, CanonicalAuthor: responder.ServicePubkey(), ProjectionReady: true}
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "vm-start", "method": "persistent-vm/operate", "params": map[string]any{"org_id": org, "id": id, "expected_generation": 1, "operation": "start", "_meta": map[string]any{"progressToken": "vm-request"}}})
	event := makeContextVMEvent(t, testRequesterKey, string(payload))
	transport := NewEncryptedRequestTransport(nil, responder, []string{event.PubKey.Hex()}, zap.NewNop())
	h.Register(transport)
	transport.HandleEvent(context.Background(), event)
	transport.HandleEvent(context.Background(), event)
	if svc.key != "vm-request" || svc.calls != 1 || svc.principal.RequestEventID != event.ID.Hex() || svc.principal.PubKey != event.PubKey.Hex() {
		t.Fatalf("transport identity or replay failed: %+v", svc)
	}
	pub.mu.Lock()
	responses := append([]nostr.Event(nil), pub.events...)
	pub.mu.Unlock()
	found := false
	for _, response := range responses {
		if strings.Contains(response.Content, "state_d_tag") && strings.Contains(response.Content, "persistent-vm:"+id.String()) {
			found = true
		}
	}
	if !found {
		t.Fatal("no signed admission acknowledgment")
	}
	bad := *event
	bad.Content = string(payload) + " "
	transport.HandleEvent(context.Background(), &bad)
	if svc.calls != 1 {
		t.Fatal("invalid event admitted")
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
