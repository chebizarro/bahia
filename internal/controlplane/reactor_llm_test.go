package controlplane

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestLLMNostrCorrelationUsesIntentMetadata(t *testing.T) {
	intent := &domain.LLMDeploymentIntent{Metadata: map[string]any{"nostr_event_id": "event-1", "nostr_request_pubkey": "pubkey-1"}}
	eventID, pubkey := llmNostrCorrelation(intent)
	if eventID != "event-1" || pubkey != "pubkey-1" {
		t.Fatalf("unexpected correlation: event=%q pubkey=%q", eventID, pubkey)
	}
}

func TestLLMProvisioningIntentMetadataUsesSignedIntentCorrelation(t *testing.T) {
	event := &nostr.Event{ID: nostr.ID{1}, PubKey: nostr.PubKey{2}}
	intent := &Intent{
		Event: event,
		Actor: event.PubKey.Hex(),
		Content: map[string]interface{}{"metadata": map[string]any{
			"source":               "operator",
			"nostr_event_id":       "spoofed",
			"nostr_request_pubkey": "spoofed",
		}},
	}
	metadata := llmProvisioningIntentMetadata(intent)
	if metadata["source"] != "operator" || metadata["nostr_event_id"] != event.ID.Hex() || metadata["nostr_request_pubkey"] != event.PubKey.Hex() {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	deployment := &domain.LLMDeploymentIntent{Metadata: metadata}
	if eventID, pubkey := llmNostrCorrelation(deployment); eventID != event.ID.Hex() || pubkey != event.PubKey.Hex() {
		t.Fatalf("unexpected correlation: event=%q pubkey=%q", eventID, pubkey)
	}
}
