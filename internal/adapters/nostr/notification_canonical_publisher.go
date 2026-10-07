package nostr

import (
	"context"
	"encoding/json"
	"fmt"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// NotificationOrgResolver resolves the org ID for notification channels.
type NotificationOrgResolver interface {
	ChannelOrgID(ctx context.Context, channelID uuid.UUID) (uuid.UUID, error)
}

// NotificationCanonicalPublisher publishes authoritative notification channel
// state records through the shared builder and outbox.
//
// Phase 3 C1: uses the unified confidential encryption path with per-org
// content key. Channel metadata (name, type, event filter, enabled status) is
// in the org-visible AEAD layer so members can see channel configuration.
// Sensitive config (webhook URLs, secrets, credentials) is in the service_inner
// NIP-44 layer, readable only by the daemon.
type NotificationCanonicalPublisher struct {
	projector   *Projector
	encryptor   ConfidentialStateEncryptor
	orgResolver NotificationOrgResolver
	logger      *zap.Logger
}

// NewNotificationCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewNotificationCanonicalPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, orgResolver NotificationOrgResolver, logger *zap.Logger) *NotificationCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NotificationCanonicalPublisher{
		projector:   projector,
		encryptor:   encryptor,
		orgResolver: orgResolver,
		logger:      logger.Named("notification-canonical"),
	}
}

// PublishChannel publishes a canonical notification channel config record.
// For org-scoped channels: metadata is in the org-visible AEAD layer; full
// config (including webhook URLs and secrets) is in the service_inner NIP-44
// layer.
// For fleet-scoped channels (OrgID is nil): the entire content is encrypted
// service-only (NIP-44 to service pubkey) since there is no member set to
// distribute an OCK to. Fleet-scoped channels are managed through the daemon
// API, not relay discovery.
func (p *NotificationCanonicalPublisher) PublishChannel(ctx context.Context, ch *domain.NotificationChannel) error {
	if p.projector == nil || !p.projector.Enabled() || ch == nil {
		return nil
	}
	dTag := NotificationChannelDTag(ch.ID)

	// Fleet-scoped channel (no org): encrypt everything service-only.
	if ch.OrgID == uuid.Nil {
		return p.publishFleetScopedChannel(ctx, ch, dTag)
	}

	// Org-visible: sanitized metadata (no secrets/webhook URLs).
	tags, orgVisibleContent := NotificationChannelRegistryRecord(ch, false, false)

	// Service-only: full config including credentials.
	_, serviceContent := NotificationChannelRegistryRecord(ch, false, true)

	return p.publishConfidentialWithServiceInner(ctx, KindNotificationChannelRegistry, dTag, false, tags, orgVisibleContent, []byte(serviceContent), "notification_channel.projection", &ch.ID, ch.OrgID.String())
}

// publishFleetScopedChannel publishes a fleet-scoped notification channel
// with the entire content as service_inner (NIP-44 to service pubkey).
// No org-visible AEAD layer since there is no org member set. The
// org-visible portion is a minimal non-sensitive envelope.
func (p *NotificationCanonicalPublisher) publishFleetScopedChannel(ctx context.Context, ch *domain.NotificationChannel, dTag string) error {
	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of fleet channel")
	}

	topic := ""
	if fam, ok := cpStateFamilies[KindNotificationChannelRegistry]; ok {
		topic = fam.topic
	}

	// Org-visible: minimal non-sensitive metadata only.
	orgVisible := map[string]any{
		"id":           ch.ID.String(),
		"name":         ch.Name,
		"channel_type": string(ch.ChannelType),
		"enabled":      ch.Enabled,
		"fleet_scoped": true,
	}
	orgVisibleJSON, _ := json.Marshal(orgVisible)

	// Service-only: full config (reconstructed from confidential record).
	_, serviceContent := NotificationChannelRegistryRecord(ch, false, true)

	// Use a synthetic fleet org key scope. The EncryptConfidential call will
	// create/use an OCK for the "fleet" scope. The service_inner remains
	// service-only via NIP-44.
	encrypted, err := p.encryptor.EncryptConfidential(ctx, "fleet", orgVisibleJSON, KindNotificationChannelRegistry, dTag, topic, []byte(serviceContent))
	if err != nil {
		return fmt.Errorf("encrypt fleet notification channel: %w", err)
	}

	tags := gonostr.Tags{
		{"channel_type", string(ch.ChannelType)},
		{"name", ch.Name},
	}

	return p.projector.publishControlState(ctx, KindNotificationChannelRegistry, dTag, false, tags, encrypted, "notification_channel.projection", &ch.ID)
}

