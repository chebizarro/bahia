package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"go.uber.org/zap"
)

// TestContextVMResponseTooLargeToStoreUsesEphemeralWrap: a reply to a stored
// 1059 request stays 1059 while Bahia's relay can store it, and becomes an
// ephemeral 21059 wrap (relayed, not refused) when its encrypted content would
// exceed maxStoredGiftWrapContentBytes, e.g. run logs or security findings of
// 41-64 KB. Requesters (web and pkg/client) subscribe to both wrap kinds.
func TestContextVMResponseTooLargeToStoreUsesEphemeralWrap(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, tc := range []struct {
		name    string
		payload int
		want    nostr.Kind
	}{
		{name: "fits a stored wrap", payload: 30_000, want: KindContextVMGiftWrap},
		{name: "too large to store", payload: 45_000, want: KindContextVMEphemeralWrap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
			logs := strings.Repeat("l", tc.payload)
			transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
				return map[string]any{"logs": logs}, nil
			})
			inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"logs","method":"backup/run","params":{"_meta":{"progressToken":"logs-1"}}}`)
			outer := wrapContextVMEvent(t, inner, KindContextVMGiftWrap)

			transport.HandleEvent(context.Background(), outer)

			if len(publisher.events) != 2 {
				t.Fatalf("published %d events, want progress ack plus response", len(publisher.events))
			}
			if ack := publisher.events[0]; ack.Kind != KindContextVMGiftWrap {
				t.Fatalf("small progress ack kind = %d, want the request's 1059", ack.Kind)
			}
			response := publisher.events[1]
			if response.Kind != tc.want {
				t.Fatalf("response kind = %d (content %d bytes), want %d", response.Kind, len(response.Content), tc.want)
			}
			if response.Kind == KindContextVMGiftWrap && len(response.Content) > maxStoredGiftWrapContentBytes {
				t.Fatalf("stored wrap content %d bytes exceeds %d", len(response.Content), maxStoredGiftWrapContentBytes)
			}
			if !response.VerifySignature() || !hasTag(response.Tags, "e", outer.ID.Hex()) || !hasTag(response.Tags, "p", inner.PubKey.Hex()) {
				t.Fatalf("response wrapper not signed/correlated: %#v", response.Tags)
			}
			rpc := contextVMResponse(t, unwrapContextVMResponseEvent(t, response, testRequesterKey))
			if rpc.Error != nil || !strings.Contains(string(encodeForTest(t, rpc.Result)), logs) {
				t.Fatalf("response did not carry the full payload: error=%v", rpc.Error)
			}
		})
	}
}

// TestContextVMTransportAnswersNIP59EphemeralWrapRequests: pkg/client sends a
// NIP-59 request too large for a stored 1059 as a 21059 wrap of the same
// rumor. The daemon unwraps it like a 1059 and answers on 21059.
func TestContextVMTransportAnswersNIP59EphemeralWrapRequests(t *testing.T) {
	ctx := context.Background()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	var got ContextVMRequest
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(_ context.Context, request ContextVMRequest) (any, error) {
		got = request
		return map[string]any{"accepted": true}, nil
	})
	requester := keyer.NewPlainKeySigner(testNostrSecretKey(t, testRequesterKey))
	inner := &nostr.Event{
		Kind:      KindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", responder.ServicePubkey()}, {"method", ContextVMMethodBackupRun}},
		Content:   `{"jsonrpc":"2.0","id":"big","method":"backup/run","params":{"note":"` + strings.Repeat("n", 40_000) + `"}}`,
	}
	outer, rumor, err := cascontextvm.WrapEventNIP59(ctx, requester, responder.ServicePubkey(), inner, cascontextvm.EphemeralGiftWrap)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	transport.HandleEvent(ctx, outer)

	if got.Event == nil || got.Event.ID != rumor.ID || got.Event.PubKey.Hex() != requesterPubkey {
		t.Fatalf("handler did not receive the NIP-59 21059 request: %+v", got.Event)
	}
	if len(publisher.events) != 2 || publisher.events[1].Kind != KindContextVMEphemeralWrap || !hasTag(publisher.events[1].Tags, "e", outer.ID.Hex()) {
		t.Fatalf("expected a 21059 reply correlated to the request wrap, got %d events", len(publisher.events))
	}
}

func encodeForTest(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
