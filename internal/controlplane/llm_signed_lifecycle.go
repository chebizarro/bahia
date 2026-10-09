package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"iter"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

func isLLMLifecycleIntent(intent *Intent) bool {
	if intent == nil || intent.Domain != "llm" {
		return false
	}
	switch intent.Op {
	case "deploy", "rollback", "approve", "reject":
		return true
	default:
		return false
	}
}

// validateLLMLifecycleSignedRequest binds the actor, idempotency key, target,
// and deployment selection to the exact operator-authored event. It is an
// admission check, not a provisioning receipt or permission to execute.
func validateLLMLifecycleSignedRequest(intent *Intent) error {
	if !isLLMLifecycleIntent(intent) || intent.Event == nil {
		return fmt.Errorf("LLM lifecycle request requires a signed operator event")
	}
	if err := nostradapter.ValidateInboundEvent(intent.Event, time.Now().UTC(), nostradapter.InboundEventMaxFutureSkew); err != nil {
		return fmt.Errorf("invalid signed LLM lifecycle request: %w", err)
	}
	parsed, err := ParseIntent(intent.Event)
	if err != nil || parsed.Domain != "llm" || parsed.Op != intent.Op || parsed.Schema != "bahia.intent.llm.v1" ||
		intent.Actor != intent.Event.PubKey.Hex() || intent.Schema != parsed.Schema || intent.OrgID != parsed.OrgID ||
		intent.OrgID == uuid.Nil || intent.Coordinate != parsed.Coordinate || intent.IntentID != parsed.IntentID ||
		!sameLLMRequestContent(intent.Content, parsed.Content) || !sameLLMRevision(intent.ExpectedUpdatedAt, parsed.ExpectedUpdatedAt) ||
		!llmTagExactlyOnce(intent.Event.Tags, "d", intent.Coordinate) ||
		!llmTagExactlyOnce(intent.Event.Tags, "domain", "llm") ||
		!llmTagExactlyOnce(intent.Event.Tags, "op", intent.Op) ||
		!llmTagExactlyOnce(intent.Event.Tags, "schema", "bahia.intent.llm.v1") ||
		!llmTagExactlyOnce(intent.Event.Tags, "org", intent.OrgID.String()) ||
		!llmTagExactlyOnce(intent.Event.Tags, "intent_id", intent.IntentID) ||
		!llmTagExactlyOnce(intent.Event.Tags, "t", "bahia-intent") {
		return fmt.Errorf("LLM lifecycle envelope differs from the signed operator request")
	}
	if id, err := uuid.Parse(intent.IntentID); err != nil || id.Version() != 7 {
		return fmt.Errorf("LLM lifecycle intent_id must be an author-minted UUIDv7")
	}
	allowed := map[string]bool{"intent_id": true, "expected_updated_at": true}
	switch intent.Op {
	case "deploy", "rollback":
		allowed["route_id"], allowed["environment_id"] = true, true
		if intent.Op == "deploy" {
			allowed["release_id"] = true
		}
		route, routeErr := uuid.Parse(firstIntentString(intent.Content, "route_id"))
		env, envErr := uuid.Parse(firstIntentString(intent.Content, "environment_id"))
		if routeErr != nil || envErr != nil || route == uuid.Nil || env == uuid.Nil ||
			intent.Coordinate != route.String()+":"+env.String() {
			return fmt.Errorf("LLM lifecycle coordinate does not bind route and environment")
		}
		if intent.Op == "deploy" {
			release, releaseErr := uuid.Parse(firstIntentString(intent.Content, "release_id"))
			if releaseErr != nil || release == uuid.Nil {
				return fmt.Errorf("LLM deploy requires a signed release id")
			}
		}
	case "approve", "reject":
		allowed["deployment_intent_id"] = true
		id, idErr := uuid.Parse(firstIntentString(intent.Content, "deployment_intent_id"))
		if idErr != nil || id == uuid.Nil || intent.Coordinate != id.String() {
			return fmt.Errorf("LLM decision coordinate does not bind deployment intent")
		}
	}
	if contentID, ok := intent.Content["intent_id"]; ok && contentID != intent.IntentID {
		return fmt.Errorf("LLM lifecycle content intent_id differs from signed tag")
	}
	for key := range intent.Content {
		if !allowed[key] {
			return fmt.Errorf("LLM lifecycle request contains non-request field %q", key)
		}
	}
	return nil
}

func sameLLMRequestContent(a, b map[string]any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func sameLLMRevision(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

func llmTagExactlyOnce(tags nostr.Tags, key, value string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == key {
			if len(tag) != 2 || tag[1] != value {
				return false
			}
			count++
		}
	}
	return count == 1
}

func llmRequestObserved(intent *Intent, events interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}) bool {
	if events == nil || intent == nil || intent.Event == nil {
		return false
	}
	for event := range events.QueryEvents(nostr.Filter{IDs: []nostr.ID{intent.Event.ID}, Limit: 1}) {
		return event.ID == intent.Event.ID && event.PubKey == intent.Event.PubKey &&
			event.Content == intent.Event.Content && event.CheckID() && event.VerifySignature()
	}
	return false
}
