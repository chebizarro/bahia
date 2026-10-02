package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// notifLegacyFixture tests the ContextVM notification handlers when the
// notification intent domain is DISABLED (legacy path).
type notifLegacyFixture struct {
	handlers  *EncryptedRouteHandlers
	repo      *memNotificationRepo
	publisher *memNotificationPublisher
	notifier  *memChannelChangeNotifier
	orgID     uuid.UUID
	pubkey    string
}

func newNotifLegacyFixture(t *testing.T) *notifLegacyFixture {
	t.Helper()
	_, pubkey := testNostrKeypair()
	orgID := testOrgID()
	repo := newMemNotificationRepo()
	publisher := &memNotificationPublisher{}
	notifier := &memChannelChangeNotifier{}

	// Intent processor with NO notification handler → legacy path.
	processor := NewIntentProcessor(
		NewTrustSet(nil, zap.NewNop()), nil, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{}},
		zap.NewNop(),
	)

	rbac := auth.NewRBAC(&encryptedMemberRepo{
		members: []domain.OrgMember{{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner}},
	})

	handlers := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{
		IntentProcessor: processor,
		NotifRepo:       &fullNotifRepo{repo},
		NotifPublisher:  publisher,
		NotifNotifier:   notifier,
		RBAC:            rbac,
		Logger:          zap.NewNop(),
	})
	return &notifLegacyFixture{
		handlers:  handlers,
		repo:      repo,
		publisher: publisher,
		notifier:  notifier,
		orgID:     orgID,
		pubkey:    pubkey,
	}
}

func (f *notifLegacyFixture) contextVMRequest(t *testing.T, params map[string]any) ContextVMRequest {
	t.Helper()
	raw, _ := json.Marshal(params)
	return ContextVMRequest{
		Event: &nostr.Event{PubKey: testNostrPubKeyFromHex(t, f.pubkey)},
		RPC:   ContextVMJSONRPCRequest{Params: raw},
	}
}

// TestNotificationLegacyCreateChannel verifies the legacy path creates a
// channel via the repo, publishes a canonical record, and notifies the
// dispatcher.
func TestNotificationLegacyCreateChannel(t *testing.T) {
	f := newNotifLegacyFixture(t)
	ctx := context.Background()
	req := f.contextVMRequest(t, map[string]any{
		"name":         "prod-webhook",
		"channel_type": "webhook",
		"org_id":       f.orgID.String(),
		"config":       map[string]any{"url": "https://example.com/hook", "secret": "s3cret"},
		"enabled":      true,
	})
	result, err := f.handlers.CreateNotificationChannel(ctx, req)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	m := result.(map[string]any)
	if m["status"] != "created" {
		t.Fatalf("expected status created, got %v", m["status"])
	}

	// Channel persisted in repo.
	channels, _ := f.repo.ListChannels(ctx, false)
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	if channels[0].Name != "prod-webhook" {
		t.Fatalf("name mismatch: %s", channels[0].Name)
	}

	// Canonical publish happened.
	f.publisher.mu.Lock()
	pubCount := len(f.publisher.published)
	f.publisher.mu.Unlock()
	if pubCount != 1 {
		t.Fatalf("expected 1 publish, got %d", pubCount)
	}

	// Dispatcher notified.
	f.notifier.mu.Lock()
	notifyCount := len(f.notifier.changes)
	deleted := false
	if notifyCount > 0 {
		deleted = f.notifier.deleteds[0]
	}
	f.notifier.mu.Unlock()
	if notifyCount != 1 {
		t.Fatalf("expected 1 notifier call, got %d", notifyCount)
	}
	if deleted {
		t.Fatal("expected deleted=false for create")
	}
}

