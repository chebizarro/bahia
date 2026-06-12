// Package docs provides the central documentation catalog and reader.
package docs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// NostrEventPublisher signs and publishes Nostr events.
// Satisfied by *nostrAdapter.Publisher.
type NostrEventPublisher interface {
	PublishSignedEvent(ctx context.Context, ev *nostr.Event) error
}

// NostrDocsPublisher syncs documentation topics to a Nostr relay as NIP-23
// long-form content (kind 30023) addressable events.
//
// Each documentation topic is published with a deterministic "d" tag derived
// from the topic slug (e.g. "getting-started", "features-services"). Because
// kind 30023 is an addressable event, the relay automatically replaces older
// versions when a newer event with the same (pubkey, kind, d) triple arrives.
//
// A SHA-256 content hash is included as a "content-hash" tag. On each sync the
// publisher queries the relay for existing docs events, compares hashes, and
// skips unchanged topics to avoid unnecessary relay writes.
type NostrDocsPublisher struct {
	docs      Service
	publisher NostrEventPublisher
	querier   NostrDocsQuerier
	logger    *zap.Logger
}

// NostrDocsQuerier fetches existing NIP-23 doc events from the relay.
// When nil, the publisher always publishes every topic.
type NostrDocsQuerier interface {
	// QueryDocEvents returns the latest kind-30023 events authored by pubkey.
	QueryDocEvents(ctx context.Context, pubkey string) ([]*nostr.Event, error)
}

// NewNostrDocsPublisher creates a docs publisher.
// querier is optional — when nil the publisher always publishes all topics.
func NewNostrDocsPublisher(docs Service, publisher NostrEventPublisher, querier NostrDocsQuerier, logger *zap.Logger) *NostrDocsPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NostrDocsPublisher{
		docs:      docs,
		publisher: publisher,
		querier:   querier,
		logger:    logger,
	}
}

// Name implements BackgroundRunner.
func (p *NostrDocsPublisher) Name() string { return "docs-nostr-publisher" }

// Run implements BackgroundRunner. It performs a single sync and returns.
func (p *NostrDocsPublisher) Run(ctx context.Context) error {
	if p.publisher == nil {
		p.logger.Info("docs nostr publisher disabled: no event publisher configured")
		return nil
	}
	return p.SyncToRelay(ctx)
}

// SyncToRelay reads the documentation catalog and publishes new or changed
// topics to the relay as NIP-23 long-form content events.
func (p *NostrDocsPublisher) SyncToRelay(ctx context.Context) error {
	catalog, err := p.docs.Catalog(ctx)
	if err != nil {
		return fmt.Errorf("loading docs catalog for nostr sync: %w", err)
	}
	if len(catalog) == 0 {
		p.logger.Info("no documentation topics to publish to relay")
		return nil
	}

	// Build map of existing content hashes from relay (if querier available).
	existingHashes := map[string]string{} // d-tag -> content-hash
	if p.querier != nil {
		existingHashes = p.fetchExistingHashes(ctx)
	}

	var published, skipped, failed int
	for _, topic := range catalog {
		if err := ctx.Err(); err != nil {
			return err
		}

		doc, err := p.docs.Read(ctx, topic.Topic)
		if err != nil {
			p.logger.Warn("failed to read doc for nostr publish",
				zap.String("topic", topic.Topic),
				zap.Error(err),
			)
			failed++
			continue
		}

		hash := contentHash(doc.Markdown)
		if existing, ok := existingHashes[topic.Topic]; ok && existing == hash {
			p.logger.Debug("doc unchanged, skipping publish",
				zap.String("topic", topic.Topic),
			)
			skipped++
			continue
		}

		if err := p.publishTopic(ctx, doc, hash); err != nil {
			p.logger.Warn("failed to publish doc to relay",
				zap.String("topic", topic.Topic),
				zap.Error(err),
			)
			failed++
			continue
		}
		published++
	}

	p.logger.Info("docs nostr sync complete",
		zap.Int("published", published),
		zap.Int("skipped", skipped),
		zap.Int("failed", failed),
		zap.Int("total", len(catalog)),
	)
	return nil
}

func (p *NostrDocsPublisher) publishTopic(ctx context.Context, doc Document, hash string) error {
	now := time.Now()

	ev := &nostr.Event{
		Kind:      kinds.LongFormContent,
		Content:   doc.Markdown,
		CreatedAt: nostr.Timestamp(now.Unix()),
		Tags: nostr.Tags{
			{"d", doc.Topic.Topic},
			{"title", doc.Topic.Title},
			{"t", doc.Topic.Category},
			{"t", "bahia-docs"},
			{"published_at", strconv.FormatInt(now.Unix(), 10)},
			{"summary", fmt.Sprintf("Bahia documentation: %s", doc.Topic.Title)},
			{"content-hash", hash},
		},
	}

	if err := p.publisher.PublishSignedEvent(ctx, ev); err != nil {
		return fmt.Errorf("publishing NIP-23 event for %s: %w", doc.Topic.Topic, err)
	}

	p.logger.Debug("published doc to relay",
		zap.String("topic", doc.Topic.Topic),
		zap.String("title", doc.Topic.Title),
		zap.String("content_hash", hash),
	)
	return nil
}

func (p *NostrDocsPublisher) fetchExistingHashes(ctx context.Context) map[string]string {
	hashes := make(map[string]string)
	// We need the pubkey but don't have it directly. The querier handles this.
	events, err := p.querier.QueryDocEvents(ctx, "")
	if err != nil {
		p.logger.Warn("failed to query existing doc events from relay, will publish all",
			zap.Error(err),
		)
		return hashes
	}

	for _, ev := range events {
		dTag := tagValue(ev.Tags, "d")
		hashTag := tagValue(ev.Tags, "content-hash")
		if dTag != "" && hashTag != "" {
			hashes[dTag] = hashTag
		}
	}

	p.logger.Debug("fetched existing doc hashes from relay",
		zap.Int("count", len(hashes)),
	)
	return hashes
}

// contentHash returns the hex-encoded SHA-256 hash of the content.
func contentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:])
}

// tagValue returns the first value for the given tag name, or empty string.
func tagValue(tags nostr.Tags, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