// PublishChannelDeleted publishes a tombstone for a deleted channel.
func (p *NotificationCanonicalPublisher) PublishChannelDeleted(ctx context.Context, channelID uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	orgID := uuid.Nil
	if p.orgResolver != nil {
		var err error
		orgID, err = p.orgResolver.ChannelOrgID(ctx, channelID)
		if err != nil {
			return fmt.Errorf("resolve org for notification channel %s: %w", channelID, err)
		}
	}
	dTag := NotificationChannelDTag(channelID)
	contentJSON, _ := json.Marshal(map[string]any{"id": channelID.String(), "deleted": true})
	content := string(contentJSON)

	return p.publishConfidentialWithServiceInner(ctx, KindNotificationChannelRegistry, dTag, true, nil, content, nil, "notification_channel.projection", &channelID, orgID.String())
}

func (p *NotificationCanonicalPublisher) publishConfidentialWithServiceInner(ctx context.Context, legacyKind int, dTag string, deleted bool, extraTags gonostr.Tags, content string, serviceOnlyPlaintext []byte, entityType string, entityID *uuid.UUID, orgID string) error {
	topic := ""
	if fam, ok := cpStateFamilies[legacyKind]; ok {
		topic = fam.topic
	}

	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish")
	}

	encrypted, err := p.encryptor.EncryptConfidential(ctx, orgID, []byte(content), legacyKind, dTag, topic, serviceOnlyPlaintext)
	if err != nil {
		return fmt.Errorf("encrypt notification state: %w", err)
	}

	return p.projector.publishControlState(ctx, legacyKind, dTag, deleted, extraTags, encrypted, entityType, entityID)
}

// NotificationChannelDTag returns the d-tag for a notification channel.
func NotificationChannelDTag(id uuid.UUID) string {
	return id.String()
}

// NotificationChannelRegistryRecord builds the tags and content for a
// notification channel registry canonical state record. When confidential is
// true the full config is included (the caller encrypts); when false, webhook
// URLs and secrets are redacted for any non-encrypted context.
func NotificationChannelRegistryRecord(ch *domain.NotificationChannel, deleted bool, confidential bool) (gonostr.Tags, string) {
	tags := gonostr.Tags{
		{"org_id", ch.OrgID.String()},
		{"channel_type", string(ch.ChannelType)},
		{"name", ch.Name},
	}

	// When publishing confidentially (NIP-44 encrypted), include the full config
	// so the daemon can reconstitute the channel from the relay copy. When not
	// confidential, sanitize to strip secrets and webhook URLs.
	var publishedConfig map[string]any
	if confidential {
		publishedConfig = ch.Config
	} else {
		publishedConfig = sanitizeNotificationConfig(ch.Config, ch.ChannelType)
	}

	payload := map[string]any{
		"id":           ch.ID.String(),
		"org_id":       ch.OrgID.String(),
		"name":         ch.Name,
		"channel_type": string(ch.ChannelType),
		"config":       publishedConfig,
		"event_filter": ch.EventFilter,
		"enabled":      ch.Enabled,
		"deleted":      deleted,
	}
	if !ch.CreatedAt.IsZero() {
		payload["created_at"] = ch.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !ch.UpdatedAt.IsZero() {
		payload["updated_at"] = ch.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}

	contentJSON, _ := json.Marshal(payload)
	return tags, string(contentJSON)
}

// sanitizeNotificationConfig strips sensitive fields from the notification
// channel config before publishing to the relay. Webhook URLs and signing
// secrets must never appear in plaintext on any relay event.
func sanitizeNotificationConfig(config map[string]any, channelType domain.ChannelType) map[string]any {
	if config == nil {
		return nil
	}
	sanitized := make(map[string]any, len(config))
	for k, v := range config {
		sanitized[k] = v
	}
	// Always strip secrets.
	delete(sanitized, "secret")
	delete(sanitized, "signing_secret")
	delete(sanitized, "api_key")
	delete(sanitized, "token")
	delete(sanitized, "password")

	// For webhooks, redact the URL to domain-only.
	if channelType == domain.ChannelTypeWebhook {
		if url, ok := sanitized["url"].(string); ok && url != "" {
			sanitized["url_redacted"] = true
			delete(sanitized, "url")
		}
	}

	// For Nostr DM, the pubkey is safe to publish.
	return sanitized
}