// TestNotificationLegacyUpdateChannel verifies the legacy update path loads
// the existing channel, authorises via org RBAC, merges, publishes, and notifies.
func TestNotificationLegacyUpdateChannel(t *testing.T) {
	f := newNotifLegacyFixture(t)
	ctx := context.Background()

	// Seed a channel.
	chID := domain.NewEntityID()
	_ = f.repo.CreateChannel(ctx, &domain.NotificationChannel{
		ID:          chID,
		OrgID:       f.orgID,
		Name:        "old-name",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "https://old.example.com/hook"},
		Enabled:     true,
	})

	req := f.contextVMRequest(t, map[string]any{
		"id":   chID.String(),
		"name": "new-name",
	})
	result, err := f.handlers.UpdateNotificationChannel(ctx, req)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	m := result.(map[string]any)
	if m["status"] != "updated" {
		t.Fatalf("expected status updated, got %v", m["status"])
	}

	// Verify repo was updated.
	updated, _ := f.repo.GetChannelByID(ctx, chID)
	if updated == nil || updated.Name != "new-name" {
		t.Fatalf("expected name new-name, got %v", updated)
	}

	// Publish + notify.
	f.publisher.mu.Lock()
	pubCount := len(f.publisher.published)
	f.publisher.mu.Unlock()
	if pubCount != 1 {
		t.Fatalf("expected 1 publish, got %d", pubCount)
	}
	f.notifier.mu.Lock()
	notifyCount := len(f.notifier.changes)
	f.notifier.mu.Unlock()
	if notifyCount != 1 {
		t.Fatalf("expected 1 notifier call, got %d", notifyCount)
	}
}

// TestNotificationLegacyDeleteChannel verifies the legacy delete path: repo
// delete, tombstone publish, and dispatcher notify with deleted=true.
func TestNotificationLegacyDeleteChannel(t *testing.T) {
	f := newNotifLegacyFixture(t)
	ctx := context.Background()

	chID := domain.NewEntityID()
	_ = f.repo.CreateChannel(ctx, &domain.NotificationChannel{
		ID:          chID,
		OrgID:       f.orgID,
		Name:        "to-delete",
		ChannelType: domain.ChannelTypeWebhook,
		Config:      map[string]any{"url": "https://example.com"},
		Enabled:     true,
	})

	req := f.contextVMRequest(t, map[string]any{"id": chID.String()})
	result, err := f.handlers.DeleteNotificationChannel(ctx, req)
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	m := result.(map[string]any)
	if m["status"] != "deleted" {
		t.Fatalf("expected status deleted, got %v", m["status"])
	}

	// Gone from repo.
	remaining, _ := f.repo.ListChannels(ctx, false)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 channels, got %d", len(remaining))
	}

	// Tombstone published.
	f.publisher.mu.Lock()
	tombCount := len(f.publisher.tombstones)
	f.publisher.mu.Unlock()
	if tombCount != 1 {
		t.Fatalf("expected 1 tombstone, got %d", tombCount)
	}

	// Dispatcher notified with deleted=true.
	f.notifier.mu.Lock()
	deletedFlag := false
	if len(f.notifier.deleteds) > 0 {
		deletedFlag = f.notifier.deleteds[0]
	}
	f.notifier.mu.Unlock()
	if !deletedFlag {
		t.Fatal("expected deleted=true for delete")
	}
}

// TestNotificationLegacyUnauthorized verifies that the legacy path rejects
// mutations from pubkeys that lack PermManageSettings.
func TestNotificationLegacyUnauthorized(t *testing.T) {
	f := newNotifLegacyFixture(t)
	ctx := context.Background()

	// Use a different pubkey that is NOT in the RBAC membership.
	_, otherPubkey := testNostrKeypair()
	raw, _ := json.Marshal(map[string]any{
		"name":         "should-fail",
		"channel_type": "webhook",
		"org_id":       f.orgID.String(),
	})
	req := ContextVMRequest{
		Event: &nostr.Event{PubKey: testNostrPubKeyFromHex(t, otherPubkey)},
		RPC:   ContextVMJSONRPCRequest{Params: raw},
	}
	_, err := f.handlers.CreateNotificationChannel(ctx, req)
	if err == nil {
		t.Fatal("expected error for unauthorized create")
	}
}
