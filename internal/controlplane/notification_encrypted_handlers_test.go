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

func TestNotificationEncryptedHandlers_CreateListSanitizesWebhookSecret(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil, newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgID: domain.RoleOwner}))

	event := makeNotificationContextVMWrappedRequest(t, "create-1", EncryptedOperationNotificationChannelsCreate, map[string]any{
		"name":         "Prod webhook",
		"channel_type": "webhook",
		"config":       map[string]any{"url": "https://hooks.example/bahia", "secret": "super-secret"},
		"enabled":      true,
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 2 {
		t.Fatalf("published events = %d", len(publisher.events))
	}
	channelPayload := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["channel"].(map[string]any)
	config := channelPayload["config"].(map[string]any)
	if _, ok := config["secret"]; ok {
		t.Fatalf("ContextVM create result leaked webhook secret: %#v", config)
	}

	var stored domain.NotificationChannel
	for _, ch := range repo.channels {
		stored = ch
	}
	if stored.Config["secret"] != "super-secret" {
		t.Fatalf("stored secret was not preserved in storage: %#v", stored.Config)
	}
	if stored.OrgID != orgID {
		t.Fatalf("stored OrgID = %s, want authorized org %s", stored.OrgID, orgID)
	}

	publisher.events = nil
	listEvent := makeNotificationContextVMWrappedRequest(t, "list-1", EncryptedOperationNotificationChannelsList, nil)
	transport.HandleEvent(context.Background(), listEvent)
	channels := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["channels"].([]any)
	listedConfig := channels[0].(map[string]any)["config"].(map[string]any)
	if _, ok := listedConfig["secret"]; ok {
		t.Fatalf("ContextVM list result leaked webhook secret: %#v", listedConfig)
	}
}

func TestNotificationEncryptedHandlers_NotificationsNewAliasCreatesChannel(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil, newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgID: domain.RoleOwner}))

	event := makeNotificationContextVMWrappedRequest(t, "create-alias-1", "notifications/new", map[string]any{
		"name":         "Ops Webhook",
		"channel_type": "webhook",
		"config":       map[string]any{"url": "https://hooks.example/ops"},
	})
	transport.HandleEvent(context.Background(), event)

	if len(repo.channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(repo.channels))
	}
	channelPayload := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["channel"].(map[string]any)
	if channelPayload["name"] != "Ops Webhook" {
		t.Fatalf("unexpected alias response: %#v", channelPayload)
	}
}

