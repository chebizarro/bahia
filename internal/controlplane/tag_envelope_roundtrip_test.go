package controlplane

import (
	"testing"

	"fiatjaf.com/nostr"
)

// TestTagEnvelope_RecipientScopedReply_FilterRoundTrip proves that an encrypted
// response envelope addressed with the shared tagRecipientPubkey ("p") tag is
// matched by the recipient-scoped subscription filter, which is built with the
// same constant. Guards producer/consumer drift of the recipient-scoping tag
// key now that both sides reference tagRecipientPubkey (bahia-s7o9).
func TestTagEnvelope_RecipientScopedReply_FilterRoundTrip(t *testing.T) {
	const servicePubkey = "service-pubkey-hex"

	// Producer-shaped envelope: reply e-tag + recipient p-tag (mirrors the
	// encrypted/ContextVM response events built in encrypted_transport.go).
	event := nostr.Event{
		Kind: KindContextVMMessage,
		Tags: nostr.Tags{
			{tagReplyEvent, "request-event-id", "", "reply"},
			{tagRecipientPubkey, servicePubkey},
			{ContextVMRoutingTag, ContextVMWireVersion},
		},
	}

	// Consumer filter: recipient-scoped (mirrors encrypted_transport.go:filter.Tags).
	filter := nostr.Filter{
		Kinds: []nostr.Kind{KindContextVMMessage},
		Tags:  nostr.TagMap{tagRecipientPubkey: []string{servicePubkey}},
	}
	if !filter.Matches(event) {
		t.Fatalf("recipient-scoped event not matched by filter; event.Tags=%v", event.Tags)
	}

	// Drift guard: an event addressed to a different recipient must NOT match.
	other := nostr.Event{
		Kind: KindContextVMMessage,
		Tags: nostr.Tags{
			{tagReplyEvent, "request-event-id", "", "reply"},
			{tagRecipientPubkey, "someone-else"},
		},
	}
	if filter.Matches(other) {
		t.Fatal("filter unexpectedly matched an event addressed to a different recipient")
	}
}
