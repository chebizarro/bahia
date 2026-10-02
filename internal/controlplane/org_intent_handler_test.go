package controlplane

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// --- test doubles ---

type memOrgRepo struct {
	mu   sync.RWMutex
	orgs map[uuid.UUID]*domain.Organization
}

func newMemOrgRepo() *memOrgRepo {
	return &memOrgRepo{orgs: make(map[uuid.UUID]*domain.Organization)}
}

func (r *memOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	org.CreatedAt = time.Now()
	org.UpdatedAt = org.CreatedAt
	r.orgs[org.ID] = org
	return nil
}

func (r *memOrgRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	org, ok := r.orgs[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *org
	return &copy, nil
}

func (r *memOrgRepo) GetByName(_ context.Context, name string) (*domain.Organization, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, org := range r.orgs {
		if org.Name == name {
			copy := *org
			return &copy, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (r *memOrgRepo) List(_ context.Context) ([]domain.Organization, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.Organization
	for _, org := range r.orgs {
		result = append(result, *org)
	}
	return result, nil
}

func (r *memOrgRepo) Update(_ context.Context, org *domain.Organization) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.orgs[org.ID]
	if !ok {
		return repository.ErrNotFound
	}
	org.CreatedAt = existing.CreatedAt
	org.UpdatedAt = time.Now()
	r.orgs[org.ID] = org
	return nil
}

func (r *memOrgRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.orgs[id]; !ok {
		return repository.ErrNotFound
	}
	delete(r.orgs, id)
	return nil
}

type memOrgMemberRepo struct {
	mu      sync.RWMutex
	members map[string]*domain.OrgMember // key: orgID:pubkey
}

func newMemOrgMemberRepo() *memOrgMemberRepo {
	return &memOrgMemberRepo{members: make(map[string]*domain.OrgMember)}
}

func orgMemberKey(orgID uuid.UUID, pubkey string) string {
	return orgID.String() + ":" + pubkey
}

func (r *memOrgMemberRepo) Add(_ context.Context, member *domain.OrgMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	member.JoinedAt = time.Now()
	member.UpdatedAt = member.JoinedAt
	r.members[orgMemberKey(member.OrgID, member.Pubkey)] = member
	return nil
}

func (r *memOrgMemberRepo) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.members[orgMemberKey(orgID, pubkey)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *m
	return &copy, nil
}

func (r *memOrgMemberRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.OrgMember
	for _, m := range r.members {
		if m.OrgID == orgID {
			result = append(result, *m)
		}
	}
	return result, nil
}

func (r *memOrgMemberRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.OrgMember
	for _, m := range r.members {
		if m.Pubkey == pubkey {
			result = append(result, *m)
		}
	}
	return result, nil
}

func (r *memOrgMemberRepo) UpdateRole(_ context.Context, orgID uuid.UUID, pubkey string, role domain.Role) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[orgMemberKey(orgID, pubkey)]
	if !ok {
		return repository.ErrNotFound
	}
	m.Role = role
	m.UpdatedAt = time.Now()
	return nil
}

func (r *memOrgMemberRepo) Remove(_ context.Context, orgID uuid.UUID, pubkey string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := orgMemberKey(orgID, pubkey)
	if _, ok := r.members[key]; !ok {
		return repository.ErrNotFound
	}
	delete(r.members, key)
	return nil
}

type memOrgInviteRepo struct {
	mu      sync.RWMutex
	invites map[uuid.UUID]*domain.OrgInvite
}

func newMemOrgInviteRepo() *memOrgInviteRepo {
	return &memOrgInviteRepo{invites: make(map[uuid.UUID]*domain.OrgInvite)}
}

func (r *memOrgInviteRepo) Create(_ context.Context, invite *domain.OrgInvite) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if invite.ID == uuid.Nil {
		invite.ID = uuid.New()
	}
	invite.CreatedAt = time.Now()
	r.invites[invite.ID] = invite
	return nil
}

func (r *memOrgInviteRepo) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*domain.OrgInvite, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inv, ok := r.invites[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	copy := *inv
	return &copy, nil
}

func (r *memOrgInviteRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.OrgInvite, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.OrgInvite
	for _, inv := range r.invites {
		if inv.OrgID == orgID {
			result = append(result, *inv)
		}
	}
	return result, nil
}

func (r *memOrgInviteRepo) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgInvite, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.OrgInvite
	for _, inv := range r.invites {
		if inv.Pubkey == pubkey {
			result = append(result, *inv)
		}
	}
	return result, nil
}