func TestNotificationEncryptedHandlers_UpdatePreservesOmittedWebhookSecret(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{
		ID:          channelID,
		OrgID:       orgID,
		Name:        "Prod webhook",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "https://hooks.example/old", "secret": "super-secret"},
		Enabled:     true,
	}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil, newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgID: domain.RoleOwner}))
	event := makeNotificationContextVMWrappedRequest(t, "update-1", EncryptedOperationNotificationChannelsUpdate, map[string]any{
		"id":     channelID.String(),
		"config": map[string]any{"url": "https://hooks.example/new"},
	})

	transport.HandleEvent(context.Background(), event)

	stored := repo.channels[channelID]
	if stored.Config["url"] != "https://hooks.example/new" || stored.Config["secret"] != "super-secret" {
		t.Fatalf("update did not preserve omitted secret: %#v", stored.Config)
	}
	config := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["channel"].(map[string]any)["config"].(map[string]any)
	if _, ok := config["secret"]; ok {
		t.Fatalf("ContextVM update result leaked webhook secret: %#v", config)
	}
}

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
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
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
		{
			name: "get", operation: EncryptedOperationNotificationChannelsGet,
			payload: func(_, channelID uuid.UUID) map[string]any { return map[string]any{"id": channelID.String()} },
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.getChannel(ctx, request)
			},
		},
		{
			name: "create", operation: EncryptedOperationNotificationChannelsCreate,
			payload: func(orgID, _ uuid.UUID) map[string]any {
				return map[string]any{"org_id": orgID.String(), "name": "Foreign", "channel_type": "webhook"}
			},
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.createChannel(ctx, request)
			},
			assert: func(t *testing.T, repo *fakeNotificationRepo, _ uuid.UUID) {
				if len(repo.channels) != 2 {
					t.Fatalf("cross-tenant create persisted a channel: %d channels", len(repo.channels))
				}
			},
		},
		{
			name: "update", operation: EncryptedOperationNotificationChannelsUpdate,
			payload: func(_, channelID uuid.UUID) map[string]any {
				return map[string]any{"id": channelID.String(), "config": map[string]any{"url": "https://attacker.example/hook"}}
			},
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.updateChannel(ctx, request)
			},
			assert: func(t *testing.T, repo *fakeNotificationRepo, channelID uuid.UUID) {
				foreign := repo.channels[channelID]
				if foreign.Config["url"] != "https://victim.example/hook" || foreign.Config["secret"] != "victim-secret" {
					t.Fatalf("foreign webhook changed despite denial: %#v", foreign.Config)
				}
			},
		},
		{
			name: "delete", operation: EncryptedOperationNotificationChannelsDelete,
			payload: func(_, channelID uuid.UUID) map[string]any { return map[string]any{"id": channelID.String()} },
			call: func(h *notificationEncryptedHandler, ctx context.Context, request EncryptedRequest) (any, error) {
				return h.deleteChannel(ctx, request)
			},
			assert: func(t *testing.T, repo *fakeNotificationRepo, channelID uuid.UUID) {
				if _, ok := repo.channels[channelID]; !ok {
					t.Fatal("foreign webhook was deleted despite denial")
				}
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

func TestNotificationEncryptedHandlers_CreateResolvesAuthorizedOrg(t *testing.T) {
	t.Run("single membership needs no org_id", func(t *testing.T) {
		repo := newFakeNotificationRepo()
		orgID := uuid.New()
		h := &notificationEncryptedHandler{
			repo: repo,
			authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
				orgID: domain.RoleOwner,
			})},
		}

		_, err := h.createChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsCreate, map[string]any{
			"name": "Ops", "channel_type": "webhook",
		}))
		if err != nil {
			t.Fatalf("createChannel() error = %v", err)
		}
		if len(repo.channels) != 1 {
			t.Fatalf("created channels = %d, want 1", len(repo.channels))
		}
		for _, ch := range repo.channels {
			if ch.OrgID != orgID {
				t.Fatalf("created channel OrgID = %s, want authorized org %s", ch.OrgID, orgID)
			}
		}
	})

	t.Run("multiple memberships require explicit org_id", func(t *testing.T) {
		repo := newFakeNotificationRepo()
		orgA := uuid.New()
		orgB := uuid.New()
		h := &notificationEncryptedHandler{
			repo: repo,
			authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
				orgA: domain.RoleOwner,
				orgB: domain.RoleOwner,
			})},
		}
		request := makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsCreate, map[string]any{
			"name": "Ops", "channel_type": "webhook",
		})
		_, err := h.createChannel(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "org_id is required when requester belongs to multiple organizations") {
			t.Fatalf("createChannel() error = %v, want ambiguous-org error", err)
		}

		request = makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsCreate, map[string]any{
			"org_id": orgB.String(), "name": "Ops", "channel_type": "webhook",
		})
		_, err = h.createChannel(context.Background(), request)
		if err != nil {
			t.Fatalf("createChannel(org B) error = %v", err)
		}
		if len(repo.channels) != 1 {
			t.Fatalf("created channels = %d, want 1", len(repo.channels))
		}
		for _, ch := range repo.channels {
			if ch.OrgID != orgB {
				t.Fatalf("created channel OrgID = %s, want selected authorized org %s", ch.OrgID, orgB)
			}
		}
	})
}

