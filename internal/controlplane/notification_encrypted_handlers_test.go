package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type fakeNotificationRepo struct {
	channels          map[uuid.UUID]domain.NotificationChannel
	logs              []domain.NotificationLog
	scopedGetOrgs     []uuid.UUID
	scopedListOrgs    []uuid.UUID
	scopedUpdateOrgs  []uuid.UUID
	scopedDeleteOrgs  []uuid.UUID
	scopedLogListOrgs []uuid.UUID
}

func newFakeNotificationRepo() *fakeNotificationRepo {
	return &fakeNotificationRepo{channels: make(map[uuid.UUID]domain.NotificationChannel)}
}

func (r *fakeNotificationRepo) CreateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	if ch.ID == uuid.Nil {
		ch.ID = uuid.New()
	}
	now := time.Now().UTC()
	ch.CreatedAt = now
	ch.UpdatedAt = now
	r.channels[ch.ID] = *ch
	return nil
}

func (r *fakeNotificationRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	ch, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	return &ch, nil
}

func (r *fakeNotificationRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	out := make([]domain.NotificationChannel, 0, len(r.channels))
	for _, ch := range r.channels {
		if enabledOnly && !ch.Enabled {
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}

func (r *fakeNotificationRepo) UpdateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	ch.UpdatedAt = time.Now().UTC()
	r.channels[ch.ID] = *ch
	return nil
}

func (r *fakeNotificationRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	delete(r.channels, id)
	return nil
}

func (r *fakeNotificationRepo) GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error) {
	r.scopedGetOrgs = append(r.scopedGetOrgs, orgID)
	ch, err := r.GetChannelByID(ctx, id)
	if err != nil || ch == nil || ch.OrgID != orgID {
		return nil, err
	}
	return ch, nil
}

