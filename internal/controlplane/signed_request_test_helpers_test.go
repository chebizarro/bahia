package controlplane

import (
	"testing"

	"fiatjaf.com/nostr"
)

func signedLLMRequest(t *testing.T, privateKey string, kind int, content string, tags nostr.Tags) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := event.Sign(testNostrSecretKey(t, privateKey)); err != nil {
		t.Fatalf("sign request event: %v", err)
	}
	return event
}
