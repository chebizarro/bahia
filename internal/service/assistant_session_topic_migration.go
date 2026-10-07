package service

import (
	"context"
	"log/slog"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AssistantSessionTopicMigration is a one-time startup migration that adds the
// single-letter "t" topic tag to compatibility assistant session events published
// before. NIP-01 relays index only single-letter tags, so the
// recovery REQ (which scopes on #t=assistant-session) misses untagged records.
//
// The migration enumerates the daemon's local event store completely (a local
// query with no page limit, not a relay REQ), selects compatibility records
// client-side, and re-publishes each one with the tag added. It is idempotent:
// a no-op once no untagged records remain. No persistent flag is needed.
type AssistantSessionTopicMigration struct {
	local         SupervisionEventStore
	signer        nostr.Signer
	publisher     AssistantEventPublisher
	servicePubkey string
	logger        *slog.Logger
}

// AssistantSessionTopicMigrationConfig holds the wiring for the migration.
type AssistantSessionTopicMigrationConfig struct {
	// LocalStore is the daemon's local event store. Its QueryEvents pages to
	// completion, so every compatibility record is migrated however many there are.
	LocalStore    SupervisionEventStore
	Signer        nostr.Signer
	Publisher     AssistantEventPublisher
	ServicePubkey string
	Logger        *slog.Logger
}

// NewAssistantSessionTopicMigration returns a migration runner. If any required
// dependency is nil, Run is a silent no-op.
func NewAssistantSessionTopicMigration(cfg AssistantSessionTopicMigrationConfig) *AssistantSessionTopicMigration {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AssistantSessionTopicMigration{
		local:         cfg.LocalStore,
		signer:        cfg.Signer,
		publisher:     cfg.Publisher,
		servicePubkey: cfg.ServicePubkey,
		logger:        logger.With("component", "assistant_session_topic_migration"),
	}
}

// Run migrates untagged assistant session events. It is safe to call on every
// startup: once all records carry the t tag, it does nothing.
func (m *AssistantSessionTopicMigration) Run(ctx context.Context) error {
	if m.local == nil || m.signer == nil || m.publisher == nil || m.servicePubkey == "" {
		m.logger.Warn("assistant session topic migration skipped: missing dependencies")
		return nil
	}
	author, err := nostr.PubKeyFromHex(m.servicePubkey)
	if err != nil {
		m.logger.Warn("assistant session topic migration skipped: invalid service pubkey", "error", err)
		return nil
	}

	// Enumerate every assistant session record the daemon authored, with no
	// limit: the local store pages to completion. Schema and topic are
	// selected client-side (no multi-letter tag filter), so compatibility v1 and v2
	// records are found alike.
	scanned, migrated := 0, 0
	for ev := range m.local.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{domain.KindAssistantSessionState}, Authors: []nostr.PubKey{author}}) {
		if ctx.Err() != nil {
			return nil
		}
		schema := tagValueOf(ev.Tags, domain.AssistantSessionTagSchema)
		if schema != domain.AssistantSessionSchema && schema != domain.AssistantSessionSchemaV2 {
			continue
		}
		scanned++
		if hasTopicTag(ev.Tags) {
			continue // Already migrated.
		}
		if err := m.republishWithTopic(ctx, ev); err != nil {
			m.logger.Warn("assistant session topic migration: republish failed", "event_id", ev.ID.Hex(), "error", err)
			continue
		}
		migrated++
	}
	m.logger.Info("assistant session topic migration completed", "records_scanned", scanned, "migrated", migrated)
	return nil
}

func tagValueOf(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// hasTopicTag returns true if the tags include ["t", "assistant-session"].
func hasTopicTag(tags nostr.Tags) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "t" && tag[1] == kinds.AssistantSessionTopic {
			return true
		}
	}
	return false
}

// republishWithTopic creates a new event with the same coordinate but adds
// the t=assistant-session tag, bumps created_at, re-signs, and publishes.
func (m *AssistantSessionTopicMigration) republishWithTopic(ctx context.Context, legacy nostr.Event) error {
	// Add the t tag to the existing tags.
	newTags := make(nostr.Tags, 0, len(legacy.Tags)+1)
	newTags = append(newTags, legacy.Tags...)
	newTags = append(newTags, nostr.Tag{"t", kinds.AssistantSessionTopic})

	ev := nostr.Event{
		Kind: legacy.Kind,
		// Bump created_at by 1 second so the relay handles event.
		CreatedAt: legacy.CreatedAt + 1,
		Tags:      newTags,
		Content:   legacy.Content,
	}
	if err := m.signer.SignEvent(ctx, &ev); err != nil {
		return err
	}
	_, err := m.publisher.Publish(ctx, ev)
	return err
}
