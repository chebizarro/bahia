package nostr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// TestNotificationPublishRoundTripFullConfig verifies that the notification
// canonical publisher publishes the FULL channel config (including webhook URLs
// and secrets) via NIP-44 encrypted content, and that the config can be
// decrypted and round-tripped back to the original values.
func TestNotificationPublishRoundTripFullConfig(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	publisher := NewNotificationCanonicalPublisher(projector, zap.NewNop())

	ch := &domain.NotificationChannel{
		ID:          uuid.New(),
		OrgID:       uuid.New(),
		Name:        "prod-webhook",
		ChannelType: domain.ChannelTypeWebhook,
		Config: map[string]any{
			"url":    "https://hooks.example.com/notify",
			"secret": "wh_secret_abc123",
		},
		EventFilter: map[string]any{"event_types": []any{"deploy.completed"}},
		Enabled:     true,
	}

	if err := publisher.PublishChannel(ctx, ch); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	sink.mu.Lock()
	events := append([]gonostr.Event(nil), sink.events...)
	sink.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]

	// 1. The wire event content must NOT contain the webhook URL or secret in plaintext.
	if strings.Contains(ev.Content, "https://hooks.example.com/notify") {
		t.Fatal("webhook URL appears in plaintext event content")
	}
	if strings.Contains(ev.Content, "wh_secret_abc123") {
		t.Fatal("webhook secret appears in plaintext event content")
	}

	// 2. The event tags (outer envelope) must NOT contain secrets.
	for _, tag := range ev.Tags {
		for _, v := range tag {
			if strings.Contains(v, "wh_secret_abc123") || strings.Contains(v, "https://hooks.example.com/notify") {
				t.Fatal("sensitive value found in plaintext tags")
			}
		}
	}

	// 3. Decrypt the content with the same service key and verify full config round-trips.
	secret, err := gonostr.SecretKeyFromHex(projectorTestPrivateKey)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	servicePub := secret.Public()
	convKey, err := nip44.GenerateConversationKey(servicePub, secret)
	if err != nil {
		t.Fatalf("generate conversation key: %v", err)
	}
	decrypted, err := nip44.Decrypt(ev.Content, convKey)
	if err != nil {
		t.Fatalf("NIP-44 decrypt failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(decrypted), &payload); err != nil {
		t.Fatalf("unmarshal decrypted content: %v", err)
	}

	// The decrypted payload must contain the full config including URL and secret.
	cfg, ok := payload["config"].(map[string]any)
	if !ok {
		t.Fatal("decrypted payload missing config")
	}
	if cfg["url"] != "https://hooks.example.com/notify" {
		t.Fatalf("url mismatch in decrypted config: %v", cfg["url"])
	}
	if cfg["secret"] != "wh_secret_abc123" {
		t.Fatalf("secret mismatch in decrypted config: %v", cfg["secret"])
	}

	// Verify other fields round-tripped.
	if payload["name"] != "prod-webhook" {
		t.Fatalf("name mismatch: %v", payload["name"])
	}
	if payload["channel_type"] != "webhook" {
		t.Fatalf("channel_type mismatch: %v", payload["channel_type"])
	}
}

// TestNotificationPublishTombstoneEncrypted verifies that a tombstone publish
// also uses NIP-44 encrypted content with no plaintext sensitive data.
func TestNotificationPublishTombstoneEncrypted(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	publisher := NewNotificationCanonicalPublisher(projector, zap.NewNop())

	channelID := uuid.New()
	if err := publisher.PublishChannelDeleted(ctx, channelID); err != nil {
		t.Fatalf("publish tombstone failed: %v", err)
	}

	sink.mu.Lock()
	events := append([]gonostr.Event(nil), sink.events...)
	sink.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	// Tombstone content should be encrypted, decrypt and verify.
	secret, _ := gonostr.SecretKeyFromHex(projectorTestPrivateKey)
	servicePub := secret.Public()
	convKey, _ := nip44.GenerateConversationKey(servicePub, secret)
	decrypted, err := nip44.Decrypt(events[0].Content, convKey)
	if err != nil {
		t.Fatalf("NIP-44 decrypt tombstone failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(decrypted), &payload); err != nil {
		t.Fatalf("unmarshal decrypted tombstone: %v", err)
	}
	if payload["deleted"] != true {
		t.Fatal("expected deleted=true in tombstone")
	}
}
