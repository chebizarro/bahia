package controlplane

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// --- test doubles ---

type memNotificationRepo struct {
	mu       sync.RWMutex
	channels map[uuid.UUID]*domain.NotificationChannel
}

func newMemNotificationRepo() *memNotificationRepo {
	return &memNotificationRepo{channels: make(map[uuid.UUID]*domain.NotificationChannel)}
}

func (r *memNotificationRepo) CreateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *ch
	r.channels[ch.ID] = &cp
	return nil
}

func (r *memNotificationRepo) GetChannelByID(_ context.Context, id uuid.UUID) (*domain.NotificationChannel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[id]
	if !ok {
		return nil, nil
	}
	cp := *ch
	return &cp, nil
}

func (r *memNotificationRepo) UpdateChannel(_ context.Context, ch *domain.NotificationChannel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *ch
	r.channels[ch.ID] = &cp
	return nil
}

func (r *memNotificationRepo) DeleteChannel(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.channels, id)
	return nil
}

func (r *memNotificationRepo) ListChannels(_ context.Context, enabledOnly bool) ([]domain.NotificationChannel, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []domain.NotificationChannel
	for _, ch := range r.channels {
		if enabledOnly && !ch.Enabled {
			continue
		}
		result = append(result, *ch)
	}
	return result, nil
}

// memNotificationPublisher records publish calls.
type memNotificationPublisher struct {
	mu         sync.Mutex
	published  []*domain.NotificationChannel
	tombstones []uuid.UUID
}

func (p *memNotificationPublisher) PublishChannel(_ context.Context, ch *domain.NotificationChannel) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := *ch
	p.published = append(p.published, &cp)
	return nil
}

func (p *memNotificationPublisher) PublishChannelDeleted(_ context.Context, id uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tombstones = append(p.tombstones, id)
	return nil
}

// memChannelChangeNotifier records change notifications.
type memChannelChangeNotifier struct {
	mu       sync.Mutex
	changes  []*domain.NotificationChannel
	deleteds []bool
}

func (n *memChannelChangeNotifier) OnChannelChanged(ch *domain.NotificationChannel, deleted bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := *ch
	n.changes = append(n.changes, &cp)
	n.deleteds = append(n.deleteds, deleted)
}

// --- fixture ---

type notificationIntentFixture struct {
	processor *IntentProcessor
	handler   *NotificationIntentHandler
	repo      *memNotificationRepo
	publisher *memNotificationPublisher
	notifier  *memChannelChangeNotifier
	trustSet  *TrustSet
	ownerPub  string
	logger    *zap.Logger
}

func newNotificationIntentFixture(t *testing.T) *notificationIntentFixture {
	t.Helper()
	logger := zap.NewNop()
	_, ownerPub := testNostrKeypair()
	orgID := testOrgID()

	repo := newMemNotificationRepo()
	publisher := &memNotificationPublisher{}
	notifier := &memChannelChangeNotifier{}

	trustSet := NewTrustSet(nil, logger,
		WithBootstrapOwners(map[string]string{
			orgID.String(): ownerPub,
		}),
	)

	processor := NewIntentProcessor(
		trustSet, nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"notification": true}},
		logger,
	)

	handler := NewNotificationIntentHandler(NotificationIntentHandlerConfig{
		Registry:  repo,
		Publisher: publisher,
		Notifier:  notifier,
		Logger:    logger,
	})
	processor.RegisterHandler("notification", handler)

	return &notificationIntentFixture{
		processor: processor,
		handler:   handler,
		repo:      repo,
		publisher: publisher,
		notifier:  notifier,
		trustSet:  trustSet,
		ownerPub:  ownerPub,
		logger:    logger,
	}
}

func makeTestNotificationIntent(t *testing.T, op string, content map[string]interface{}, intentID, pubkeyHex string) *nostr.Event {
	t.Helper()
	contentJSON, _ := json.Marshal(content)
	coordinate := ""
	if id, ok := content["id"].(string); ok {
		coordinate = id
	}

	ev := &nostr.Event{
		Kind:      30900,
		PubKey:    testNostrPubKeyFromHex(t, pubkeyHex),
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", coordinate},
			{"domain", "notification"},
			{"schema", "bahia.intent.notification.v1"},
			{"t", "bahia-intent"},
			{"t", "notification-channel"},
			{"op", op},
			{"org", testOrgID().String()},
			{"intent_id", intentID},
		},
		Content: string(contentJSON),
	}
	ev.ID = ev.GetID()
	return ev
}