func (r *fakeNotificationRepo) ListChannelsByOrg(_ context.Context, orgID uuid.UUID, enabledOnly bool) ([]domain.NotificationChannel, error) {
	r.scopedListOrgs = append(r.scopedListOrgs, orgID)
	out := make([]domain.NotificationChannel, 0)
	for _, ch := range r.channels {
		if ch.OrgID != orgID || enabledOnly && !ch.Enabled {
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}

func (r *fakeNotificationRepo) UpdateChannelForOrg(ctx context.Context, ch *domain.NotificationChannel, orgID uuid.UUID) error {
	r.scopedUpdateOrgs = append(r.scopedUpdateOrgs, orgID)
	existing, err := r.GetChannelByIDForOrg(ctx, ch.ID, orgID)
	if err != nil {
		return err
	}
	if existing == nil {
		return repository.ErrNotFound
	}
	return r.UpdateChannel(ctx, ch)
}

func (r *fakeNotificationRepo) DeleteChannelForOrg(ctx context.Context, id, orgID uuid.UUID) error {
	r.scopedDeleteOrgs = append(r.scopedDeleteOrgs, orgID)
	existing, err := r.GetChannelByIDForOrg(ctx, id, orgID)
	if err != nil {
		return err
	}
	if existing == nil {
		return repository.ErrNotFound
	}
	return r.DeleteChannel(ctx, id)
}

func (r *fakeNotificationRepo) CreateLog(_ context.Context, log *domain.NotificationLog) error {
	if log.ID == uuid.Nil {
		log.ID = uuid.New()
	}
	r.logs = append(r.logs, *log)
	return nil
}

func (r *fakeNotificationRepo) UpdateLog(_ context.Context, log *domain.NotificationLog) error {
	for i := range r.logs {
		if r.logs[i].ID == log.ID {
			r.logs[i] = *log
			return nil
		}
	}
	r.logs = append(r.logs, *log)
	return nil
}

func (r *fakeNotificationRepo) ListLogsByChannel(_ context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	out := []domain.NotificationLog{}
	for _, log := range r.logs {
		if log.ChannelID == channelID {
			out = append(out, log)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeNotificationRepo) ListRecentLogs(_ context.Context, limit int) ([]domain.NotificationLog, error) {
	out := append([]domain.NotificationLog(nil), r.logs...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeNotificationRepo) ListRecentLogsByOrg(_ context.Context, orgID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	r.scopedLogListOrgs = append(r.scopedLogListOrgs, orgID)
	out := make([]domain.NotificationLog, 0)
	for _, log := range r.logs {
		ch, ok := r.channels[log.ChannelID]
		if !ok || ch.OrgID != orgID {
			continue
		}
		out = append(out, log)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeNotificationRepo) ListRetryable(context.Context, int) ([]domain.NotificationLog, error) {
	return nil, nil
}

func encryptedPayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func makeNotificationContextVMRequest(t *testing.T, id, operation string, payload any) *nostr.Event {
	t.Helper()
	params := json.RawMessage(`null`)
	if payload != nil {
		params = encryptedPayload(t, payload)
	}
	request := ContextVMJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      encryptedPayload(t, id),
		Method:  operation,
		Params:  params,
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal ContextVM request: %v", err)
	}
	return makeContextVMEvent(t, testRequesterKey, string(data))
}

func makeNotificationContextVMWrappedRequest(t *testing.T, id, operation string, payload any) *nostr.Event {
	t.Helper()
	return wrapContextVMEvent(t, makeNotificationContextVMRequest(t, id, operation, payload), KindContextVMGiftWrap)
}

type notificationMemberLookup struct {
	members  []domain.OrgMember
	getCalls []uuid.UUID
}

func (m *notificationMemberLookup) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	m.getCalls = append(m.getCalls, orgID)
	for i := range m.members {
		member := m.members[i]
		if member.OrgID == orgID && member.Pubkey == pubkey {
			return &member, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *notificationMemberLookup) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	out := make([]domain.OrgMember, 0, len(m.members))
	for _, member := range m.members {
		if member.Pubkey == pubkey {
			out = append(out, member)
		}
	}
	return out, nil
}

func newNotificationTestRBAC(t *testing.T, orgRoles map[uuid.UUID]domain.Role) *auth.RBAC {
	t.Helper()
	pubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	members := make([]domain.OrgMember, 0, len(orgRoles))
	for orgID, role := range orgRoles {
		members = append(members, domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: role})
	}
	return auth.NewRBAC(&notificationMemberLookup{members: members})
}

func newTrackedNotificationTestRBAC(t *testing.T, orgRoles map[uuid.UUID]domain.Role) (*auth.RBAC, *notificationMemberLookup) {
	t.Helper()
	pubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	members := make([]domain.OrgMember, 0, len(orgRoles))
	for orgID, role := range orgRoles {
		members = append(members, domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: role})
	}
	lookup := &notificationMemberLookup{members: members}
	return auth.NewRBAC(lookup), lookup
}

func notificationMemberGetCallCount(lookup *notificationMemberLookup, orgID uuid.UUID) int {
	count := 0
	for _, calledOrgID := range lookup.getCalls {
		if calledOrgID == orgID {
			count++
		}
	}
	return count
}

func makeNotificationEncryptedRequest(t *testing.T, operation string, payload any) EncryptedRequest {
	t.Helper()
	return EncryptedRequest{
		Event: makeNotificationContextVMRequest(t, "direct-1", operation, payload),
		Envelope: EncryptedRequestEnvelope{
			Operation: operation,
			Payload:   encryptedPayload(t, payload),
		},
	}
}

func notificationResultPayload(t *testing.T, ev nostr.Event) map[string]any {
	t.Helper()
	if ev.Kind == KindContextVMGiftWrap || ev.Kind == KindContextVMEphemeralWrap {
		ev = unwrapContextVMResponseEvent(t, ev, testRequesterKey)
	}
	response := contextVMResponse(t, ev)
	if response.Error != nil {
		t.Fatalf("ContextVM response error: %+v", response.Error)
	}
	payload, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T: %#v", response.Result, response.Result)
	}
	return payload
}

// TestNotificationEncryptedHandlers_CreateListSanitizesWebhookSecret: deleted in Phase 3 N1 (tested deleted mutation handlers).
// TestNotificationEncryptedHandlers_NotificationsNewAliasCreatesChannel: deleted in Phase 3 N1 (tested deleted mutation handlers).
// TestNotificationEncryptedHandlers_UpdatePreservesOmittedWebhookSecret: deleted in Phase 3 N1 (tested deleted mutation handlers).
func TestNotificationEncryptedHandlers_ListLogsReturnsEncryptedContextVMResponse(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{ID: channelID, OrgID: orgID, Name: "Ops", Enabled: true}
	repo.logs = []domain.NotificationLog{{
		ID:        uuid.New(),
		ChannelID: channelID,
		EventType: "deployment.failed",
		Payload:   map[string]any{"secret_detail": "only-in-encrypted-result"},
		Status:    domain.NotificationStatusSent,
		Attempts:  1,
	}}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), contextVMTestAuthorizedPubkeys(t), zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil, newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgID: domain.RoleViewer}))
	event := makeNotificationContextVMWrappedRequest(t, "logs-1", EncryptedOperationNotificationLogsList, map[string]any{"limit": 50})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d", len(publisher.events))
	}
	if got := publisher.events[len(publisher.events)-1]; got.Kind != KindContextVMGiftWrap || got.Content == "" || got.Content == "only-in-encrypted-result" {
		t.Fatalf("log result was not published as encrypted ContextVM response: %#v", got)
	}
	logs := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["logs"].([]any)
	if logs[0].(map[string]any)["payload"].(map[string]any)["secret_detail"] != "only-in-encrypted-result" {
		t.Fatalf("missing decrypted log payload: %#v", logs[0])
	}
}

