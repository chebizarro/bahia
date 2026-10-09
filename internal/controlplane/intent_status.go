package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// IntentStatusPublisher publishes bounded kind-30315 (NIP-38 status) events
// for intent processing outcomes. The d-tag is scoped to requester and entity
// coordinate, so at most one status event exists per (requester, entity) pair.
//
// See docs/architecture/intents-and-authority.md.
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
	_ = p.publishStatus(ctx, intent, "accepted", "applied", "", nil)
}

// PublishAcceptedChecked lets request operations surface a failed result
// publication so a replay can re-emit status without repeating the mutation.
func (p *IntentStatusPublisher) PublishAcceptedChecked(ctx context.Context, intent *Intent) error {
	return p.publishStatus(ctx, intent, "accepted", "applied", "", nil)
}

// PublishPendingChecked reports durable local staging without claiming relay
// acceptance. The same request can be replayed after the run-state quorum ACK.
func (p *IntentStatusPublisher) PublishPendingChecked(ctx context.Context, intent *Intent) error {
	if p == nil || p.publish == nil || p.signer == nil {
		return fmt.Errorf("intent pending status publisher is not configured")
	}
	return p.publishStatus(ctx, intent, "pending", "relay_delivery_pending", "", nil)
}

// PublishRejection publishes a "rejected" status for an intent that failed
// authorization or validation. Only for known principals.
func (p *IntentStatusPublisher) PublishRejection(ctx context.Context, intent *Intent, reason string) {
	_ = p.publishStatus(ctx, intent, "rejected", "rejected", reason, nil)
}

// PublishRejectionChecked reports failed admission to the caller so a
// suspended request can be retried without ever manufacturing acceptance.
func (p *IntentStatusPublisher) PublishRejectionChecked(ctx context.Context, intent *Intent, reason string) error {
	if p == nil || p.publish == nil || p.signer == nil {
		return fmt.Errorf("intent rejection status publisher is not configured")
	}
	return p.publishStatus(ctx, intent, "rejected", "rejected", reason, nil)
}

// PublishConflict publishes a "conflict" status for a stale expected_updated_at.
func (p *IntentStatusPublisher) PublishConflict(ctx context.Context, intent *Intent) {
	_ = p.publishStatus(ctx, intent, "conflict", "revision_conflict", "stale expected_updated_at", nil)
}

func (p *IntentStatusPublisher) PublishConflictReason(ctx context.Context, intent *Intent, reason string) {
	_ = p.publishStatus(ctx, intent, "conflict", "conflict", reason, nil)
}

// PublishAcceptedEvaluation carries a computed decision in the same bounded
// requester/coordinate status used for ordinary intent acknowledgments.
func (p *IntentStatusPublisher) PublishAcceptedEvaluation(ctx context.Context, intent *Intent) error {
	if intent == nil || intent.Evaluation == nil {
		return fmt.Errorf("intent outcome is required")
	}
	if p == nil || p.publish == nil || p.signer == nil {
		return fmt.Errorf("intent outcome status publisher is not configured")
	}
	return p.publishStatus(ctx, intent, "accepted", "evaluated", "", intent.Evaluation)
}

func (p *IntentStatusPublisher) publishStatus(ctx context.Context, intent *Intent, status, result, reason string, evaluation *domain.PolicyEvaluation) error {
	if p.publish == nil || p.signer == nil || intent == nil {
		return nil
	}

	// Build d-tag: intent-status:<requester-pubkey>:<entity-coordinate>
	// This bounds growth to one status event per requester per entity.
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
	if evaluation != nil {
		content["evaluation"] = evaluation
	}
	if (status == "accepted" || status == "pending") && len(intent.StatusData) != 0 {
		content["data"] = intent.StatusData
	} else if (status == "accepted" || status == "pending") && len(intent.Result) != 0 {
		content["data"] = intent.Result
	} else if status == "rejected" && len(intent.StatusData) != 0 {
		// A handler that got part of the way (adoption's per-resource
		// canonical publication) reports its progress on the rejection, so a
		// partial application is visible, not silent.
		content["data"] = intent.StatusData
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		p.logger.Warn("failed to marshal intent status content", zap.Error(err))
		return err
	}

	if evaluation != nil && len(contentJSON) > 16*1024 {
		return fmt.Errorf("intent evaluation exceeds 16 KiB status limit")
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
		return err
	}

	if err := p.publish(ctx, ev); err != nil {
		p.logger.Warn("failed to publish intent status event",
			zap.String("intent_id", intent.IntentID),
			zap.Error(err),
		)
		return err
	}
	return nil
}
