package service

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// TestAssistantStatusCarriesSingleLetterTopic pins bahia-irsry.37: browsers
// REQ assistant status by #t=assistant-status (relays index single-letter
// tags only), so every published status must carry that topic, and the REQ
// shape the web store sends must match it.
func TestAssistantStatusCarriesSingleLetterTopic(t *testing.T) {
	publisher := &assistantTestPublisher{}
	signer := testAssistantSigner(t)
	status := NewAssistantStatusEventPublisher(publisher, signer, AssistantIdentity{AgentID: "assistant-test"})
	if err := status.PublishAssistantStatus(context.Background(), "session-1", "thinking", map[string]any{"run_id": "run-1"}); err != nil {
		t.Fatalf("publish status: %v", err)
	}
	published := publisher.eventsOfKind(domain.KindAssistantStatus)
	if len(published) != 1 {
		t.Fatalf("published statuses = %d, want 1", len(published))
	}
	ev := published[0]
	if !ev.Tags.ContainsAny("t", []string{kinds.AssistantStatusTopic}) {
		t.Fatalf("assistant status lacks t=%s: %v", kinds.AssistantStatusTopic, ev.Tags)
	}
	webFilter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(domain.KindAssistantStatus)}, Authors: []nostr.PubKey{ev.PubKey}, Tags: nostr.TagMap{"t": []string{kinds.AssistantStatusTopic}}}
	if !webFilter.Matches(ev) {
		t.Fatalf("assistant status does not match the #t REQ: %v", ev.Tags)
	}
}