// --- tests ---

func TestNotificationIntentHandler_CreateViaRelayIntent(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()

	channelID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Alerts Webhook",
		"channel_type": "webhook",
		"config": map[string]any{
			"url":    "https://hooks.example.com/alert",
			"secret": "wh-secret-123",
		},
		"event_filter": map[string]any{
			"event_types": []string{"deployment.failed"},
		},
		"enabled": true,
	}
	ev := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("ProcessRelayIntent failed: %v", err)
	}

	stored, _ := f.repo.GetChannelByID(ctx, channelID)
	if stored == nil {
		t.Fatal("channel not created after relay intent")
	}
	if stored.Name != "Alerts Webhook" {
		t.Errorf("expected name 'Alerts Webhook', got %q", stored.Name)
	}
	if stored.ChannelType != domain.ChannelTypeWebhook {
		t.Errorf("expected type 'webhook', got %q", stored.ChannelType)
	}
	if !stored.Enabled {
		t.Error("expected channel to be enabled")
	}

	// Verify publish.
	if len(f.publisher.published) != 1 {
		t.Fatalf("expected 1 publish call, got %d", len(f.publisher.published))
	}
	if f.publisher.published[0].ID != channelID {
		t.Errorf("published ID %s != channel ID %s", f.publisher.published[0].ID, channelID)
	}

	// Verify notifier was called.
	if len(f.notifier.changes) != 1 {
		t.Fatalf("expected 1 notifier call, got %d", len(f.notifier.changes))
	}
	if f.notifier.deleteds[0] {
		t.Error("notifier should report created, not deleted")
	}
}

func TestNotificationIntentHandler_UpdateViaInProcess(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()
	statuses := &statusCollector{}
	f.processor.status = NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop())

	channelID := domain.NewEntityID()
	// Create via relay.
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Slack Hook",
		"channel_type": "webhook",
		"config":       map[string]any{"url": "https://hooks.slack.com/aaa"},
		"enabled":      true,
	}
	ev := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	current, _ := f.repo.GetChannelByID(ctx, channelID)
	revision := current.UpdatedAt

	// Update via in-process (dual dispatch).
	intent := &Intent{
		Domain:            "notification",
		Op:                "update",
		OrgID:             testOrgID(),
		IntentID:          uuid.New().String(),
		Coordinate:        channelID.String(),
		Actor:             f.ownerPub,
		ExpectedUpdatedAt: &revision,
		Content: map[string]interface{}{
			"id":                  channelID.String(),
			"name":                "Slack Hook v2",
			"channel_type":        "webhook",
			"config":              map[string]any{"url": "https://hooks.slack.com/bbb"},
			"enabled":             false,
			"expected_updated_at": revision.Format(time.RFC3339Nano),
		},
	}

	if err := f.processor.ProcessInProcess(ctx, intent); err != nil {
		t.Fatalf("ProcessInProcess failed: %v", err)
	}

	stored, _ := f.repo.GetChannelByID(ctx, channelID)
	if stored == nil {
		t.Fatal("channel not found after update")
	}
	if stored.Name != "Slack Hook v2" {
		t.Errorf("expected name 'Slack Hook v2', got %q", stored.Name)
	}
	if stored.Enabled {
		t.Error("expected channel to be disabled after update")
	}

	// 2 publishes (create + update).
	if len(f.publisher.published) != 2 {
		t.Fatalf("expected 2 publish calls, got %d", len(f.publisher.published))
	}
	stale := *intent
	stale.IntentID = uuid.New().String()
	if err := f.processor.ProcessInProcess(ctx, &stale); !IsRevisionConflict(err) {
		t.Fatalf("stale revision must conflict: %v", err)
	}
	if len(f.publisher.published) != 2 || len(statuses.events) != 3 || tagValueNostr(statuses.events[2].Tags, "status") != "conflict" {
		t.Fatal("stale notification update mutated state or missed conflict status")
	}
}