func (r *memOrgInviteRepo) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.invites[id]; !ok {
		return repository.ErrNotFound
	}
	delete(r.invites, id)
	return nil
}

func (r *memOrgInviteRepo) DeleteExpired(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	for id, inv := range r.invites {
		if inv.IsExpired() {
			delete(r.invites, id)
			count++
		}
	}
	return count, nil
}

// stubOrgPublisher records publish calls and captures content for encryption tests.
type stubOrgPublisher struct {
	mu               sync.Mutex
	publishedOrgs    []orgPublishRecord
	publishedMembers []memberPublishRecord
	publishedInvites []invitePublishRecord
}

type orgPublishRecord struct {
	Org     *domain.Organization
	Deleted bool
}

type memberPublishRecord struct {
	Member  *domain.OrgMember
	Deleted bool
}

type invitePublishRecord struct {
	Invite  *domain.OrgInvite
	Deleted bool
}

func (p *stubOrgPublisher) PublishOrg(_ context.Context, org *domain.Organization, deleted bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishedOrgs = append(p.publishedOrgs, orgPublishRecord{Org: org, Deleted: deleted})
	return nil
}

func (p *stubOrgPublisher) PublishMember(_ context.Context, member *domain.OrgMember, deleted bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishedMembers = append(p.publishedMembers, memberPublishRecord{Member: member, Deleted: deleted})
	return nil
}

func (p *stubOrgPublisher) PublishInvite(_ context.Context, invite *domain.OrgInvite, deleted bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishedInvites = append(p.publishedInvites, invitePublishRecord{Invite: invite, Deleted: deleted})
	return nil
}

// --- test helpers ---

func newTestOrgHandler(t *testing.T) (*OrgIntentHandler, *memOrgRepo, *memOrgMemberRepo, *memOrgInviteRepo, *stubOrgPublisher, *[]uuid.UUID) {
	t.Helper()
	orgs := newMemOrgRepo()
	members := newMemOrgMemberRepo()
	invites := newMemOrgInviteRepo()
	pub := &stubOrgPublisher{}
	var memberChanges []uuid.UUID
	handler := NewOrgIntentHandler(OrgIntentHandlerConfig{
		Orgs:      orgs,
		Members:   members,
		Invites:   invites,
		Publisher: pub,
		Logger:    zap.NewNop(),
		OnMemberChange: func(orgID uuid.UUID) {
			memberChanges = append(memberChanges, orgID)
		},
	})
	return handler, orgs, members, invites, pub, &memberChanges
}

func orgCreateIntent(orgID uuid.UUID, name, displayName, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "create",
		Schema:     "bahia.intent.org.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: orgID.String(),
		Content: map[string]interface{}{
			"id":           orgID.String(),
			"name":         name,
			"display_name": displayName,
		},
		Actor: actor,
	}
}

func memberAddIntent(orgID uuid.UUID, pubkey string, role domain.Role, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "create",
		Schema:     "bahia.intent.org-member.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: "org:member:" + orgID.String() + ":" + pubkey,
		Content: map[string]interface{}{
			"org_id": orgID.String(),
			"pubkey": pubkey,
			"role":   string(role),
		},
		Actor: actor,
	}
}

func memberRoleChangeIntent(orgID uuid.UUID, pubkey string, role domain.Role, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "update",
		Schema:     "bahia.intent.org-member.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: "org:member:" + orgID.String() + ":" + pubkey,
		Content: map[string]interface{}{
			"org_id": orgID.String(),
			"pubkey": pubkey,
			"role":   string(role),
		},
		Actor: actor,
	}
}

