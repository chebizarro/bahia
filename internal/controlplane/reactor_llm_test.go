package controlplane

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAppendLLMRequestTagsAddsContentAndTagCorrelation(t *testing.T) {
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	intentID := uuid.New()
	request := &nostr.Event{
		ID:      testNostrID("llm-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, nostr.Generate().Hex()),
		Kind:    KindLLMDeployRequest,
		Content: `{"route_id":"` + routeID.String() + `","environment_id":"` + envID.String() + `","release_id":"` + releaseID.String() + `"}`,
		Tags:    nostr.Tags{{"intent", intentID.String()}},
	}
	tags := appendLLMRequestTags(nostr.Tags{{"status", "error"}}, request)
	assertReactorTag(t, tags, "route", routeID.String())
	assertReactorTag(t, tags, "environment", envID.String())
	assertReactorTag(t, tags, "release", releaseID.String())
	assertReactorTag(t, tags, "intent", intentID.String())
}

func TestLLMNostrCorrelationUsesIntentMetadata(t *testing.T) {
	intent := &domain.LLMDeploymentIntent{Metadata: map[string]any{"nostr_event_id": "event-1", "nostr_request_pubkey": "pubkey-1"}}
	eventID, pubkey := llmNostrCorrelation(intent)
	if eventID != "event-1" || pubkey != "pubkey-1" {
		t.Fatalf("unexpected correlation: event=%q pubkey=%q", eventID, pubkey)
	}
}
