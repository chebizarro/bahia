package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	"go.uber.org/zap"
)

const (
	testServiceKey   = "0000000000000000000000000000000000000000000000000000000000000001"
	testRequesterKey = "0000000000000000000000000000000000000000000000000000000000000002"
	testOtherKey     = "0000000000000000000000000000000000000000000000000000000000000003"
)

type mockPrivatePublisher struct {
	events []nostr.Event
}

func (m *mockPrivatePublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	m.events = append(m.events, ev)
	return 1, nil
}

func newResponder(t *testing.T, publisher *mockPrivatePublisher) *EncryptedResponder {
	t.Helper()
	signer, err := NewPrivateKeySigner(testServiceKey)
	if err != nil {
		t.Fatalf("NewPrivateKeySigner: %v", err)
	}
	return NewEncryptedResponder(publisher, signer, testServiceKey, zap.NewNop())
}

func makePrivateRequestEvent(t *testing.T, requesterKey string, envelope PrivateRequestEnvelope) *nostr.Event {
	t.Helper()
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatalf("service pubkey: %v", err)
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, requesterKey)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	ciphertext, err := nip44.Encrypt(string(content), conversationKey)
	if err != nil {
		t.Fatalf("encrypt request: %v", err)
	}
	event := &nostr.Event{
		Kind:      KindPrivateRequest,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", servicePubkey}, {"private", PrivateTransportVersion}},
		Content:   ciphertext,
	}
	if err := event.Sign(requesterKey); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	return event
}

func hasTag(tags nostr.Tags, name, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func decryptResultEnvelope(t *testing.T, ev nostr.Event, requesterKey string) PrivateResultEnvelope {
	t.Helper()
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatalf("service pubkey: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, requesterKey)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	plaintext, err := nip44.Decrypt(ev.Content, conversationKey)
	if err != nil {
		t.Fatalf("decrypt result: %v", err)
	}
	var envelope PrivateResultEnvelope
	if err := json.Unmarshal([]byte(plaintext), &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return envelope
}

func TestEncryptedResponder_DecryptRequestContentRoundTrip(t *testing.T) {
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	req := makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{
		Version:         PrivateTransportVersion,
		Operation:       "payments.history",
		RequesterPubkey: requesterPubkey,
		Payload:         json.RawMessage(`{"limit":25}`),
	})
	responder := newResponder(t, &mockPrivatePublisher{})

	plaintext, err := responder.DecryptRequestContent(req)
	if err != nil {
		t.Fatalf("DecryptRequestContent: %v", err)
	}
	var envelope PrivateRequestEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatalf("unmarshal plaintext: %v", err)
	}
	if envelope.Operation != "payments.history" || string(envelope.Payload) != `{"limit":25}` {
		t.Fatalf("unexpected envelope: %+v payload=%s", envelope, envelope.Payload)
	}
}

func TestEncryptedResponder_PublishEncryptedResultCorrelatesToRequest(t *testing.T) {
	publisher := &mockPrivatePublisher{}
	responder := newResponder(t, publisher)
	req := makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{Version: PrivateTransportVersion, Operation: "orgs.list"})

	if err := responder.PublishEncryptedResult(context.Background(), req, "ok", map[string]any{"count": 2}, nil); err != nil {
		t.Fatalf("PublishEncryptedResult: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d", len(publisher.events))
	}
	result := publisher.events[0]
	if result.Kind != KindPrivateResult {
		t.Fatalf("result kind = %d", result.Kind)
	}
	if !hasTag(result.Tags, "e", req.ID) || !hasTag(result.Tags, "p", req.PubKey) {
		t.Fatalf("missing correlation tags: %#v", result.Tags)
	}
	envelope := decryptResultEnvelope(t, result, testRequesterKey)
	if envelope.RequestEventID != req.ID || envelope.Status != "ok" {
		t.Fatalf("unexpected result envelope: %+v", envelope)
	}
}

func TestPrivateTransport_HandleEventPublishesDecryptFailure(t *testing.T) {
	publisher := &mockPrivatePublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	event := &nostr.Event{Kind: KindPrivateRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkey}, {"private", PrivateTransportVersion}}, Content: "not-valid-nip44"}
	if err := event.Sign(testRequesterKey); err != nil {
		t.Fatal(err)
	}
	transport := NewPrivateTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted error result, got %d events", len(publisher.events))
	}
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "decrypt_failed" {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
}

func TestPrivateTransport_HandleEventIgnoresUnroutedPrivateKind(t *testing.T) {
	publisher := &mockPrivatePublisher{}
	responder := newResponder(t, publisher)
	event := makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{Version: PrivateTransportVersion, Operation: "notifications.list"})
	event.Tags = nostr.Tags{{"p", "c" + event.PubKey[1:]}, {"private", PrivateTransportVersion}}
	if err := event.Sign(testRequesterKey); err != nil {
		t.Fatal(err)
	}
	transport := NewPrivateTransport(nil, responder, []string{event.PubKey}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 0 {
		t.Fatalf("unrouted private traffic should be ignored, got %d published events", len(publisher.events))
	}
}

func TestPrivateTransport_HandleEventRejectsUnauthorizedRequester(t *testing.T) {
	publisher := &mockPrivatePublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	otherPubkey, err := nostr.GetPublicKey(testOtherKey)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	event := makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{Version: PrivateTransportVersion, Operation: "notifications.list", RequesterPubkey: requesterPubkey})
	transport := NewPrivateTransport(nil, responder, []string{otherPubkey}, zap.NewNop())
	transport.RegisterHandler("notifications.list", func(context.Context, PrivateRequest) (any, error) {
		called = true
		return map[string]any{"ok": true}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if called {
		t.Fatalf("unauthorized requester should not reach handler")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted unauthorized result, got %d events", len(publisher.events))
	}
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "unauthorized" {
		t.Fatalf("unexpected unauthorized envelope: %+v", envelope)
	}
}

func TestPrivateTransport_HandleEventDispatchesAuthorizedOperation(t *testing.T) {
	publisher := &mockPrivatePublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	event := makePrivateRequestEvent(t, testRequesterKey, PrivateRequestEnvelope{Version: PrivateTransportVersion, Operation: "payments.history", RequesterPubkey: requesterPubkey, Payload: json.RawMessage(`{"limit":10}`)})
	transport := NewPrivateTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterHandler("payments.history", func(_ context.Context, request PrivateRequest) (any, error) {
		if string(request.Envelope.Payload) != `{"limit":10}` {
			t.Fatalf("handler payload = %s", request.Envelope.Payload)
		}
		return map[string]any{"records": []any{}}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted success result, got %d events", len(publisher.events))
	}
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "ok" || envelope.RequestEventID != event.ID {
		t.Fatalf("unexpected success envelope: %+v", envelope)
	}
}
