package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/events"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// mockNotificationRepo is an in-memory NotificationRepository.
type mockNotificationRepo struct {
	mu              sync.Mutex
	channels        []domain.NotificationChannel
	logs            []domain.NotificationLog
	createLogErr    error
	updateLogErr    error
	listChannelsErr error
}

func (m *mockNotificationRepo) CreateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch.ID == uuid.Nil {
		ch.ID = uuid.New()
	}
	m.channels = append(m.channels, *ch)
	return nil
}

func (m *mockNotificationRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.channels {
		if ch.ID == id {
			return &ch, nil
		}
	}
	return nil, nil
}

func (m *mockNotificationRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listChannelsErr != nil {
		return nil, m.listChannelsErr
	}
	var result []domain.NotificationChannel
	for _, ch := range m.channels {
		if enabledOnly && !ch.Enabled {
			continue
		}
		result = append(result, ch)
	}
	return result, nil
}

func (m *mockNotificationRepo) UpdateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.channels {
		if existing.ID == ch.ID {
			m.channels[i] = *ch
			return nil
		}
	}
	return nil
}

func (m *mockNotificationRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, ch := range m.channels {
		if ch.ID == id {
			m.channels = append(m.channels[:i], m.channels[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockNotificationRepo) CreateLog(_ context.Context, log *domain.NotificationLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createLogErr != nil {
		return m.createLogErr
	}
	m.logs = append(m.logs, *log)
	return nil
}

func (m *mockNotificationRepo) UpdateLog(_ context.Context, log *domain.NotificationLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateLogErr != nil {
		return m.updateLogErr
	}
	for i, existing := range m.logs {
		if existing.ID == log.ID {
			m.logs[i] = *log
			return nil
		}
	}
	return nil
}

func (m *mockNotificationRepo) ListLogsByChannel(_ context.Context, channelID uuid.UUID, limit int) ([]domain.NotificationLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.NotificationLog
	for _, l := range m.logs {
		if l.ChannelID == channelID {
			result = append(result, l)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (m *mockNotificationRepo) ListRecentLogs(_ context.Context, limit int) ([]domain.NotificationLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit > len(m.logs) {
		limit = len(m.logs)
	}
	return m.logs[:limit], nil
}

func (m *mockNotificationRepo) ListRetryable(_ context.Context, maxAttempts int) ([]domain.NotificationLog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.NotificationLog
	for _, l := range m.logs {
		if (l.Status == domain.NotificationStatusRetrying || l.Status == domain.NotificationStatusPending) && l.Attempts < maxAttempts {
			result = append(result, l)
		}
	}
	return result, nil
}

// mockSender tracks sent notifications.
type mockSender struct {
	mu       sync.Mutex
	sent     []sentNotification
	failNext bool
	notify   chan struct{}
}

type sentNotification struct {
	Channel   string
	EventType string
	Payload   map[string]any
}

func (s *mockSender) Send(_ context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return fmt.Errorf("mock send failure")
	}
	s.sent = append(s.sent, sentNotification{
		Channel:   ch.Name,
		EventType: eventType,
		Payload:   payload,
	})
	if s.notify != nil {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestDispatcher_Dispatch(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{}

	channelID := uuid.New()
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          channelID,
		Name:        "test-webhook",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com/hook"},
		Enabled:     true,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	d.Dispatch(context.Background(), "deployment_intent.created", map[string]any{"service": "api"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 sent, got %d", len(sender.sent))
	}
	if sender.sent[0].EventType != "deployment_intent.created" {
		t.Errorf("expected deployment_intent.created, got %s", sender.sent[0].EventType)
	}
	if sender.sent[0].Channel != "test-webhook" {
		t.Errorf("expected test-webhook, got %s", sender.sent[0].Channel)
	}
}

func TestDispatcher_SetupSubscriptionsDispatchesSecurityPolicyBreached(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{notify: make(chan struct{}, 1)}
	repo.channels = append(repo.channels, domain.NotificationChannel{ID: uuid.New(), Name: "security", ChannelType: domain.ChannelTypeWebhook, EventFilter: map[string]any{"type": "security.policy_breached"}, Enabled: true})
	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)
	pub := events.NewInProcessPublisher(zap.NewNop())
	d.SetupSubscriptions(pub)
	pub.Publish(context.Background(), events.Event{Type: events.EventSecurityPolicyBreached, EntityID: "breach-1", Data: map[string]any{"policy_id": "policy-1"}})
	select {
	case <-sender.notify:
	case <-time.After(time.Second):
		t.Fatal("security policy breach notification was not dispatched")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.logs) != 1 || repo.logs[0].EventType != "security.policy_breached" {
		t.Fatalf("security breach log not recorded: %+v", repo.logs)
	}
}

func TestDispatcher_EventFilter(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{}

	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          uuid.New(),
		Name:        "filtered",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com/hook"},
		EventFilter: map[string]any{"types": []any{"drift.detected"}},
		Enabled:     true,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	// Send a non-matching event.
	d.Dispatch(context.Background(), "build.registered", map[string]any{})

	sender.mu.Lock()
	if len(sender.sent) != 0 {
		t.Errorf("expected 0 sent (filtered), got %d", len(sender.sent))
	}
	sender.mu.Unlock()

	// Send a matching event.
	d.Dispatch(context.Background(), "drift.detected", map[string]any{})

	sender.mu.Lock()
	if len(sender.sent) != 1 {
		t.Errorf("expected 1 sent, got %d", len(sender.sent))
	}
	sender.mu.Unlock()
}

func TestDispatcher_DisabledChannel(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{}

	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          uuid.New(),
		Name:        "disabled",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com"},
		Enabled:     false,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	d.Dispatch(context.Background(), "test", map[string]any{})

	sender.mu.Lock()
	if len(sender.sent) != 0 {
		t.Errorf("expected 0 sent (disabled channel), got %d", len(sender.sent))
	}
	sender.mu.Unlock()
}

func TestDispatcher_FailedDelivery(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{failNext: true}

	channelID := uuid.New()
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          channelID,
		Name:        "failing",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com"},
		Enabled:     true,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	d.Dispatch(context.Background(), "test", map[string]any{})

	// Should have a log entry with retrying status.
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.logs) == 0 {
		t.Fatal("expected log entry for failed delivery")
	}
	if repo.logs[0].Status != domain.NotificationStatusRetrying {
		t.Errorf("expected retrying status, got %s", repo.logs[0].Status)
	}
}

func TestDispatcherReturnsSendAndPersistenceFailures(t *testing.T) {
	persistErr := errors.New("notification log unavailable")
	repo := &mockNotificationRepo{updateLogErr: persistErr}
	sender := &mockSender{failNext: true}
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID: uuid.New(), Name: "failing", ChannelType: domain.ChannelTypeWebhook, Enabled: true,
	})
	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	err := d.Dispatch(context.Background(), "test", map[string]any{})
	if err == nil {
		t.Fatal("Dispatch returned nil, want joined send and persistence failure")
	}
	if !strings.Contains(err.Error(), "mock send failure") || !errors.Is(err, persistErr) {
		t.Fatalf("Dispatch error = %v, want send and persistence failures", err)
	}
}

func TestDispatcherDoesNotSendWhenLogCreationFails(t *testing.T) {
	persistErr := errors.New("cannot create log")
	repo := &mockNotificationRepo{createLogErr: persistErr}
	sender := &mockSender{}
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID: uuid.New(), Name: "untracked", ChannelType: domain.ChannelTypeWebhook, Enabled: true,
	})
	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	err := d.Dispatch(context.Background(), "test", map[string]any{})
	if !errors.Is(err, persistErr) {
		t.Fatalf("Dispatch error = %v, want log creation failure", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent %d untracked notifications", len(sender.sent))
	}
}

func TestNostrDMSenderRejectsZeroRelayPublication(t *testing.T) {
	privateKey := strings.Repeat("1", 64)
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		t.Fatalf("parse recipient secret key: %v", err)
	}
	recipient := secret.Public().Hex()
	sender := &NostrDMSender{
		privateKey: privateKey,
		logger:     zap.NewNop(),
		publish: func(context.Context, nostr.Event) (int, error) {
			return 0, nil
		},
	}
	channel := &domain.NotificationChannel{Name: "dm", Config: map[string]any{"pubkey": recipient}}

	err = sender.Send(context.Background(), channel, "test", map[string]any{"ok": true})
	if err == nil || !strings.Contains(err.Error(), "no relay accepted") {
		t.Fatalf("Send error = %v, want zero-relay failure", err)
	}
}

func TestDispatcher_LogCreated(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{}

	channelID := uuid.New()
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          channelID,
		Name:        "logging",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com"},
		Enabled:     true,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	d.Dispatch(context.Background(), "test.event", map[string]any{"key": "value"})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.logs) == 0 {
		t.Fatal("expected log entry")
	}
	log := repo.logs[0]
	if log.EventType != "test.event" {
		t.Errorf("expected test.event, got %s", log.EventType)
	}
	if log.Status != domain.NotificationStatusSent {
		t.Errorf("expected sent, got %s", log.Status)
	}
	if log.ChannelID != channelID {
		t.Error("channel ID mismatch")
	}
}

func TestWebhookSender(t *testing.T) {
	var received map[string]any
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender()
	ch := &domain.NotificationChannel{
		Name:        "test-hook",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": server.URL},
	}

	err := sender.Send(context.Background(), ch, "build.registered", map[string]any{"build_id": "123"})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	if received["event"] != "build.registered" {
		t.Errorf("expected build.registered, got %v", received["event"])
	}
	if receivedHeaders.Get("Content-Type") != "application/json" {
		t.Error("expected application/json content type")
	}
	if receivedHeaders.Get("X-Bahia-Event") != "build.registered" {
		t.Error("expected X-Bahia-Event header")
	}
}

func TestWebhookSender_WithSignature(t *testing.T) {
	secret := "my-webhook-secret"
	var receivedSig string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Bahia-Signature")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewWebhookSender()
	ch := &domain.NotificationChannel{
		Name:   "signed-hook",
		Config: map[string]any{"url": server.URL, "secret": secret},
	}

	err := sender.Send(context.Background(), ch, "test", map[string]any{})
	if err != nil {
		t.Fatalf("send failed: %v", err)
	}

	if receivedSig == "" {
		t.Fatal("expected signature header")
	}

	// Verify the signature.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if receivedSig != expected {
		t.Errorf("signature mismatch: got %s, want %s", receivedSig, expected)
	}
}

func TestWebhookSender_NonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sender := NewWebhookSender()
	ch := &domain.NotificationChannel{
		Name:   "error-hook",
		Config: map[string]any{"url": server.URL},
	}

	err := sender.Send(context.Background(), ch, "test", map[string]any{})
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestWebhookSender_MissingURL(t *testing.T) {
	sender := NewWebhookSender()
	ch := &domain.NotificationChannel{
		Name:   "no-url",
		Config: map[string]any{},
	}

	err := sender.Send(context.Background(), ch, "test", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing URL")
	}
}

func TestMatchesEvent(t *testing.T) {
	tests := []struct {
		name   string
		filter map[string]any
		event  string
		want   bool
	}{
		{"empty filter matches all", nil, "any.event", true},
		{"types array match", map[string]any{"types": []any{"build.registered", "drift.detected"}}, "drift.detected", true},
		{"types array no match", map[string]any{"types": []any{"build.registered"}}, "drift.detected", false},
		{"type single match", map[string]any{"type": "drift.detected"}, "drift.detected", true},
		{"type wildcard", map[string]any{"type": "*"}, "anything", true},
		{"type single no match", map[string]any{"type": "build.registered"}, "drift.detected", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := &domain.NotificationChannel{EventFilter: tc.filter}
			got := ch.MatchesEvent(tc.event)
			if got != tc.want {
				t.Errorf("MatchesEvent(%q) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}

func TestRetryFailed(t *testing.T) {
	repo := &mockNotificationRepo{}
	sender := &mockSender{}

	channelID := uuid.New()
	repo.channels = append(repo.channels, domain.NotificationChannel{
		ID:          channelID,
		Name:        "retry-test",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "http://example.com"},
		Enabled:     true,
	})

	// Add a retryable log entry.
	repo.logs = append(repo.logs, domain.NotificationLog{
		ID:        uuid.New(),
		ChannelID: channelID,
		EventType: "test",
		Payload:   map[string]any{"test": true},
		Status:    domain.NotificationStatusRetrying,
		Attempts:  1,
	})

	d := NewDispatcher(repo, zap.NewNop())
	d.RegisterSender(domain.ChannelTypeWebhook, sender)

	retried, err := d.RetryFailed(context.Background(), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried != 1 {
		t.Errorf("expected 1 retried, got %d", retried)
	}

	sender.mu.Lock()
	if len(sender.sent) != 1 {
		t.Errorf("expected 1 sent, got %d", len(sender.sent))
	}
	sender.mu.Unlock()
}
