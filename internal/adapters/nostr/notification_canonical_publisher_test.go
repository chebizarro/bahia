package nostr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// mockConfidentialEncryptor is a test double that wraps content in a JSON
// envelope so tests can verify which data was passed for encryption, without
// needing a real OCK or NIP-44 stack.
type mockConfidentialEncryptor struct {
	lastOrgID            string
	lastServiceOnlyPlain []byte
	lastLegacyKind       int
	lastDTag             string
	lastTopic            string
}

func (m *mockConfidentialEncryptor) EncryptConfidential(_ context.Context, orgID string, plaintext []byte, legacyKind int, dTag, topic string, serviceOnlyPlaintext []byte) (string, error) {
	m.lastOrgID = orgID
	m.lastServiceOnlyPlain = serviceOnlyPlaintext
	m.lastLegacyKind = legacyKind
	m.lastDTag = dTag
	m.lastTopic = topic
	// Wrap in a test envelope so we can verify later.
	envelope := map[string]any{
		"_test_encrypted":      true,
		"_org_visible":         json.RawMessage(plaintext),
		"_service_only_exists": len(serviceOnlyPlaintext) > 0,
	}
	if len(serviceOnlyPlaintext) > 0 {
		envelope["_service_only"] = base64.StdEncoding.EncodeToString(serviceOnlyPlaintext)
	}
	out, _ := json.Marshal(envelope)
	return string(out), nil
}

func (m *mockConfidentialEncryptor) DecryptConfidential(_ context.Context, content string, legacyKind int, dTag, topic string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, err
	}
	return envelope["_org_visible"], nil
}

func (m *mockConfidentialEncryptor) DecryptServiceInner(_ context.Context, content string) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, err
	}
	b64, ok := envelope["_service_only"].(string)
	if !ok || b64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(b64)
}

func (m *mockConfidentialEncryptor) RotateKey(_ context.Context, _ string) error {
	return nil
}

func (m *mockConfidentialEncryptor) RotateKeyExcluding(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockConfidentialEncryptor) WrapKeyForMember(_ context.Context, _, _ string) error {
	return nil
}

// TestNotificationPublishRoundTripFullConfig verifies that the notification
// canonical publisher publishes the FULL channel config (including webhook URLs
// and secrets) via the confidential encryptor, and that org-visible content is
// sanitized while service-inner retains credentials.
func TestNotificationPublishRoundTripFullConfig(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	encryptor := &mockConfidentialEncryptor{}
	publisher := NewNotificationCanonicalPublisher(projector, encryptor, nil, zap.NewNop())

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

	// Verify the encryptor received the correct org ID.
	if encryptor.lastOrgID != ch.OrgID.String() {
		t.Fatalf("expected orgID %s, got %s", ch.OrgID.String(), encryptor.lastOrgID)
	}

	// Verify the org-visible layer was called (service-inner also exists).
	if len(encryptor.lastServiceOnlyPlain) == 0 {
		t.Fatal("expected service-inner to be present for notification with credentials")
	}

	// Decrypt org-visible and verify secrets are sanitized.
	sink.mu.Lock()
	events := append([]gonostr.Event(nil), sink.events...)
	sink.mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	orgVisible, err := encryptor.DecryptConfidential(ctx, events[0].Content, 0, "", "")
	if err != nil {
		t.Fatalf("decrypt org-visible: %v", err)
	}
	var orgPayload map[string]any
	if err := json.Unmarshal(orgVisible, &orgPayload); err != nil {
		t.Fatalf("unmarshal org-visible: %v", err)
	}
	// Org-visible should NOT contain the webhook URL or secret (sanitized).
	cfg, _ := orgPayload["config"].(map[string]any)
	if cfg["url"] != nil {
		t.Fatal("webhook URL should not appear in org-visible content")
	}
	if cfg["secret"] != nil {
		t.Fatal("webhook secret should not appear in org-visible content")
	}

	// Decrypt service-inner and verify full config round-trips.
	serviceInner, err := encryptor.DecryptServiceInner(ctx, events[0].Content)
	if err != nil {
		t.Fatalf("decrypt service-inner: %v", err)
	}
	var servicePayload map[string]any
	if err := json.Unmarshal(serviceInner, &servicePayload); err != nil {
		t.Fatalf("unmarshal service-inner: %v", err)
	}
	sCfg, _ := servicePayload["config"].(map[string]any)
	if sCfg["url"] != "https://hooks.example.com/notify" {
		t.Fatalf("url mismatch in service-inner: %v", sCfg["url"])
	}
	if sCfg["secret"] != "wh_secret_abc123" {
		t.Fatalf("secret mismatch in service-inner: %v", sCfg["secret"])
	}

	// Verify the wire event content does NOT contain sensitive data in plaintext.
	if strings.Contains(events[0].Content, "wh_secret_abc123") {
		t.Fatal("webhook secret appears in wire event content")
	}
}

func TestNotificationChannelSignedWireTagsContainNoConfidentialMetadata(t *testing.T) {
	for _, fleetScoped := range []bool{false, true} {
		t.Run(map[bool]string{false: "org", true: "fleet"}[fleetScoped], func(t *testing.T) {
			sink := &captureProjectionPublisher{}
			projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
			encryptor := &mockConfidentialEncryptor{}
			publisher := NewNotificationCanonicalPublisher(projector, encryptor, nil, zap.NewNop())
			ch := &domain.NotificationChannel{ID: uuid.New(), OrgID: uuid.New(), Name: "private-oncall", ChannelType: domain.ChannelTypeWebhook, Enabled: true}
			if fleetScoped {
				ch.OrgID = uuid.Nil
			}
			require.NoError(t, publisher.PublishChannel(t.Context(), ch))
			require.Len(t, sink.events, 1)
			ev := sink.events[0]
			require.True(t, ev.CheckID())
			require.True(t, ev.VerifySignature())
			wire, err := json.Marshal(ev)
			require.NoError(t, err)
			var signed struct {
				Tags [][]string `json:"tags"`
			}
			require.NoError(t, json.Unmarshal(wire, &signed))
			allowed := map[string]bool{"d": true, "domain": true, "schema": true, "legacy_kind": true, "deleted": true, "t": true}
			for _, tag := range signed.Tags {
				require.True(t, allowed[tag[0]], "unexpected public tag %q", tag[0])
			}
			require.Equal(t, ch.ID.String(), eventDTag(ev))
			assertTag(t, ev, "t", kinds.CPStateTopicNotificationChannelRegistry)
			orgVisible, err := encryptor.DecryptConfidential(t.Context(), ev.Content, 0, "", "")
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(orgVisible, &payload))
			require.Equal(t, ch.Name, payload["name"])
			require.Equal(t, string(ch.ChannelType), payload["channel_type"])
			if !fleetScoped {
				require.Equal(t, ch.OrgID.String(), payload["org_id"])
			}
		})
	}
}

// TestNotificationPublishTombstoneEncrypted verifies that a tombstone publish
// also uses the confidential encryptor.
func TestNotificationPublishTombstoneEncrypted(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	encryptor := &mockConfidentialEncryptor{}
	publisher := NewNotificationCanonicalPublisher(projector, encryptor, nil, zap.NewNop())

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

	// Decrypt and verify tombstone.
	orgVisible, err := encryptor.DecryptConfidential(ctx, events[0].Content, 0, "", "")
	if err != nil {
		t.Fatalf("decrypt tombstone: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(orgVisible, &payload); err != nil {
		t.Fatalf("unmarshal tombstone: %v", err)
	}
	if payload["deleted"] != true {
		t.Fatal("expected deleted=true in tombstone")
	}
}
