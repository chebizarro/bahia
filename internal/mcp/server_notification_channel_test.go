package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"go.uber.org/zap"
)

type testNotificationRepo struct {
	channels map[uuid.UUID]*domain.NotificationChannel
	logs     map[uuid.UUID]*domain.NotificationLog
}

func newTestNotificationRepo() *testNotificationRepo {
	return &testNotificationRepo{
		channels: make(map[uuid.UUID]*domain.NotificationChannel),
		logs:     make(map[uuid.UUID]*domain.NotificationLog),
	}
}

func (r *testNotificationRepo) CreateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	if ch.ID == uuid.Nil {
		ch.ID = uuid.New()
	}
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now().UTC()
	}
	if ch.UpdatedAt.IsZero() {
		ch.UpdatedAt = ch.CreatedAt
	}
	copy := *ch
	r.channels[ch.ID] = &copy
	return nil
}

func (r *testNotificationRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	ch, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	copy := *ch
	return &copy, nil
}

func (r *testNotificationRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	out := make([]domain.NotificationChannel, 0, len(r.channels))
	for _, ch := range r.channels {
		if enabledOnly && !ch.Enabled {
			continue
		}
		out = append(out, *ch)
	}
	return out, nil
}

func (r *testNotificationRepo) UpdateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	if _, ok := r.channels[ch.ID]; !ok {
		return nil
	}
	copy := *ch
	r.channels[ch.ID] = &copy
	return nil
}

func (r *testNotificationRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	delete(r.channels, id)
	return nil
}

func (r *testNotificationRepo) CreateLog(_ context.Context, log *domain.NotificationLog) error {
	if log.ID == uuid.Nil {
		log.ID = uuid.New()
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}
	if log.UpdatedAt.IsZero() {
		log.UpdatedAt = log.CreatedAt
	}
	copy := *log
	r.logs[log.ID] = &copy
	return nil
}

func (r *testNotificationRepo) UpdateLog(_ context.Context, log *domain.NotificationLog) error {
	copy := *log
	r.logs[log.ID] = &copy
	return nil
}

