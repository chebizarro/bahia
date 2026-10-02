package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

// AssistantSessionTopicMigration is a one-time startup migration that adds the
// single-letter "t" topic tag to legacy assistant session events published
// before bahia-irsry.43. NIP-01 relays index only single-letter tags, so the
// recovery REQ (which scopes on #t=assistant-session) misses untagged records.
//
// The migration reads from the daemon's local event store (a local query, not
// a relay REQ), selects legacy records client-side, and re-publishes each one
// with the tag added. It is idempotent: a no-op once no untagged records remain.
// No persistent flag is needed.
type AssistantSessionTopicMigration struct {
	history       repository.NostrEventRepository
	signer        nostr.Signer
	publisher     AssistantEventPublisher
	servicePubkey string
	logger        *slog.Logger
}

// AssistantSessionTopicMigrationConfig holds the wiring for the migration.
type AssistantSessionTopicMigrationConfig struct {
	// History is the daemon's local event store (projectionHistory).
	History       repository.NostrEventRepository
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
		history:       cfg.History,
		signer:        cfg.Signer,
		publisher:     cfg.Publisher,
		servicePubkey: cfg.ServicePubkey,
		logger:        logger.With("component", "assistant_session_topic_migration"),
	}
}

// Run migrates untagged assistant session events. It is safe to call on every
// startup: once all records carry the t tag, it does nothing.
func (m *AssistantSessionTopicMigration) Run(ctx context.Context) error {
	if m.history == nil || m.signer == nil || m.publisher == nil || m.servicePubkey == "" {
		m.logger.Warn("assistant session topic migration skipped: missing dependencies")
		return nil
	}

	// Query the local store for kind 30900 records by the service author with
	// the assistant session schema tag. This is a local query — no relay REQ,
	// no TagMap, no archtest issue.
	v1Records, err := m.history.FindByTag(ctx, domain.AssistantSessionTagSchema, domain.AssistantSessionSchema, []int{int(domain.KindAssistantSessionState)}, 500)
	if err != nil {
		m.logger.Warn("assistant session topic migration: v1 query failed", "error", err)
		return nil
	}
	v2Records, err := m.history.FindByTag(ctx, domain.AssistantSessionTagSchema, domain.AssistantSessionSchemaV2, []int{int(domain.KindAssistantSessionState)}, 500)
	if err != nil {
		m.logger.Warn("assistant session topic migration: v2 query failed", "error", err)
		return nil
	}

	records := append(v1Records, v2Records...)
	migrated := 0
	for _, rec := range records {
		if ctx.Err() != nil {
			return nil
		}
		if rec.PubKey != m.servicePubkey {
			continue
		}
		tags, err := parseTags(rec.Tags)
		if err != nil {
			m.logger.Warn("assistant session topic migration: parse tags failed", "event_id", rec.ID, "error", err)
			continue
		}
		if hasTopicTag(tags) {
			continue // Already migrated.
		}
		if err := m.republishWithTopic(ctx, rec, tags); err != nil {
			m.logger.Warn("assistant session topic migration: republish failed", "event_id", rec.ID, "error", err)
			continue
		}
		migrated++
	}
	m.logger.Info("assistant session topic migration completed", "records_scanned", len(records), "migrated", migrated)
	return nil
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

// parseTags decodes the JSON tags from a NostrEventRecord.
func parseTags(raw json.RawMessage) (nostr.Tags, error) {
	var tags nostr.Tags
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// republishWithTopic creates a new event with the same coordinate but adds
// the t=assistant-session tag, bumps created_at, re-signs, and publishes.
func (m *AssistantSessionTopicMigration) republishWithTopic(ctx context.Context, rec repository.NostrEventRecord, existingTags nostr.Tags) error {
	// Add the t tag to the existing tags.
	newTags := make(nostr.Tags, 0, len(existingTags)+1)
	for _, tag := range existingTags {
		newTags = append(newTags, tag)
	}
	newTags = append(newTags, nostr.Tag{"t", kinds.AssistantSessionTopic})

	// Bump created_at by 1 second so the relay replaces the old event.
	created := nostr.Timestamp(rec.CreatedAt.Unix() + 1)

	ev := nostr.Event{
		Kind:      nostr.Kind(rec.Kind),
		CreatedAt: created,
		Tags:      newTags,
		Content:   rec.Content,
	}
	if err := m.signer.SignEvent(ctx, &ev); err != nil {
		return err
	}
	_, err := m.publisher.Publish(ctx, ev)
	return err
}