func TestNotificationEncryptedHandlers_ForgedOrgIDIsRejectedByMembershipSet(t *testing.T) {
	t.Run("list rejects org outside sole membership", func(t *testing.T) {
		repo := newFakeNotificationRepo()
		orgA := uuid.New()
		orgB := uuid.New()
		foreignChannelID := uuid.New()
		repo.channels[foreignChannelID] = domain.NotificationChannel{ID: foreignChannelID, OrgID: orgB, Name: "Foreign", Enabled: true}
		rbac, lookup := newTrackedNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgA: domain.RoleOwner})
		h := &notificationEncryptedHandler{repo: repo, authorizer: encryptedTenantAuthorizer{rbac: rbac}}

		_, err := h.listChannels(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsList, map[string]any{"org_id": orgB.String()}))
		if err == nil || !auth.IsAccessDenied(err) {
			t.Fatalf("listChannels(forged org_id) error = %v, want access denied", err)
		}
		if calls := notificationMemberGetCallCount(lookup, orgB); calls != 0 {
			t.Fatalf("forged list org_id reached CheckPermission %d times, want membership-set rejection", calls)
		}
	})

	t.Run("create rejects org outside sole membership", func(t *testing.T) {
		repo := newFakeNotificationRepo()
		orgA := uuid.New()
		orgB := uuid.New()
		rbac, lookup := newTrackedNotificationTestRBAC(t, map[uuid.UUID]domain.Role{orgA: domain.RoleOwner})
		h := &notificationEncryptedHandler{repo: repo, authorizer: encryptedTenantAuthorizer{rbac: rbac}}

		_, err := h.createChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsCreate, map[string]any{
			"org_id": orgB.String(), "name": "Forged", "channel_type": "webhook",
		}))
		if err == nil || !auth.IsAccessDenied(err) {
			t.Fatalf("createChannel(forged org_id) error = %v, want access denied", err)
		}
		if calls := notificationMemberGetCallCount(lookup, orgB); calls != 0 {
			t.Fatalf("forged create org_id reached CheckPermission %d times, want membership-set rejection", calls)
		}
		if len(repo.channels) != 0 {
			t.Fatalf("forged create persisted %d channels, want none", len(repo.channels))
		}
	})

	t.Run("create rejects org outside ambiguous memberships", func(t *testing.T) {
		repo := newFakeNotificationRepo()
		orgA := uuid.New()
		orgB := uuid.New()
		orgC := uuid.New()
		rbac, lookup := newTrackedNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
			orgA: domain.RoleOwner,
			orgB: domain.RoleOwner,
		})
		h := &notificationEncryptedHandler{repo: repo, authorizer: encryptedTenantAuthorizer{rbac: rbac}}

		_, err := h.createChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsCreate, map[string]any{
			"org_id": orgC.String(), "name": "Forged", "channel_type": "webhook",
		}))
		if err == nil || !auth.IsAccessDenied(err) {
			t.Fatalf("createChannel(ambiguous forged org_id) error = %v, want access denied", err)
		}
		if calls := notificationMemberGetCallCount(lookup, orgC); calls != 0 {
			t.Fatalf("forged ambiguous org_id reached CheckPermission %d times, want membership-set rejection", calls)
		}
		if len(repo.channels) != 0 {
			t.Fatalf("ambiguous forged create persisted %d channels, want none", len(repo.channels))
		}
	})
}

