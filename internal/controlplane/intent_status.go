package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

// IntentStatusPublisher publishes bounded kind-30315 (NIP-38 status) events
// for intent processing outcomes. The d-tag is scoped to requester and entity
// coordinate, so at most one status event exists per (requester, entity) pair.
//
// See design §3.3.
type IntentStatusPublisher struct {
	publish func(ctx context.Context, ev nostr.Event) error
	signer  nostr.Signer
	logger  *zap.Logger
	// statusExpiry is the NIP-40 expiration duration (default 7 days).
	statusExpiry time.Duration
}

// NewIntentStatusPublisher creates a status publisher. The publish function
// should route through the outbox (Publisher.PublishBeforeCommit or similar).
// signer is the daemon's Nostr signer for signing status events.
func NewIntentStatusPublisher(
	publish func(ctx context.Context, ev nostr.Event) error,
	signer nostr.Signer,
	logger *zap.Logger,
) *IntentStatusPublisher {
	return &IntentStatusPublisher{
		publish:      publish,
		signer:       signer,
		logger:       logger.Named("intent-status"),
		statusExpiry: 7 * 24 * time.Hour,
	}
}

// PublishAccepted publishes an "accepted" status for a processed intent.
func (p *IntentStatusPublisher) PublishAccepted(ctx context.Context, intent *Intent) {
	p.publishStatus(ctx, intent, "accepted", "applied", "")
}

// PublishRejection publishes a "rejected" status for an intent that failed
// authorization or validation. Only for known principals (§2.3).
func (p *IntentStatusPublisher) PublishRejection(ctx context.Context, intent *Intent, reason string) {
	p.publishStatus(ctx, intent, "rejected", "rejected", reason)
}

// PublishConflict publishes a "conflict" status for a stale expected_updated_at.
func (p *IntentStatusPublisher) PublishConflict(ctx context.Context, intent *Intent) {
	p.publishStatus(ctx, intent, "conflict", "revision_conflict", "stale expected_updated_at")
}

func (p *IntentStatusPublisher) publishStatus(ctx context.Context, intent *Intent, status, result, reason string) {
	if p.publish == nil || p.signer == nil || intent == nil {
		return
	}

	// Build d-tag: intent-status:<requester-pubkey>:<entity-coordinate>
	// This bounds growth to one status event per requester per entity (§3.3).
	dTag := fmt.Sprintf("intent-status:%s:%s", intent.Actor, intent.Coordinate)

	expiration := fmt.Sprintf("%d", time.Now().Add(p.statusExpiry).Unix())

	content := map[string]interface{}{
		"intent_id":  intent.IntentID,
		"coordinate": intent.Coordinate,
		"result":     result,
	}
	if reason != "" {
		content["reason"] = reason
	}
	if status == "accepted" && len(intent.StatusData) != 0 {
		content["data"] = intent.StatusData
	} else if status == "accepted" && len(intent.Result) != 0 {
		content["data"] = intent.Result
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		p.logger.Warn("failed to marshal intent status content", zap.Error(err))
		return
	}

	ev := nostr.Event{
		Kind:      30315,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"domain", "intent"},
			{"status", status},
			{"t", "intent-status"},
			{"p", intent.Actor},
			{"intent_id", intent.IntentID},
			{"expiration", expiration},
		},
		Content: string(contentJSON),
	}

	// Add event reference if the intent has an event.
	if intent.Event != nil {
		ev.Tags = append(ev.Tags, nostr.Tag{"e", intent.Event.ID.Hex()})
	}

	if err := p.signer.SignEvent(ctx, &ev); err != nil {
		p.logger.Warn("failed to sign intent status event",
			zap.String("intent_id", intent.IntentID),
			zap.Error(err),
		)
		return
	}

	if err := p.publish(ctx, ev); err != nil {
		p.logger.Warn("failed to publish intent status event",
			zap.String("intent_id", intent.IntentID),
			zap.Error(err),
		)
	}
}
