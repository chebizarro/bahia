package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type encryptedPaymentRepo struct {
	lastWorker string
	lastLimit  int
	records    []domain.PaymentRecord
}

func (r *encryptedPaymentRepo) Create(context.Context, *domain.PaymentRecord) error { return nil }
func (r *encryptedPaymentRepo) GetByID(context.Context, uuid.UUID) (*domain.PaymentRecord, error) {
	return nil, repository.ErrNotFound
}
func (r *encryptedPaymentRepo) ListByRun(context.Context, uuid.UUID) ([]domain.PaymentRecord, error) {
	return nil, nil
}
func (r *encryptedPaymentRepo) ListByWorker(_ context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	r.lastWorker = workerPubkey
	r.lastLimit = limit
	return r.records, nil
}
func (r *encryptedPaymentRepo) UpdateStatus(context.Context, uuid.UUID, domain.PaymentStatus, string) error {
	return nil
}
func (r *encryptedPaymentRepo) GetByTokenHash(context.Context, string) (*domain.PaymentRecord, error) {
	return nil, repository.ErrNotFound
}

type encryptedOrgRepo struct {
	byID   map[uuid.UUID]*domain.Organization
	byName map[string]*domain.Organization
}

func newEncryptedOrgRepo() *encryptedOrgRepo {
	return &encryptedOrgRepo{byID: map[uuid.UUID]*domain.Organization{}, byName: map[string]*domain.Organization{}}
}
func (r *encryptedOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	copy := *org
	r.byID[org.ID] = &copy
	r.byName[org.Name] = &copy
	return nil
}
func (r *encryptedOrgRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	if org := r.byID[id]; org != nil {
		copy := *org
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *encryptedOrgRepo) GetByName(_ context.Context, name string) (*domain.Organization, error) {
	if org := r.byName[name]; org != nil {
		copy := *org
		return &copy, nil
	}
	return nil, repository.ErrNotFound
}
func (r *encryptedOrgRepo) List(context.Context) ([]domain.Organization, error) { return nil, nil }
func (r *encryptedOrgRepo) Update(_ context.Context, org *domain.Organization) error {
	copy := *org
	r.byID[org.ID] = &copy
	r.byName[org.Name] = &copy
	return nil
}
func (r *encryptedOrgRepo) Delete(_ context.Context, id uuid.UUID) error {
	org := r.byID[id]
	if org == nil {
		return repository.ErrNotFound
	}
	delete(r.byName, org.Name)
	delete(r.byID, id)
	return nil
}

type encryptedMemberRepo struct {
	members []domain.OrgMember
}

func (r *encryptedMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	r.members = append(r.members, *member)
	return nil
}
func (r *encryptedMemberRepo) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	for _, member := range r.members {
		if member.OrgID == orgID && member.Pubkey == pubkey {
			copy := member
			return &copy, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *encryptedMemberRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	var out []domain.OrgMember
	for _, member := range r.members {
		if member.OrgID == orgID {
			out = append(out, member)
		}
	}
	return out, nil
}
func (r *encryptedMemberRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	var out []domain.OrgMember
	for _, member := range r.members {
		if member.Pubkey == pubkey {
			out = append(out, member)
		}
	}
	return out, nil
}
func (r *encryptedMemberRepo) UpdateRole(_ context.Context, orgID uuid.UUID, pubkey string, role domain.Role) error {
	for i := range r.members {
		if r.members[i].OrgID == orgID && r.members[i].Pubkey == pubkey {
			r.members[i].Role = role
			return nil
		}
	}
	return repository.ErrNotFound
}
func (r *encryptedMemberRepo) Remove(_ context.Context, orgID uuid.UUID, pubkey string) error {
	for i, member := range r.members {
		if member.OrgID == orgID && member.Pubkey == pubkey {
			r.members = append(r.members[:i], r.members[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type encryptedInviteRepo struct {
	invites []domain.OrgInvite
}

func (r *encryptedInviteRepo) Create(_ context.Context, invite *domain.OrgInvite) error {
	if invite.ID == uuid.Nil {
		invite.ID = uuid.New()
	}
	r.invites = append(r.invites, *invite)
	return nil
}
func (r *encryptedInviteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.OrgInvite, error) {
	for _, invite := range r.invites {
		if invite.ID == id {
			copy := invite
			return &copy, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *encryptedInviteRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgInvite, error) {
	var out []domain.OrgInvite
	for _, invite := range r.invites {
		if invite.OrgID == orgID {
			out = append(out, invite)
		}
	}
	return out, nil
}
func (r *encryptedInviteRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgInvite, error) {
	var out []domain.OrgInvite
	for _, invite := range r.invites {
		if invite.Pubkey == pubkey {
			out = append(out, invite)
		}
	}
	return out, nil
}
func (r *encryptedInviteRepo) Delete(_ context.Context, id uuid.UUID) error {
	for i, invite := range r.invites {
		if invite.ID == id {
			r.invites = append(r.invites[:i], r.invites[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}
func (r *encryptedInviteRepo) DeleteExpired(context.Context) (int, error) { return 0, nil }

func encryptedRequestForTest(t *testing.T, pubkey string, payload any) EncryptedRequest {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return EncryptedRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromHex(t, pubkey)}, Envelope: EncryptedRequestEnvelope{Payload: encoded}}
}

func TestEncryptedDomainHandlers_PaymentHistoryUsesEncryptedOperationPayload(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	paymentRepo := &encryptedPaymentRepo{records: []domain.PaymentRecord{{WorkerPubkey: "worker-a", AmountSats: 21}}}
	handlers := NewEncryptedDomainHandlers(EncryptedDomainHandlersConfig{
		Payments: service.NewPaymentService(paymentRepo, nil, nil, zap.NewNop()),
		Logger:   zap.NewNop(),
	})

	result, err := handlers.PaymentHistory(context.Background(), encryptedRequestForTest(t, requesterPubkey, map[string]any{"worker": "worker-a", "limit": 999}))
	if err != nil {
		t.Fatalf("PaymentHistory: %v", err)
	}
	if paymentRepo.lastWorker != "worker-a" || paymentRepo.lastLimit != 250 {
		t.Fatalf("ListByWorker args = (%q, %d), want (worker-a, 250)", paymentRepo.lastWorker, paymentRepo.lastLimit)
	}
	if got := result.([]domain.PaymentRecord); len(got) != 1 || got[0].AmountSats != 21 {
		t.Fatalf("unexpected payment result: %#v", result)
	}
}

func TestEncryptedDomainHandlers_CreateOrgHonorsBootstrapOwnerAndAddsOwner(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	orgs := newEncryptedOrgRepo()
	members := &encryptedMemberRepo{}
	handlers := NewEncryptedDomainHandlers(EncryptedDomainHandlersConfig{
		Orgs:                  orgs,
		Members:               members,
		Invites:               &encryptedInviteRepo{},
		RBAC:                  auth.NewRBAC(members),
		BootstrapOwnerPubkeys: []string{requesterPubkey},
		Logger:                zap.NewNop(),
	})

	result, err := handlers.CreateOrg(context.Background(), encryptedRequestForTest(t, requesterPubkey, map[string]any{"name": "demo-org", "display_name": "Demo Org"}))
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}
	org := result.(*domain.Organization)
	if org.Name != "demo-org" || org.OwnerPubkey != requesterPubkey {
		t.Fatalf("unexpected org: %+v", org)
	}
	member, err := members.GetMember(context.Background(), org.ID, requesterPubkey)
	if err != nil {
		t.Fatalf("owner membership missing: %v", err)
	}
	if member.Role != domain.RoleOwner {
		t.Fatalf("owner role = %q", member.Role)
	}
}

func TestEncryptedDomainHandlers_CreateInviteRejectsOwnerRoleFromAdmin(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	orgID := uuid.New()
	orgs := newEncryptedOrgRepo()
	_ = orgs.Create(context.Background(), &domain.Organization{ID: orgID, Name: "demo", DisplayName: "Demo", OwnerPubkey: "owner"})
	members := &encryptedMemberRepo{members: []domain.OrgMember{{OrgID: orgID, Pubkey: requesterPubkey, Role: domain.RoleAdmin}}}
	invites := &encryptedInviteRepo{}
	handlers := NewEncryptedDomainHandlers(EncryptedDomainHandlersConfig{Orgs: orgs, Members: members, Invites: invites, RBAC: auth.NewRBAC(members), Logger: zap.NewNop()})

	_, err := handlers.CreateInvite(context.Background(), encryptedRequestForTest(t, requesterPubkey, map[string]any{
		"org_id": orgID.String(),
		"pubkey": "b",
		"role":   string(domain.RoleOwner),
	}))
	if err == nil || err.Error() != "only owners can grant owner role" {
		t.Fatalf("CreateInvite owner role error = %v", err)
	}
	if len(invites.invites) != 0 {
		t.Fatalf("admin owner-role invite should not be persisted: %#v", invites.invites)
	}
}

func TestEncryptedDomainHandlers_OrgDetailReturnsAdminInvitesEncrypted(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	orgID := uuid.New()
	orgs := newEncryptedOrgRepo()
	_ = orgs.Create(context.Background(), &domain.Organization{ID: orgID, Name: "demo", DisplayName: "Demo", OwnerPubkey: requesterPubkey})
	members := &encryptedMemberRepo{members: []domain.OrgMember{{OrgID: orgID, Pubkey: requesterPubkey, Role: domain.RoleAdmin}}}
	invites := &encryptedInviteRepo{invites: []domain.OrgInvite{{ID: uuid.New(), OrgID: orgID, Pubkey: "b", Role: domain.RoleViewer, ExpiresAt: time.Now().Add(time.Hour)}}}
	handlers := NewEncryptedDomainHandlers(EncryptedDomainHandlersConfig{Orgs: orgs, Members: members, Invites: invites, RBAC: auth.NewRBAC(members), Logger: zap.NewNop()})

	result, err := handlers.OrgDetail(context.Background(), encryptedRequestForTest(t, requesterPubkey, map[string]any{"id": orgID.String()}))
	if err != nil {
		t.Fatalf("OrgDetail: %v", err)
	}
	detail := result.(map[string]any)
	if detail["my_role"] != "admin" {
		t.Fatalf("my_role = %#v", detail["my_role"])
	}
	if got := detail["invites"].([]domain.OrgInvite); len(got) != 1 {
		t.Fatalf("invites = %#v", detail["invites"])
	}
}