func TestNotificationEncryptedHandlers_CrossTenantAccessIsDenied(t *testing.T) {
	newFixture := func() (*notificationEncryptedHandler, *fakeNotificationRepo, uuid.UUID, uuid.UUID) {
		repo := newFakeNotificationRepo()
		requesterOrgID := uuid.New()
		foreignOrgID := uuid.New()
		requesterChannelID := uuid.New()
		foreignChannelID := uuid.New()
		repo.channels[requesterChannelID] = domain.NotificationChannel{
			ID: requesterChannelID, OrgID: requesterOrgID, Name: "Requester", ChannelType: domain.ChannelTypeWebhook,
			Config: map[string]any{"url": "https://requester.example/hook"}, Enabled: true,
		}
		repo.channels[foreignChannelID] = domain.NotificationChannel{
			ID: foreignChannelID, OrgID: foreignOrgID, Name: "Victim", ChannelType: domain.ChannelTypeWebhook,
			Config: map[string]any{"url": "https://victim.example/hook", "secret": "victim-secret"}, Enabled: true,
		}
		repo.logs = []domain.NotificationLog{{
			ID: uuid.New(), ChannelID: foreignChannelID, EventType: "deployment.failed",
			Payload: map[string]any{"detail": "victim-only"}, Status: domain.NotificationStatusSent,
		}}
		h := &notificationEncryptedHandler{
			repo: repo,
			authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
				requesterOrgID: domain.RoleOwner,
			})},
		}
		return h, repo, requesterChannelID, foreignChannelID
	}

	t.Run("list excludes foreign channels", func(t *testing.T) {
		h, _, requesterChannelID, _ := newFixture()
		result, err := h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, nil))
		if err != nil {
			t.Fatalf("listChannels() error = %v", err)
		}
		channels := result.(map[string]any)["channels"].([]domain.NotificationChannel)
		if len(channels) != 1 || channels[0].ID != requesterChannelID {
			t.Fatalf("listChannels() returned cross-tenant channels: %#v", channels)
		}
	})

	tests := []struct {
		name      string
		operation string
		payload   func(uuid.UUID, uuid.UUID) map[string]any
		call      func(*notificationEncryptedHandler, context.Context, EncryptedRequest) (any, error)
		assert    func(*testing.T, *fakeNotificationRepo, uuid.UUID)
	}{
		// Phase 3 N1: create/update/delete entries removed — those mutations
		// now go through intent publishing. Only read + test + logs remain.
		{
			name: "get", operation: EncryptedOperationNotificationChannelsGet,
			payload: func(_, channelID uuid.UUID) map[string]any { return map[string]any{"id": channelID.String()} },
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.getChannel(ctx, request)
			},
		},
		{
			name: "test", operation: EncryptedOperationNotificationChannelsTest,
			payload: func(_, channelID uuid.UUID) map[string]any { return map[string]any{"id": channelID.String()} },
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.testChannel(ctx, request)
			},
		},
		{
			name: "logs", operation: EncryptedOperationNotificationLogsList,
			payload: func(_, channelID uuid.UUID) map[string]any { return map[string]any{"channel_id": channelID.String()} },
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.listLogs(ctx, request)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, repo, _, foreignChannelID := newFixture()
			foreignOrgID := repo.channels[foreignChannelID].OrgID
			payload := tt.payload(foreignOrgID, foreignChannelID)
			_, err := tt.call(h, context.Background(), makeNotificationEncryptedRequest(t, tt.operation, payload))
			if err == nil || !auth.IsAccessDenied(err) {
				t.Fatalf("%s cross-tenant error = %v, want access denied", tt.name, err)
			}
			if tt.assert != nil {
				tt.assert(t, repo, foreignChannelID)
			}
		})
	}
}