func memberRemoveIntent(orgID uuid.UUID, pubkey string, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "delete",
		Schema:     "bahia.intent.org-member.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: "org:member:" + orgID.String() + ":" + pubkey,
		Content: map[string]interface{}{
			"org_id":  orgID.String(),
			"pubkey":  pubkey,
			"deleted": true,
		},
		Actor: actor,
	}
}

func inviteCreateIntent(orgID, inviteID uuid.UUID, pubkey string, role domain.Role, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "create",
		Schema:     "bahia.intent.org-invite.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: inviteID.String(),
		Content: map[string]interface{}{
			"id":     inviteID.String(),
			"org_id": orgID.String(),
			"pubkey": pubkey,
			"role":   string(role),
		},
		Actor: actor,
	}
}

func inviteRevokeIntent(orgID, inviteID uuid.UUID, actor string) *Intent {
	return &Intent{
		Domain:     "org",
		Op:         "delete",
		Schema:     "bahia.intent.org-invite.v1",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Coordinate: inviteID.String(),
		Content: map[string]interface{}{
			"id":      inviteID.String(),
			"org_id":  orgID.String(),
			"deleted": true,
		},
		Actor: actor,
	}
}

// --- handler tests ---

func TestOrgIntentHandler_CreateOrg(t *testing.T) {
	handler, orgs, members, _, pub, changes := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	actor := "abc123"
	intent := orgCreateIntent(orgID, "my-org", "My Org", actor)

	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("HandleIntent failed: %v", err)
	}

	org, err := orgs.GetByID(ctx, orgID)
	if err != nil {
		t.Fatalf("org not found: %v", err)
	}
	if org.Name != "my-org" {
		t.Errorf("org.Name = %q, want %q", org.Name, "my-org")
	}

	member, err := members.GetMember(ctx, orgID, actor)
	if err != nil {
		t.Fatalf("creator not found as member: %v", err)
	}
	if member.Role != domain.RoleOwner {
		t.Errorf("creator role = %q, want %q", member.Role, domain.RoleOwner)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.publishedOrgs) != 1 || pub.publishedOrgs[0].Deleted {
		t.Errorf("expected 1 live org publish, got %d", len(pub.publishedOrgs))
	}
	if len(pub.publishedMembers) != 1 || pub.publishedMembers[0].Deleted {
		t.Errorf("expected 1 live member publish, got %d", len(pub.publishedMembers))
	}

	if len(*changes) != 1 || (*changes)[0] != orgID {
		t.Errorf("expected 1 member change for orgID %s, got %v", orgID, *changes)
	}
}

func TestOrgIntentHandler_MemberAdd(t *testing.T) {
	handler, orgs, members, _, pub, changes := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "owner123", Role: domain.RoleOwner})

	intent := memberAddIntent(orgID, "member456", domain.RoleAdmin, "owner123")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("HandleIntent failed: %v", err)
	}

	m, err := members.GetMember(ctx, orgID, "member456")
	if err != nil {
		t.Fatalf("member not found: %v", err)
	}
	if m.Role != domain.RoleAdmin {
		t.Errorf("member.Role = %q, want %q", m.Role, domain.RoleAdmin)
	}

	pub.mu.Lock()
	if len(pub.publishedMembers) != 1 || pub.publishedMembers[0].Deleted {
		t.Errorf("expected 1 live member publish, got %d", len(pub.publishedMembers))
	}
	pub.mu.Unlock()
	if len(*changes) != 1 {
		t.Errorf("expected 1 member change, got %d", len(*changes))
	}
}

func TestOrgIntentHandler_MemberRoleChange(t *testing.T) {
	handler, orgs, members, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "owner123", Role: domain.RoleOwner})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "member456", Role: domain.RoleViewer})

	intent := memberRoleChangeIntent(orgID, "member456", domain.RoleAdmin, "owner123")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("HandleIntent failed: %v", err)
	}

	m, _ := members.GetMember(ctx, orgID, "member456")
	if m.Role != domain.RoleAdmin {
		t.Errorf("member.Role = %q, want %q", m.Role, domain.RoleAdmin)
	}
}

