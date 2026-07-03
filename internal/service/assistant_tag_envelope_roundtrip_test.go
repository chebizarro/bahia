package service

import (
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// TestTagEnvelope_AssistantSessionState_FilterRoundTrip proves that an assistant
// session-state event tagged with the shared AssistantSessionTagSchema key is
// matched by the session-recovery subscription filter, which is built with the
// same constant. Guards producer/consumer drift of the session schema tag key
// now that assistant_orchestrator (producer) and assistant_session_recovery
// (consumer) both reference domain.AssistantSessionTagSchema (bahia-s7o9).
func TestTagEnvelope_AssistantSessionState_FilterRoundTrip(t *testing.T) {
	// Producer-shaped event (mirrors assistant_orchestrator session-state tags).
	event := nostr.Event{
		Kind: domain.KindAssistantSessionState,
		Tags: nostr.Tags{
			{"d", domain.AssistantSessionSchema + ":session-123"},
			{domain.AssistantSessionTagSchema, domain.AssistantSessionSchema},
		},
	}

	// Consumer filter (mirrors assistant_session_recovery replay filter).
	filter := nostr.Filter{
		Kinds: []nostr.Kind{domain.KindAssistantSessionState},
		Tags:  nostr.TagMap{domain.AssistantSessionTagSchema: []string{domain.AssistantSessionSchema}},
	}
	if !filter.Matches(event) {
		t.Fatalf("session-state event not matched by recovery filter; event.Tags=%v", event.Tags)
	}

	// Drift guard: a different schema value must NOT match.
	other := nostr.Event{
		Kind: domain.KindAssistantSessionState,
		Tags: nostr.Tags{{domain.AssistantSessionTagSchema, "bahia.some-other.v1"}},
	}
	if filter.Matches(other) {
		t.Fatal("recovery filter unexpectedly matched an event with a different schema value")
	}
}
