package nostr

import (
	"context"
	"encoding/json"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// NotificationCanonicalPublisher publishes authoritative notification channel
// state records through the shared builder and outbox. Channel config including
// webhook URLs and credentials is published as NIP-44 self-encrypted content
// via publishConfidentialControlState — plaintext envelope tags contain only
// non-sensitive metadata (org_id, channel_type, name).
//
// Follows the BackupCanonicalPublisher pattern.
type NotificationCanonicalPublisher struct {
	projector *Projector
	logger    *zap.Logger
}

// NewNotificationCanonicalPublisher creates a publisher that delegates to the
// projector's shared signing and outbox pipeline.
func NewNotificationCanonicalPublisher(projector *Projector, logger *zap.Logger) *NotificationCanonicalPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NotificationCanonicalPublisher{
		projector: projector,
		logger:    logger.Named("notification-canonical"),
	}
}

// PublishChannel publishes a canonical notification channel config record.
// The full channel config (including webhook URLs and secrets) is included
// in the content because it is NIP-44 encrypted by publishConfidentialControlState.
func (p *NotificationCanonicalPublisher) PublishChannel(ctx context.Context, ch *domain.NotificationChannel) error {
	if p.projector == nil || !p.projector.Enabled() || ch == nil {
		return nil
	}
	dTag := NotificationChannelDTag(ch.ID)
	tags, content := NotificationChannelRegistryRecord(ch, false, true)
	return p.projector.publishConfidentialControlState(ctx, KindNotificationChannelRegistry, dTag, false, tags, content, "notification_channel.projection", &ch.ID)
}

// PublishChannelDeleted publishes a tombstone for a deleted channel.
func (p *NotificationCanonicalPublisher) PublishChannelDeleted(ctx context.Context, channelID uuid.UUID) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	dTag := NotificationChannelDTag(channelID)
	contentJSON, _ := json.Marshal(map[string]any{"id": channelID.String(), "deleted": true})
	content := string(contentJSON)
	return p.projector.publishConfidentialControlState(ctx, KindNotificationChannelRegistry, dTag, true, nil, content, "notification_channel.projection", &channelID)
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

	content, _ := json.Marshal(payload)
	return tags, string(content)
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