func TestOrgIntentHandler_MemberRemove(t *testing.T) {
	handler, orgs, members, _, pub, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "owner123", Role: domain.RoleOwner})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "member456", Role: domain.RoleViewer})

	intent := memberRemoveIntent(orgID, "member456", "owner123")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("HandleIntent failed: %v", err)
	}

	_, err := members.GetMember(ctx, orgID, "member456")
	if err != repository.ErrNotFound {
		t.Errorf("expected member to be removed, got err=%v", err)
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	found := false
	for _, rec := range pub.publishedMembers {
		if rec.Deleted && rec.Member.Pubkey == "member456" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected member tombstone to be published")
	}
}

func TestOrgIntentHandler_InviteCreateAndRevoke(t *testing.T) {
	handler, orgs, _, invites, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})

	inviteID := uuid.New()
	intent := inviteCreateIntent(orgID, inviteID, "invitee789", domain.RoleViewer, "owner")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("create invite failed: %v", err)
	}

	inv, err := invites.GetByID(ctx, orgID, inviteID)
	if err != nil {
		t.Fatalf("invite not found: %v", err)
	}
	if inv.Pubkey != "invitee789" {
		t.Errorf("invite.Pubkey = %q, want %q", inv.Pubkey, "invitee789")
	}

	revokeIntent := inviteRevokeIntent(orgID, inviteID, "owner")
	if err := handler.HandleIntent(ctx, revokeIntent); err != nil {
		t.Fatalf("revoke invite failed: %v", err)
	}

	_, err = invites.GetByID(ctx, orgID, inviteID)
	if err != repository.ErrNotFound {
		t.Errorf("expected invite to be revoked, got err=%v", err)
	}
}

func TestOrgIntentHandler_StaleRevisionConflict(t *testing.T) {
	handler, orgs, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	now := time.Now()
	_ = orgs.Create(ctx, &domain.Organization{
		ID: orgID, Name: "test-org", DisplayName: "Test", UpdatedAt: now,
	})

	staleTime := now.Add(-time.Hour).UnixNano()
	intent := &Intent{
		Domain: "org", Op: "update", Schema: "bahia.intent.org.v1",
		OrgID: orgID, IntentID: uuid.New().String(), Coordinate: orgID.String(),
		Content:           map[string]interface{}{"id": orgID.String(), "display_name": "Updated"},
		ExpectedUpdatedAt: &staleTime,
		Actor:             "owner",
	}

	err := handler.HandleIntent(ctx, intent)
	if err == nil {
		t.Fatalf("expected revision conflict error")
	}
	if !IsRevisionConflict(err) {
		t.Errorf("expected revision conflict, got: %v", err)
	}
}

func TestOrgIntentHandler_IdempotentMemberAdd(t *testing.T) {
	handler, orgs, members, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})
	_ = members.Add(ctx, &domain.OrgMember{OrgID: orgID, Pubkey: "member456", Role: domain.RoleAdmin})

	intent := memberAddIntent(orgID, "member456", domain.RoleAdmin, "owner")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("idempotent member add should not fail: %v", err)
	}

	m, _ := members.GetMember(ctx, orgID, "member456")
	if m.Role != domain.RoleAdmin {
		t.Errorf("role should remain admin, got %q", m.Role)
	}
}

func TestOrgIntentHandler_LegacyPathPublishes(t *testing.T) {
	handler, orgs, _, _, pub, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	intent := orgCreateIntent(orgID, "legacy-org", "Legacy", "actor")
	if err := handler.HandleIntent(ctx, intent); err != nil {
		t.Fatalf("HandleIntent failed: %v", err)
	}

	_, _ = orgs.GetByID(ctx, orgID)

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.publishedOrgs) == 0 {
		t.Errorf("expected at least one org publish from intent handler (legacy path)")
	}
}

