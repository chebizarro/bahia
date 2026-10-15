package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
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
	statusExpiry        time.Duration
	backupRunStatusGate func(string, string, nostr.Event, func() error) error
}

// SetBackupRunStatusGate serializes generic backup status publication with
// run admission and reserves the signed timestamp before relay I/O.
func (p *IntentStatusPublisher) SetBackupRunStatusGate(gate func(string, string, nostr.Event, func() error) error) {
	if p != nil {
		p.backupRunStatusGate = gate
	}
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

// PublishSupersededChecked reports that a newer trusted hostname intent won.
func (p *IntentStatusPublisher) PublishSupersededChecked(ctx context.Context, intent *Intent, reason string) error {
	if p == nil || p.publish == nil || p.signer == nil {
		return fmt.Errorf("intent status publisher is not configured")
	}
	return p.publishStatus(ctx, intent, "superseded", "superseded", reason, nil)
}

// PublishConflict publishes a "conflict" status for a stale expected_updated_at.
func (p *IntentStatusPublisher) PublishConflict(ctx context.Context, intent *Intent) {
	_ = p.publishStatus(ctx, intent, "conflict", "revision_conflict", "stale expected_updated_at", nil)
}

func (p *IntentStatusPublisher) PublishConflictReason(ctx context.Context, intent *Intent, reason string) {
	_ = p.publishStatus(ctx, intent, "conflict", "conflict", reason, nil)
}

func (p *IntentStatusPublisher) PublishConflictChecked(ctx context.Context, intent *Intent, reason string) error {
	if p == nil || p.publish == nil || p.signer == nil {
		return fmt.Errorf("intent conflict status publisher is not configured")
	}
	return p.publishStatus(ctx, intent, "conflict", "revision_conflict", reason, nil)
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
	if p == nil || p.publish == nil || p.signer == nil || intent == nil {
		return nil
	}
	ev, err := p.buildStatusEvent(ctx, intent, status, result, reason, evaluation)
	if err != nil {
		return err
	}
	publish := func() error {
		if err := p.publish(ctx, ev); err != nil {
			p.logger.Warn("failed to publish intent status event",
				zap.String("intent_id", intent.IntentID),
				zap.Error(err),
			)
			return err
		}
		return nil
	}
	if strings.HasPrefix(intent.Coordinate, "backup-run:") {
		if p.backupRunStatusGate == nil {
			return fmt.Errorf("backup run status gate is unavailable")
		}
		if intent.Event == nil || intent.Event.ID == nostr.ZeroID {
			return fmt.Errorf("backup run status requires the signed request event id")
		}
		return p.backupRunStatusGate(intent.Coordinate, intent.Event.ID.Hex(), ev, publish)
	}
	return publish()
}

func (p *IntentStatusPublisher) buildStatusEvent(ctx context.Context, intent *Intent, status, result, reason string, evaluation *domain.PolicyEvaluation) (nostr.Event, error) {
	expiry := p.statusExpiry
	if status == "accepted" && isLLMLifecycleIntent(intent) {
		// Recovery rechecks the exact accepted event after restart. A NIP-40
		// expiry would erase its local/relay authority while its effect receipt
		// could still be nonterminal. The d coordinate remains replaceable.
		expiry = 0
	}
	return p.buildStatusEventWithExpiry(ctx, intent, status, result, reason, evaluation, expiry)
}

func (p *IntentStatusPublisher) buildStatusEventWithExpiry(ctx context.Context, intent *Intent, status, result, reason string, evaluation *domain.PolicyEvaluation, expiry time.Duration) (nostr.Event, error) {
	return p.buildStatusEventWithExpiryAt(ctx, intent, status, result, reason, evaluation, expiry, nostr.Now())
}

func (p *IntentStatusPublisher) buildStatusEventWithExpiryAt(ctx context.Context, intent *Intent, status, result, reason string, evaluation *domain.PolicyEvaluation, expiry time.Duration, createdAt nostr.Timestamp) (nostr.Event, error) {
	if p == nil || p.signer == nil || intent == nil {
		return nostr.Event{}, fmt.Errorf("intent status signer is not configured")
	}

	// Ordinarily the d-tag is bounded to one status per requester and entity.
	// Confidential refusals use request correlation instead of the target.
	privateStatus := intent.Domain == "secret" || (status == "rejected" || status == "conflict") && confidentialTenantStatusDomain(intent.Domain)
	dTag := kinds.IntentStatusCoordinate(intent.Actor, intent.Coordinate)
	if privateStatus {
		// The target coordinate can identify confidential resources.
		correlation := intent.IntentID
		if correlation == "" && intent.Event != nil {
			correlation = intent.Event.ID.Hex()
		}
		if correlation == "" {
			return nostr.Event{}, fmt.Errorf("confidential status requires request correlation")
		}
		dTag = kinds.IntentPrivateStatusCoordinate(intent.Actor, intent.Domain, correlation)
	}
	if intent.Domain == "secret" {
		if status == "conflict" {
			reason = "revision_conflict"
		} else {
			switch reason {
			case "invalid_request", "revision_conflict", "store_unavailable", "processing_failed":
			default:
				if reason != "" {
					reason = "invalid_request"
				}
			}
		}
	}
	if status == "rejected" && confidentialTenantStatusDomain(intent.Domain) && reason != "" {
		reason = "access_denied"
	}
	if status == "conflict" && confidentialTenantStatusDomain(intent.Domain) && reason != "" {
		reason = "revision_conflict"
	}

	content := map[string]interface{}{
		"intent_id": intent.IntentID,
		"result":    result,
	}
	if !privateStatus {
		content["coordinate"] = intent.Coordinate
	}
	if reason != "" {
		content["reason"] = reason
	}
	if evaluation != nil {
		content["evaluation"] = evaluation
	}
	// Secret handler output is never a public status payload.
	if intent.Domain != "secret" && (status == "accepted" || status == "pending") && len(intent.StatusData) != 0 {
		content["data"] = intent.StatusData
	} else if intent.Domain != "secret" && (status == "accepted" || status == "pending") && len(intent.Result) != 0 {
		content["data"] = intent.Result
	} else if intent.Domain != "secret" && !confidentialTenantStatusDomain(intent.Domain) && status == "rejected" && len(intent.StatusData) != 0 {
		// A handler that got part of the way (adoption's per-resource
		// canonical publication) reports its progress on the rejection, so a
		// partial application is visible, not silent.
		content["data"] = intent.StatusData
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		p.logger.Warn("failed to marshal intent status content", zap.Error(err))
		return nostr.Event{}, err
	}

	if evaluation != nil && len(contentJSON) > 16*1024 {
		return nostr.Event{}, fmt.Errorf("intent evaluation exceeds 16 KiB status limit")
	}

	ev := nostr.Event{
		Kind:      30315,
		CreatedAt: createdAt,
		Tags: nostr.Tags{
			{"d", dTag},
			{"domain", "intent"},
			{"status", status},
			{"t", kinds.IntentStatusTopic},
			{"p", intent.Actor},
			{"intent_id", intent.IntentID},
		},
		Content: string(contentJSON),
	}
	if intent.Domain == "llm" {
		ev.Tags = append(ev.Tags, nostr.Tag{"workflow", "llm"})
	}
	if expiry > 0 {
		ev.Tags = append(ev.Tags, nostr.Tag{"expiration", fmt.Sprintf("%d", time.Now().Add(expiry).Unix())})
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
		return nostr.Event{}, err
	}
	return ev, nil
}

func confidentialTenantStatusDomain(domain string) bool {
	switch domain {
	case "backup", "dns", "worker", "loom", "notification", "sbom", "security", "relay":
		return true
	}
	return false
}