func TestNotificationEncryptedHandlers_AuthorizedOperationsUseOrgScopedRepository(t *testing.T) {
	orgID := uuid.New()
	newHandler := func() (*notificationEncryptedHandler, *fakeNotificationRepo, uuid.UUID) {
		repo := newFakeNotificationRepo()
		channelID := uuid.New()
		repo.channels[channelID] = domain.NotificationChannel{
			ID: channelID, OrgID: orgID, Name: "Ops", ChannelType: domain.ChannelTypeWebhook,
			Config: map[string]any{"url": "https://ops.example/hook", "secret": "signing-secret"}, Enabled: true,
		}
		repo.logs = []domain.NotificationLog{{ID: uuid.New(), ChannelID: channelID, CreatedAt: time.Now().UTC()}}
		return &notificationEncryptedHandler{
			repo: repo,
			authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
				orgID: domain.RoleOwner,
			})},
		}, repo, channelID
	}

	t.Run("get", func(t *testing.T) {
		h, repo, channelID := newHandler()
		_, err := h.getChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsGet, map[string]any{"id": channelID.String()}))
		if err != nil {
			t.Fatalf("getChannel() error = %v", err)
		}
		if len(repo.scopedGetOrgs) != 1 || repo.scopedGetOrgs[0] != orgID {
			t.Fatalf("scoped get orgs = %v, want %s", repo.scopedGetOrgs, orgID)
		}
	})

	t.Run("update", func(t *testing.T) {
		h, repo, channelID := newHandler()
		_, err := h.updateChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsUpdate, map[string]any{
			"id": channelID.String(), "org_id": uuid.New().String(), "name": "Changed",
		}))
		if err != nil {
			t.Fatalf("updateChannel() error = %v", err)
		}
		if len(repo.scopedUpdateOrgs) != 1 || repo.scopedUpdateOrgs[0] != orgID {
			t.Fatalf("scoped update orgs = %v, want %s", repo.scopedUpdateOrgs, orgID)
		}
		if got := repo.channels[channelID].OrgID; got != orgID {
			t.Fatalf("update accepted client org_id: stored org = %s, want %s", got, orgID)
		}
	})

	t.Run("delete", func(t *testing.T) {
		h, repo, channelID := newHandler()
		_, err := h.deleteChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsDelete, map[string]any{"id": channelID.String()}))
		if err != nil {
			t.Fatalf("deleteChannel() error = %v", err)
		}
		if len(repo.scopedDeleteOrgs) != 1 || repo.scopedDeleteOrgs[0] != orgID {
			t.Fatalf("scoped delete orgs = %v, want %s", repo.scopedDeleteOrgs, orgID)
		}
	})

	t.Run("test", func(t *testing.T) {
		h, repo, channelID := newHandler()
		_, err := h.testChannel(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationChannelsTest, map[string]any{"id": channelID.String()}))
		if err == nil || err.Error() != "notification dispatcher is not configured" {
			t.Fatalf("testChannel() error = %v, want dispatcher error after authorization", err)
		}
		if len(repo.scopedGetOrgs) != 1 || repo.scopedGetOrgs[0] != orgID {
			t.Fatalf("scoped test-channel get orgs = %v, want %s", repo.scopedGetOrgs, orgID)
		}
	})

	t.Run("channel logs", func(t *testing.T) {
		h, repo, channelID := newHandler()
		_, err := h.listLogs(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationLogsList, map[string]any{"channel_id": channelID.String()}))
		if err != nil {
			t.Fatalf("listLogs(channel) error = %v", err)
		}
		if len(repo.scopedGetOrgs) != 1 || repo.scopedGetOrgs[0] != orgID {
			t.Fatalf("scoped log channel get orgs = %v, want %s", repo.scopedGetOrgs, orgID)
		}
	})

	t.Run("recent logs", func(t *testing.T) {
		h, repo, _ := newHandler()
		_, err := h.listLogs(context.Background(), makeNotificationEncryptedRequest(t, EncryptedOperationNotificationLogsList, nil))
		if err != nil {
			t.Fatalf("listLogs() error = %v", err)
		}
		if len(repo.scopedLogListOrgs) != 1 || repo.scopedLogListOrgs[0] != orgID {
			t.Fatalf("scoped recent-log orgs = %v, want %s", repo.scopedLogListOrgs, orgID)
		}
	})
}