func TestOrgIntentHandler_RelayHydrationUpdatesTrustSet(t *testing.T) {
	handler, orgs, _, _, _, changes := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	_ = orgs.Create(ctx, &domain.Organization{ID: orgID, Name: "test-org"})

	_ = handler.HandleIntent(ctx, memberAddIntent(orgID, "member1", domain.RoleAdmin, "owner"))
	_ = handler.HandleIntent(ctx, memberRoleChangeIntent(orgID, "member1", domain.RoleViewer, "owner"))
	_ = handler.HandleIntent(ctx, memberRemoveIntent(orgID, "member1", "owner"))

	if len(*changes) != 3 {
		t.Errorf("expected 3 member change callbacks, got %d", len(*changes))
	}
}

// --- SelfAuthorizingHandler processor-level tests (item 6) ---

func TestSelfAuthorizingHandler_OrgCreateFromNonFleetOpRejected(t *testing.T) {
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	trustSet := NewTrustSet([]string{"fleet-op-1"}, zap.NewNop())
	intent := orgCreateIntent(uuid.New(), "my-org", "My Org", "non-fleet-op")

	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err == nil {
		t.Fatalf("expected authorization failure for non-fleet-op creating org")
	}
}

func TestSelfAuthorizingHandler_OrgCreateByFleetOpAccepted(t *testing.T) {
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	trustSet := NewTrustSet([]string{"fleet-op-1"}, zap.NewNop())
	intent := orgCreateIntent(uuid.New(), "my-org", "My Org", "fleet-op-1")

	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err != nil {
		t.Fatalf("expected fleet op to be authorized for org create: %v", err)
	}
}

func TestSelfAuthorizingHandler_MemberAddByViewerRejected(t *testing.T) {
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	trustSet := NewTrustSet(nil, zap.NewNop())
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"viewer-pubkey": domain.RoleViewer,
	})

	intent := memberAddIntent(orgID, "new-member", domain.RoleViewer, "viewer-pubkey")
	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err == nil {
		t.Fatalf("expected authorization failure for viewer adding member")
	}
}

func TestSelfAuthorizingHandler_MemberAddByOrgOwnerAccepted(t *testing.T) {
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	trustSet := NewTrustSet(nil, zap.NewNop())
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"owner-pubkey": domain.RoleOwner,
	})

	intent := memberAddIntent(orgID, "new-member", domain.RoleViewer, "owner-pubkey")
	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err != nil {
		t.Fatalf("expected owner to be authorized for member add: %v", err)
	}
}

func TestSelfAuthorizingHandler_DefaultDenyForUnknownOp(t *testing.T) {
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	trustSet := NewTrustSet([]string{"fleet-op-1"}, zap.NewNop())
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"fleet-op-1": domain.RoleOwner,
	})

	// Use a valid org intent with an unknown schema to exercise the default path.
	intent := &Intent{
		Domain: "org", Op: "update", Schema: "bahia.intent.org.v1",
		OrgID: orgID, IntentID: uuid.New().String(), Coordinate: orgID.String(),
		Content: map[string]interface{}{"id": orgID.String()},
		Actor:   "unknown-pubkey-not-in-trustset",
	}

	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err == nil {
		t.Fatalf("expected authorization failure for unknown pubkey (default deny)")
	}
}

// --- encryption tests (item 1) ---

