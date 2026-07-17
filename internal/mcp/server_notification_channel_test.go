package mcp

import (
	"context"
	"fmt"
	"strings"
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
	ctx := authorizedMCPContext()
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
	ctx := authorizedMCPContext()
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

func TestGetTools_IncludesNotificationLogHandlers(t *testing.T) {
	server, _, _ := newTestMCPNotificationServer()
	required := map[string]bool{
		"bahia_list_notifications":     false,
		"bahia_get_notification":       false,
		"bahia_mark_notification_read": false,
		"bahia_dismiss_notification":   false,
	}

	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
			if tool.InputSchema["required"] == nil && tool.Name != "bahia_list_notifications" {
				t.Fatalf("%s missing required schema", tool.Name)
			}
		}
	}
	for name, present := range required {
		if !present {
			t.Fatalf("missing tool %s", name)
		}
	}
}

func TestCallTool_NotificationLogListAndMarkRead(t *testing.T) {
	ctx := authorizedMCPContext()
	server, repo, _ := newTestMCPNotificationServer()
	channelID := uuid.New()
	pendingDeployID := uuid.New()
	pendingBillingID := uuid.New()
	sentDeployID := uuid.New()
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)

	logs := []domain.NotificationLog{
		{
			ID:        pendingDeployID,
			ChannelID: channelID,
			EventType: "deployment.failed",
			Payload:   map[string]any{"service": "api"},
			Status:    domain.NotificationStatusPending,
			Attempts:  0,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        pendingBillingID,
			ChannelID: channelID,
			EventType: "billing.low_balance",
			Payload:   map[string]any{"balance": 42},
			Status:    domain.NotificationStatusRetrying,
			Attempts:  2,
			LastError: "rate limited",
			CreatedAt: now.Add(time.Minute),
			UpdatedAt: now.Add(time.Minute),
		},
		{
			ID:        sentDeployID,
			ChannelID: channelID,
			EventType: "deployment.failed",
			Payload:   map[string]any{"service": "worker"},
			Status:    domain.NotificationStatusSent,
			Attempts:  1,
			CreatedAt: now.Add(2 * time.Minute),
			UpdatedAt: now.Add(2 * time.Minute),
		},
	}
	for i := range logs {
		if err := repo.CreateLog(ctx, &logs[i]); err != nil {
			t.Fatalf("create log: %v", err)
		}
	}

	listRes, err := server.CallTool(ctx, "bahia_list_notifications", map[string]interface{}{
		"status":     "unread",
		"event_type": "deployment.failed",
		"limit":      float64(10),
	})
	if err != nil {
		t.Fatalf("list err: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list returned error: %s", listRes.Content[0].Text)
	}
	listPayload := decodeResultMap(t, listRes)
	if int(listPayload["total"].(float64)) != 1 {
		t.Fatalf("expected one unread deployment notification, got %#v", listPayload)
	}
	notifications := listPayload["notifications"].([]interface{})
	notification := notifications[0].(map[string]interface{})
	if notification["id"] != pendingDeployID.String() || notification["status"] != string(domain.NotificationStatusPending) {
		t.Fatalf("unexpected notification payload: %#v", notification)
	}
	if _, ok := notification["last_error"]; ok {
		t.Fatalf("unexpected last_error on pending notification: %#v", notification)
	}

	retryListRes, err := server.CallTool(ctx, "bahia_list_notifications", map[string]interface{}{
		"status":     "unread",
		"event_type": "billing.low_balance",
	})
	if err != nil {
		t.Fatalf("retry list err: %v", err)
	}
	if retryListRes.IsError {
		t.Fatalf("retry list returned error: %s", retryListRes.Content[0].Text)
	}
	retryPayload := decodeResultMap(t, retryListRes)
	retryNotifications := retryPayload["notifications"].([]interface{})
	retryNotification := retryNotifications[0].(map[string]interface{})
	if retryNotification["last_error"] != "rate limited" {
		t.Fatalf("expected last_error for retrying notification, got %#v", retryNotification)
	}

	markRes, err := server.CallTool(ctx, "bahia_mark_notification_read", map[string]interface{}{
		"notification_id": pendingDeployID.String(),
	})
	if err != nil {
		t.Fatalf("mark read err: %v", err)
	}
	if markRes.IsError {
		t.Fatalf("mark read returned error: %s", markRes.Content[0].Text)
	}
	markPayload := decodeResultMap(t, markRes)
	if markPayload["status"] != "marked_read" || markPayload["notification_id"] != pendingDeployID.String() {
		t.Fatalf("unexpected mark read payload: %#v", markPayload)
	}
	if repo.logs[pendingDeployID].Status != domain.NotificationStatusSent {
		t.Fatalf("expected notification to be marked sent/read, got %s", repo.logs[pendingDeployID].Status)
	}

	readListRes, err := server.CallTool(ctx, "bahia_list_notifications", map[string]interface{}{"status": "read"})
	if err != nil {
		t.Fatalf("read list err: %v", err)
	}
	if readListRes.IsError {
		t.Fatalf("read list returned error: %s", readListRes.Content[0].Text)
	}
	readPayload := decodeResultMap(t, readListRes)
	if int(readPayload["total"].(float64)) != 2 {
		t.Fatalf("expected two read notifications after mark-read, got %#v", readPayload)
	}
}

func TestCallTool_NotificationLogUnsupportedAndValidation(t *testing.T) {
	ctx := authorizedMCPContext()
	server, _, _ := newTestMCPNotificationServer()

	result, err := server.CallTool(ctx, "bahia_get_notification", map[string]interface{}{"notification_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("get call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not currently supported") {
		t.Fatalf("expected unsupported get error, got %#v", result)
	}

	result, err = server.CallTool(ctx, "bahia_dismiss_notification", map[string]interface{}{"notification_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("dismiss call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "immutable audit records") {
		t.Fatalf("expected unsupported dismiss error, got %#v", result)
	}

	result, err = server.CallTool(ctx, "bahia_mark_notification_read", map[string]interface{}{})
	if err != nil {
		t.Fatalf("mark missing id call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "notification_id is required") {
		t.Fatalf("expected missing notification_id error, got %#v", result)
	}

	result, err = server.CallTool(ctx, "bahia_mark_notification_read", map[string]interface{}{"notification_id": "not-a-uuid"})
	if err != nil {
		t.Fatalf("mark invalid id call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "invalid notification_id") {
		t.Fatalf("expected invalid notification_id error, got %#v", result)
	}

	result, err = server.CallTool(ctx, "bahia_mark_notification_read", map[string]interface{}{"notification_id": uuid.New().String()})
	if err != nil {
		t.Fatalf("mark missing log call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "notification not found") {
		t.Fatalf("expected notification not found error, got %#v", result)
	}

	unconfigured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	result, err = unconfigured.CallTool(ctx, "bahia_list_notifications", map[string]interface{}{})
	if err != nil {
		t.Fatalf("unconfigured call err: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "not configured") {
		t.Fatalf("expected configuration error, got %#v", result)
	}
}