func (r *testNotificationRepo) ListLogsByChannel(_ context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	out := make([]domain.NotificationLog, 0)
	for _, log := range r.logs {
		if log.ChannelID == channelID {
			out = append(out, *log)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (r *testNotificationRepo) ListRecentLogs(_ context.Context, limit int) ([]domain.NotificationLog, error) {
	out := make([]domain.NotificationLog, 0, len(r.logs))
	for _, log := range r.logs {
		out = append(out, *log)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *testNotificationRepo) ListRetryable(_ context.Context, maxAttempts int) ([]domain.NotificationLog, error) {
	out := make([]domain.NotificationLog, 0)
	for _, log := range r.logs {
		if (log.Status == domain.NotificationStatusPending || log.Status == domain.NotificationStatusRetrying) && log.Attempts < maxAttempts {
			out = append(out, *log)
		}
	}
	return out, nil
}

type testNotificationSender struct {
	sent []string
}

func (s *testNotificationSender) Send(_ context.Context, ch *domain.NotificationChannel, eventType string, _ map[string]any) error {
	s.sent = append(s.sent, fmt.Sprintf("%s:%s", ch.Name, eventType))
	return nil
}

func newTestMCPNotificationServer() (*Server, *testNotificationRepo, *testNotificationSender) {
	repo := newTestNotificationRepo()
	dispatcher := notifications.NewDispatcher(repo, zap.NewNop())
	sender := &testNotificationSender{}
	dispatcher.RegisterSender(domain.ChannelTypeWebhook, sender)
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{
		NotificationRepo:       repo,
		NotificationDispatcher: dispatcher,
	})
	return server, repo, sender
}

func TestGetTools_IncludesNotificationChannelCRUDAndTest(t *testing.T) {
	server, _, _ := newTestMCPNotificationServer()
	required := map[string]bool{
		"bahia_list_notification_channels":  false,
		"bahia_get_notification_channel":    false,
		"bahia_create_notification_channel": false,
		"bahia_update_notification_channel": false,
		"bahia_delete_notification_channel": false,
		"bahia_test_notification_channel":   false,
		"bahia_list_notifications":          false,
		"bahia_mark_notification_read":      false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_NotificationChannelCRUDAndTest(t *testing.T) {
	ctx := context.Background()
	server, repo, sender := newTestMCPNotificationServer()

	createRes, err := server.CallTool(ctx, "bahia_create_notification_channel", map[string]interface{}{
		"name":         "deployments",
		"channel_type": "webhook",
		"config":       map[string]interface{}{"url": "https://example.com/hook"},
		"event_filter": map[string]interface{}{"type": "*"},
	})
	if err != nil {
		t.Fatalf("create err: %v", err)
	}
	if createRes.IsError {
		t.Fatalf("create returned error: %s", createRes.Content[0].Text)
	}
	createPayload := decodeResultMap(t, createRes)
	channelID := createPayload["channel_id"].(string)
	if len(repo.channels) != 1 {
		t.Fatalf("expected channel to be persisted")
	}

	getRes, err := server.CallTool(ctx, "bahia_get_notification_channel", map[string]interface{}{"channel_id": channelID})
	if err != nil {
		t.Fatalf("get err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["name"] != "deployments" {
		t.Fatalf("unexpected channel name: %v", getPayload["name"])
	}
	config := getPayload["config"].(map[string]interface{})
	if config["url"] != "[redacted]" || getPayload["config_redacted"] != true {
		t.Fatalf("expected sensitive config redaction, got config=%#v redacted=%#v", config, getPayload["config_redacted"])
	}

	listRes, err := server.CallTool(ctx, "bahia_list_notification_channels", map[string]interface{}{"enabled": true})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected 1 enabled channel, got %v", listPayload["total"])
	}

	updateRes, err := server.CallTool(ctx, "bahia_update_notification_channel", map[string]interface{}{
		"channel_id": channelID,
		"name":       "deployments-v2",
		"enabled":    false,
	})
	if err != nil {
		t.Fatalf("update err: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("update returned error: %s", updateRes.Content[0].Text)
	}
	updatePayload := decodeResultMap(t, updateRes)
	updated := updatePayload["channel"].(map[string]interface{})
	if updated["name"] != "deployments-v2" || updated["enabled"] != false {
		t.Fatalf("unexpected updated channel: %#v", updated)
	}

	disabledTestRes, err := server.CallTool(ctx, "bahia_test_notification_channel", map[string]interface{}{"channel_id": channelID})
	if err != nil {
		t.Fatalf("disabled test err: %v", err)
	}
	if !disabledTestRes.IsError {
		t.Fatalf("expected disabled channel test to return error")
	}

	_, err = server.CallTool(ctx, "bahia_update_notification_channel", map[string]interface{}{
		"channel_id": channelID,
		"enabled":    true,
	})
	if err != nil {
		t.Fatalf("enable err: %v", err)
	}
	otherChannel := &domain.NotificationChannel{
		ID:          uuid.New(),
		Name:        "other-matching-channel",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "https://example.com/other"},
		EventFilter: map[string]any{"type": "*"},
		Enabled:     true,
	}
	if err := repo.CreateChannel(ctx, otherChannel); err != nil {
		t.Fatalf("create other channel: %v", err)
	}

	testRes, err := server.CallTool(ctx, "bahia_test_notification_channel", map[string]interface{}{"channel_id": channelID})
	if err != nil {
		t.Fatalf("test err: %v", err)
	}
	if testRes.IsError {
		t.Fatalf("test returned error: %s", testRes.Content[0].Text)
	}
	if len(sender.sent) != 1 || sender.sent[0] != "deployments-v2:test" {
		t.Fatalf("expected test notification send, got %#v", sender.sent)
	}

	deleteRes, err := server.CallTool(ctx, "bahia_delete_notification_channel", map[string]interface{}{"channel_id": channelID})
	if err != nil {
		t.Fatalf("delete err: %v", err)
	}
	if deleteRes.IsError {
		t.Fatalf("delete returned error: %s", deleteRes.Content[0].Text)
	}
	getAfterDeleteRes, err := server.CallTool(ctx, "bahia_get_notification_channel", map[string]interface{}{"channel_id": channelID})
	if err != nil {
		t.Fatalf("get after delete err: %v", err)
	}
	if !getAfterDeleteRes.IsError {
		t.Fatalf("expected get after delete to return error")
	}
}

func TestCallTool_NotificationChannelValidation(t *testing.T) {
	ctx := context.Background()
	server, _, _ := newTestMCPNotificationServer()

	result, err := server.CallTool(ctx, "bahia_create_notification_channel", map[string]interface{}{
		"name":         "bad",
		"channel_type": "email",
		"config":       map[string]interface{}{"address": "ops@example.com"},
	})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected invalid channel type to return error")
	}

	result, err = server.CallTool(ctx, "bahia_get_notification_channel", map[string]interface{}{"channel_id": "not-a-uuid"})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected invalid channel_id to return error")
	}
}
