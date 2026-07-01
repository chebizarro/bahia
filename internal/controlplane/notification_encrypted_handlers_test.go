package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type fakeNotificationRepo struct {
	channels map[uuid.UUID]domain.NotificationChannel
	logs     []domain.NotificationLog
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
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil)

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

	publisher.events = nil
	listEvent := makeNotificationContextVMWrappedRequest(t, "list-1", EncryptedOperationNotificationChannelsList, nil)
	transport.HandleEvent(context.Background(), listEvent)
	channels := notificationResultPayload(t, publisher.events[len(publisher.events)-1])["channels"].([]any)
	listedConfig := channels[0].(map[string]any)["config"].(map[string]any)
	if _, ok := listedConfig["secret"]; ok {
		t.Fatalf("ContextVM list result leaked webhook secret: %#v", listedConfig)
	}
}

func TestNotificationEncryptedHandlers_UpdatePreservesOmittedWebhookSecret(t *testing.T) {
	repo := newFakeNotificationRepo()
	channelID := uuid.New()
	repo.channels[channelID] = domain.NotificationChannel{
		ID:          channelID,
		Name:        "Prod webhook",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "https://hooks.example/old", "secret": "super-secret"},
		Enabled:     true,
	}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), nil, zap.NewNop())
	RegisterNotificationEncryptedHandlers(transport, repo, nil)
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
	channelID := uuid.New()
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
	RegisterNotificationEncryptedHandlers(transport, repo, nil)
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