func TestNotificationEncryptedHandlers_PermissionMapping(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{
		ID: channelID, OrgID: orgID, Name: "Ops", ChannelType: domain.ChannelTypeWebhook,
		Config: map[string]any{"url": "https://ops.example/hook"}, Enabled: true,
	}
	repo.logs = []domain.NotificationLog{{ID: uuid.New(), ChannelID: channelID, CreatedAt: time.Now().UTC()}}
	h := &notificationEncryptedHandler{
		repo: repo,
		authorizer: encryptedTenantAuthorizer{rbac: newNotificationTestRBAC(t, map[uuid.UUID]domain.Role{
			orgID: domain.RoleViewer,
		})},
	}

	reads := []struct {
		name      string
		operation string
		payload   map[string]any
		call      func(context.Context, EncryptedRequest) (any, error)
	}{
		{name: "list", operation: EncryptedOperationNotificationChannelsList, call: h.listChannels},
		{name: "get", operation: EncryptedOperationNotificationChannelsGet, payload: map[string]any{"id": channelID.String()}, call: h.getChannel},
		{name: "logs", operation: EncryptedOperationNotificationLogsList, payload: map[string]any{"channel_id": channelID.String()}, call: h.listLogs},
	}
	for _, tt := range reads {
		t.Run(tt.name+" allows read permission", func(t *testing.T) {
			if _, err := tt.call(context.Background(), makeNotificationEncryptedRequest(t, tt.operation, tt.payload)); err != nil {
				t.Fatalf("%s error = %v, want viewer read access", tt.name, err)
			}
		})
	}

	mutations := []struct {
		name      string
		operation string
		payload   map[string]any
		call      func(context.Context, EncryptedRequest) (any, error)
	}{
		{name: "create", operation: EncryptedOperationNotificationChannelsCreate, payload: map[string]any{"name": "New", "channel_type": "webhook"}, call: h.createChannel},
		{name: "update", operation: EncryptedOperationNotificationChannelsUpdate, payload: map[string]any{"id": channelID.String(), "name": "Changed"}, call: h.updateChannel},
		{name: "delete", operation: EncryptedOperationNotificationChannelsDelete, payload: map[string]any{"id": channelID.String()}, call: h.deleteChannel},
		{name: "test", operation: EncryptedOperationNotificationChannelsTest, payload: map[string]any{"id": channelID.String()}, call: h.testChannel},
	}
	for _, tt := range mutations {
		t.Run(tt.name+" requires manage settings", func(t *testing.T) {
			_, err := tt.call(context.Background(), makeNotificationEncryptedRequest(t, tt.operation, tt.payload))
			if err == nil || !auth.IsAccessDenied(err) || !strings.Contains(err.Error(), string(domain.PermManageSettings)) {
				t.Fatalf("%s error = %v, want %s denial", tt.name, err, domain.PermManageSettings)
			}
		})
	}
}

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

func TestNotificationEncryptedHandlers_FailClosedWithoutRBAC(t *testing.T) {
	repo := newFakeNotificationRepo()
	orgID := uuid.New()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{ID: channelID, OrgID: orgID, Name: "Ops", Enabled: true}
	h := &notificationEncryptedHandler{repo: repo}
	tests := []struct {
		name      string
		operation string
		payload   map[string]any
		call      func(context.Context, EncryptedRequest) (any, error)
	}{
		{name: "list", operation: EncryptedOperationNotificationChannelsList, call: h.listChannels},
		{name: "get", operation: EncryptedOperationNotificationChannelsGet, payload: map[string]any{"id": channelID.String()}, call: h.getChannel},
		{name: "create", operation: EncryptedOperationNotificationChannelsCreate, payload: map[string]any{"name": "New", "channel_type": "webhook"}, call: h.createChannel},
		{name: "update", operation: EncryptedOperationNotificationChannelsUpdate, payload: map[string]any{"id": channelID.String(), "name": "Changed"}, call: h.updateChannel},
		{name: "delete", operation: EncryptedOperationNotificationChannelsDelete, payload: map[string]any{"id": channelID.String()}, call: h.deleteChannel},
		{name: "test", operation: EncryptedOperationNotificationChannelsTest, payload: map[string]any{"id": channelID.String()}, call: h.testChannel},
		{name: "logs", operation: EncryptedOperationNotificationLogsList, payload: map[string]any{"channel_id": channelID.String()}, call: h.listLogs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.call(context.Background(), makeNotificationEncryptedRequest(t, tt.operation, tt.payload))
			if err == nil || !strings.Contains(err.Error(), "tenant RBAC is not configured") {
				t.Fatalf("%s error = %v, want fail-closed RBAC error", tt.name, err)
			}
		})
	}
}

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