func TestNotificationIntentHandler_Delete(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()

	channelID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Temp Channel",
		"channel_type": "nostr_dm",
		"enabled":      true,
	}
	ev := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Delete.
	delContent := map[string]interface{}{
		"id": channelID.String(),
	}
	evDel := makeTestNotificationIntent(t, "delete", delContent, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, evDel); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	stored, _ := f.repo.GetChannelByID(ctx, channelID)
	if stored != nil {
		t.Fatal("channel should be deleted")
	}
	if len(f.publisher.tombstones) != 1 {
		t.Fatalf("expected 1 tombstone, got %d", len(f.publisher.tombstones))
	}
	if f.publisher.tombstones[0] != channelID {
		t.Errorf("tombstone ID %s != channel ID %s", f.publisher.tombstones[0], channelID)
	}

	// Verify notifier reports deletion.
	// 2 notifier calls: 1 create + 1 delete.
	if len(f.notifier.changes) != 2 {
		t.Fatalf("expected 2 notifier calls, got %d", len(f.notifier.changes))
	}
	if !f.notifier.deleteds[1] {
		t.Error("notifier should report deleted on second call")
	}
}

func TestNotificationIntentHandler_Unauthorized(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()

	// Untrusted authors are dropped silently (no error).
	_, untrustedPub := testNostrKeypair()

	channelID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Evil Channel",
		"channel_type": "webhook",
		"enabled":      true,
	}
	ev := makeTestNotificationIntent(t, "create", content, uuid.New().String(), untrustedPub)

	err := f.processor.ProcessRelayIntent(ctx, ev)
	if err != nil {
		t.Fatalf("expected silent drop for untrusted author, got error: %v", err)
	}
	stored, _ := f.repo.GetChannelByID(ctx, channelID)
	if stored != nil {
		t.Fatal("untrusted key should not have been able to create a channel")
	}
}

func TestNotificationIntentHandler_Idempotent(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()

	channelID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Idempotent Channel",
		"channel_type": "webhook",
		"enabled":      true,
	}

	// Two intents with different intent_ids for the same entity.
	ev1 := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	ev2 := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)

	if err := f.processor.ProcessRelayIntent(ctx, ev1); err != nil {
		t.Fatalf("first process failed: %v", err)
	}
	if err := f.processor.ProcessRelayIntent(ctx, ev2); err != nil {
		t.Fatalf("second process failed: %v", err)
	}

	// Level-triggered: both run, second updates existing. One entity, 2 publishes.
	stored, _ := f.repo.GetChannelByID(ctx, channelID)
	if stored == nil {
		t.Fatal("channel should exist after two intents")
	}
	if len(f.publisher.published) != 2 {
		t.Errorf("expected 2 publish calls (level-triggered), got %d", len(f.publisher.published))
	}
}

func TestNotificationIntentHandler_PermissionIsManageSettings(t *testing.T) {
	handler := NewNotificationIntentHandler(NotificationIntentHandlerConfig{Logger: zap.NewNop()})
	perm := handler.PermissionFor("create")
	if perm != domain.PermManageSettings {
		t.Errorf("expected PermManageSettings, got %v", perm)
	}
	perm2 := handler.PermissionFor("delete")
	if perm2 != domain.PermManageSettings {
		t.Errorf("expected PermManageSettings for delete, got %v", perm2)
	}
}

func TestNotificationIntentHandler_EventDrivenNotifier(t *testing.T) {
	f := newNotificationIntentFixture(t)
	ctx := context.Background()

	channelID := domain.NewEntityID()
	content := map[string]interface{}{
		"id":           channelID.String(),
		"name":         "Event-Driven Test",
		"channel_type": "webhook",
		"config": map[string]any{
			"url": "https://example.com/hook",
		},
		"enabled": true,
	}
	ev := makeTestNotificationIntent(t, "create", content, uuid.New().String(), f.ownerPub)
	if err := f.processor.ProcessRelayIntent(ctx, ev); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Verify notifier received the channel with its config intact.
	if len(f.notifier.changes) != 1 {
		t.Fatalf("expected 1 notifier change, got %d", len(f.notifier.changes))
	}
	notified := f.notifier.changes[0]
	if notified.ID != channelID {
		t.Errorf("notifier channel ID %s != %s", notified.ID, channelID)
	}
	if notified.Config == nil {
		t.Error("notifier channel should have config")
	}
}