func TestOrgStateCrypto_EncryptDecryptRoundTrip(t *testing.T) {
	key := OrgStateKey{Ref: "test-key", Version: "v1", Key: make([]byte, 32)}
	for i := range key.Key {
		key.Key[i] = byte(i)
	}

	plaintext := []byte(`{"org_id":"abc","pubkey":"def","role":"admin"}`)
	encrypted, err := encryptOrgState(context.Background(), key, plaintext, "test-d", "test-topic")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Verify it's actually encrypted (not plaintext).
	if encrypted == string(plaintext) {
		t.Fatal("encrypted content is identical to plaintext")
	}

	decrypted, err := decryptOrgState(key, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestOrgStateCrypto_NoPlaintextPubkeyOrRoleInEncryptedContent(t *testing.T) {
	key := OrgStateKey{Ref: "test-key", Version: "v1", Key: make([]byte, 32)}
	for i := range key.Key {
		key.Key[i] = byte(i)
	}

	content := map[string]interface{}{
		"org_id":  "org-123",
		"pubkey":  "member-secret-pubkey-abcdef",
		"role":    "admin",
		"deleted": false,
	}
	contentJSON, _ := json.Marshal(content)

	encrypted, err := encryptOrgState(context.Background(), key, contentJSON, "test-d", "test-topic")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Assert no plaintext member pubkey or role appears in the encrypted content.
	if contains(encrypted, "member-secret-pubkey-abcdef") {
		t.Error("encrypted content contains plaintext member pubkey")
	}
	if contains(encrypted, `"admin"`) {
		t.Error("encrypted content contains plaintext role")
	}
	if contains(encrypted, `"role"`) {
		t.Error("encrypted content contains plaintext role key")
	}
}

func TestDecryptMemberContent_RoundTrip(t *testing.T) {
	key := OrgStateKey{Ref: "test-key", Version: "v1", Key: make([]byte, 32)}
	for i := range key.Key {
		key.Key[i] = byte(i)
	}
	encryptor := NewOrgStateEncryptor(StaticOrgStateKeyProvider{Key: key})

	content := map[string]interface{}{
		"org_id":  "org-uuid-123",
		"pubkey":  "member-pubkey-xyz",
		"role":    "admin",
		"deleted": false,
	}
	contentJSON, _ := json.Marshal(content)

	encrypted, err := encryptor.EncryptOrgState(context.Background(), contentJSON, "test-d", "test-topic")
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	orgID, pubkey, role, deleted, err := DecryptMemberContent(encryptor, encrypted)
	if err != nil {
		t.Fatalf("DecryptMemberContent failed: %v", err)
	}
	if orgID != "org-uuid-123" {
		t.Errorf("orgID = %q, want %q", orgID, "org-uuid-123")
	}
	if pubkey != "member-pubkey-xyz" {
		t.Errorf("pubkey = %q, want %q", pubkey, "member-pubkey-xyz")
	}
	if role != "admin" {
		t.Errorf("role = %q, want %q", role, "admin")
	}
	if deleted {
		t.Errorf("deleted = true, want false")
	}
}

// --- TrustSet relay trust source tests (item 3) ---

func TestTrustSet_RelaySourceWithoutPostgres(t *testing.T) {
	// TrustSet with NO Postgres configured. Relay members alone authorize.
	orgID := uuid.New()
	trustSet := NewTrustSet(nil, zap.NewNop()) // no fleetOps, no Postgres

	// Before relay members: no permission.
	if trustSet.HasPermission(context.Background(), orgID, "owner1", domain.PermManageMembers) {
		t.Fatal("should have no permission before relay members are set")
	}

	// Set relay members.
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"owner1": domain.RoleOwner,
		"admin1": domain.RoleAdmin,
	})

	// Owner can manage members.
	if !trustSet.HasPermission(context.Background(), orgID, "owner1", domain.PermManageMembers) {
		t.Error("owner1 should have PermManageMembers via relay source (no Postgres)")
	}

	// Admin can write services but not manage members.
	if !trustSet.HasPermission(context.Background(), orgID, "admin1", domain.PermWriteServices) {
		t.Error("admin1 should have PermWriteServices via relay source")
	}
	if trustSet.HasPermission(context.Background(), orgID, "admin1", domain.PermManageMembers) {
		t.Error("admin1 should NOT have PermManageMembers (admin role)")
	}

	// Unknown pubkey: no permission.
	if trustSet.HasPermission(context.Background(), orgID, "unknown", domain.PermWriteServices) {
		t.Error("unknown pubkey should have no permission")
	}
}