func TestNotificationEncryptedHandlers_ListUsesRequesterMembershipUnion(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgA := uuid.New()
	orgB := uuid.New()
	orgC := uuid.New()
	channelA := domain.NotificationChannel{ID: uuid.New(), OrgID: orgA, Name: "A", Enabled: true}
	channelB := domain.NotificationChannel{ID: uuid.New(), OrgID: orgB, Name: "B", Enabled: true}
	channelC := domain.NotificationChannel{ID: uuid.New(), OrgID: orgC, Name: "C", Enabled: true}
	repo.channels[channelA.ID] = channelA
	repo.channels[channelB.ID] = channelB
	repo.channels[channelC.ID] = channelC
	h := &notificationEncryptedHandler{
		repo: repo,
		authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
			orgA: domain.RoleViewer,
			orgC: domain.RoleViewer,
		})},
	}

	result, err := h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, nil))
	if err != nil {
		t.Fatalf("listChannels() error = %v", err)
	}
	channels := result.(map[string]any)["channels"].([]domain.NotificationChannel)
	if len(channels) != 2 || channels[0].ID != channelA.ID || channels[1].ID != channelC.ID {
		t.Fatalf("membership union channels = %#v, want org A and C only", channels)
	}

	result, err = h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, map[string]any{"org_id": orgC.String()}))
	if err != nil {
		t.Fatalf("listChannels(org C) error = %v", err)
	}
	channels = result.(map[string]any)["channels"].([]domain.NotificationChannel)
	if len(channels) != 1 || channels[0].ID != channelC.ID {
		t.Fatalf("filtered channels = %#v, want org C only", channels)
	}

	_, err = h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, map[string]any{"org_id": orgB.String()}))
	if err == nil || !auth.IsAccessDenied(err) {
		t.Fatalf("listChannels(foreign org) error = %v, want access denied", err)
	}
}

// TestNotificationEncryptedHandlers_CreateResolvesAuthorizedOrg: deleted in Phase 3 N1 (tested deleted mutation handlers).
// TestNotificationEncryptedHandlers_ForgedOrgIDIsRejectedByMembershipSet: deleted in Phase 3 N1 (tested deleted mutation handlers).
// TestNotificationEncryptedHandlers_AuthorizedOperationsUseOrgScopedRepository: deleted in Phase 3 N1 (tested deleted mutation handlers).
// TestNotificationEncryptedHandlers_PermissionMapping: deleted in Phase 3 N1 (tested deleted mutation handlers).
func TestNotificationEncryptedHandlers_RejectsUnownedLegacyChannel(t *testing.T) {
	repo := newFakeNotificationRepo()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{ID: channelID, Name: "Legacy"}
	h := &notificationEncryptedHandler{
		repo: repo,
		authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
			uuid.New(): domain.RoleOwner,
		})},
	}

	_, err := h.getChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsGet, map[string]any{"id": channelID.String()}))
	if err == nil || err.Error() != "notification channel is not assigned to an organization" {
		t.Fatalf("getChannel() error = %v, want unowned-channel denial", err)
	}
}

// TestNotificationEncryptedHandlers_FailClosedWithoutRBAC: deleted in Phase 3 N1 (tested deleted mutation handlers).
func TestNotificationEncryptedHandlers_FailClosedWithRBACMissingMembersLookup(t *testing.T) {
	repo := newFakeNotificationRepo()
	h := &notificationEncryptedHandler{
		repo:       repo,
		authorizer: encryptedTenantAuthorizer{rbac: auth.NewRBAC(nil)},
	}

	_, err := h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, nil))
	if err == nil || !strings.Contains(err.Error(), "authorization not configured: members lookup is unavailable") {
		t.Fatalf("listChannels() error = %v, want fail-closed missing-members error", err)
	}
}