func TestTrustSet_PrecedenceRelayOverPostgres(t *testing.T) {
	orgID := uuid.New()
	trustSet := NewTrustSet(nil, zap.NewNop())

	// Set relay members: only owner1 has owner access.
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"owner1": domain.RoleOwner,
	})

	// owner1 should have PermManageMembers via relay.
	if !trustSet.HasPermission(context.Background(), orgID, "owner1", domain.PermManageMembers) {
		t.Errorf("relay owner1 should have PermManageMembers")
	}
	// owner2 should NOT have permission via relay (not in relay members).
	if trustSet.HasPermission(context.Background(), orgID, "owner2", domain.PermManageMembers) {
		t.Errorf("owner2 should NOT have permission via relay (not in relay members)")
	}
}

func TestTrustSet_RelaySourceAuthorizesIntent(t *testing.T) {
	// Verify end-to-end: TrustSet from relay membership events alone
	// authorizes an intent through the org handler's AuthorizeIntent.
	handler, _, _, _, _, _ := newTestOrgHandler(t)
	ctx := context.Background()

	orgID := uuid.New()
	trustSet := NewTrustSet(nil, zap.NewNop()) // no Postgres, no fleet ops
	trustSet.SetRelayMembers(orgID.String(), map[string]domain.Role{
		"org-owner": domain.RoleOwner,
	})

	// Owner should be able to add a member.
	intent := memberAddIntent(orgID, "new-member", domain.RoleViewer, "org-owner")
	err := handler.AuthorizeIntent(ctx, trustSet, intent)
	if err != nil {
		t.Fatalf("expected relay-only TrustSet to authorize member add: %v", err)
	}

	// Non-member should be rejected.
	intent2 := memberAddIntent(orgID, "another", domain.RoleViewer, "stranger")
	err = handler.AuthorizeIntent(ctx, trustSet, intent2)
	if err == nil {
		t.Fatal("expected stranger to be rejected by relay-only TrustSet")
	}
}

// --- gift-wrap ingress tests (item 2) ---

func TestIntentGiftWrapIngress_RejectPlaintextSensitiveDomain(t *testing.T) {
	ingress := NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{
		Processor:        &IntentProcessor{},
		SensitiveDomains: []string{"org", "secret", "notification"},
		Logger:           zap.NewNop(),
	})

	// org domain: sensitive → rejected.
	intent := &Intent{Domain: "org", IntentID: "test-1", Actor: "actor1"}
	if !ingress.RejectPlaintextSensitiveIntent(context.Background(), intent) {
		t.Error("expected plaintext org intent to be rejected")
	}

	// secret domain: sensitive → rejected.
	intent2 := &Intent{Domain: "secret", IntentID: "test-2", Actor: "actor1"}
	if !ingress.RejectPlaintextSensitiveIntent(context.Background(), intent2) {
		t.Error("expected plaintext secret intent to be rejected")
	}

	// service domain: not sensitive → allowed.
	intent3 := &Intent{Domain: "service", IntentID: "test-3", Actor: "actor1"}
	if ingress.RejectPlaintextSensitiveIntent(context.Background(), intent3) {
		t.Error("service domain should not be rejected")
	}
}

func TestIntentGiftWrapIngress_SensitiveDomainCoverage(t *testing.T) {
	ingress := NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{
		Processor:        &IntentProcessor{},
		SensitiveDomains: []string{"org", "secret", "notification"},
		Logger:           zap.NewNop(),
	})

	// Verify sensitive domains are rejected as plaintext.
	for _, domain := range []string{"org", "secret", "notification"} {
		intent := &Intent{Domain: domain, IntentID: "test-" + domain, Actor: "actor1"}
		if !ingress.RejectPlaintextSensitiveIntent(context.Background(), intent) {
			t.Errorf("expected domain %q to be sensitive (rejected as plaintext)", domain)
		}
	}

	// Non-sensitive domain should not be rejected.
	intent := &Intent{Domain: "service", IntentID: "test-service", Actor: "actor1"}
	if ingress.RejectPlaintextSensitiveIntent(context.Background(), intent) {
		t.Error("service domain should not be sensitive")
	}
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
