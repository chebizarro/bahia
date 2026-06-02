package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

const (
	testServiceKey   = "0000000000000000000000000000000000000000000000000000000000000001"
	testRequesterKey = "0000000000000000000000000000000000000000000000000000000000000002"
	testOtherKey     = "0000000000000000000000000000000000000000000000000000000000000003"
)

type mockEncryptedPublisher struct {
	events []nostr.Event
}

func (m *mockEncryptedPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	m.events = append(m.events, ev)
	return 1, nil
}

func newResponder(t *testing.T, publisher *mockEncryptedPublisher) *EncryptedResponder {
	t.Helper()
	signer, err := NewPrivateKeySigner(testServiceKey)
	if err != nil {
		t.Fatalf("NewPrivateKeySigner: %v", err)
	}
	return NewEncryptedResponder(publisher, signer, testServiceKey, zap.NewNop())
}

func makeEncryptedRequestEvent(t *testing.T, requesterKey string, envelope EncryptedRequestEnvelope) *nostr.Event {
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
		Kind:      KindEncryptedRequest,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", servicePubkey}, {EncryptedRequestRoutingTag, EncryptedRequestWireVersion}},
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

func decryptResultEnvelope(t *testing.T, ev nostr.Event, requesterKey string) EncryptedResultEnvelope {
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
	var envelope EncryptedResultEnvelope
	if err := json.Unmarshal([]byte(plaintext), &envelope); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return envelope
}

func makeContextVMEvent(t *testing.T, requesterKey, content string) *nostr.Event {
	t.Helper()
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatalf("service pubkey: %v", err)
	}
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkey}}, Content: content}
	if err := event.Sign(requesterKey); err != nil {
		t.Fatalf("sign ContextVM event: %v", err)
	}
	return event
}

func contextVMResponse(t *testing.T, ev nostr.Event) ContextVMJSONRPCResponse {
	t.Helper()
	var response ContextVMJSONRPCResponse
	if err := json.Unmarshal([]byte(ev.Content), &response); err != nil {
		t.Fatalf("unmarshal ContextVM response: %v content=%s", err, ev.Content)
	}
	return response
}

func wrapContextVMEvent(t *testing.T, inner *nostr.Event, requesterKey string, kind int) *nostr.Event {
	t.Helper()
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatalf("service pubkey: %v", err)
	}
	content, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner ContextVM event: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, requesterKey)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	ciphertext, err := nip44.Encrypt(string(content), conversationKey)
	if err != nil {
		t.Fatalf("encrypt ContextVM event: %v", err)
	}
	outer := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkey}}, Content: ciphertext}
	if err := outer.Sign(requesterKey); err != nil {
		t.Fatalf("sign ContextVM wrapper: %v", err)
	}
	return outer
}

func unwrapContextVMResponse(t *testing.T, ev nostr.Event, requesterKey string) ContextVMJSONRPCResponse {
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
		t.Fatalf("decrypt ContextVM response: %v", err)
	}
	var inner nostr.Event
	if err := json.Unmarshal([]byte(plaintext), &inner); err != nil {
		t.Fatalf("unmarshal inner response event: %v", err)
	}
	return contextVMResponse(t, inner)
}

func TestEncryptedResponder_DecryptRequestContentRoundTrip(t *testing.T) {
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	req := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{
		Version:         EncryptedRequestWireVersion,
		Operation:       "payments.history",
		RequesterPubkey: requesterPubkey,
		Payload:         json.RawMessage(`{"limit":25}`),
	})
	responder := newResponder(t, &mockEncryptedPublisher{})

	plaintext, err := responder.DecryptRequestContent(req)
	if err != nil {
		t.Fatalf("DecryptRequestContent: %v", err)
	}
	var envelope EncryptedRequestEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatalf("unmarshal plaintext: %v", err)
	}
	if envelope.Operation != "payments.history" || string(envelope.Payload) != `{"limit":25}` {
		t.Fatalf("unexpected envelope: %+v payload=%s", envelope, envelope.Payload)
	}
}

func TestEncryptedResponder_PublishEncryptedResultCorrelatesToRequest(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	req := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: "orgs.list"})

	if err := responder.PublishEncryptedResult(context.Background(), req, "ok", map[string]any{"count": 2}, nil); err != nil {
		t.Fatalf("PublishEncryptedResult: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d", len(publisher.events))
	}
	result := publisher.events[0]
	if result.Kind != KindEncryptedResult {
		t.Fatalf("result kind = %d", result.Kind)
	}
	if !hasTag(result.Tags, "e", req.ID) || !hasTag(result.Tags, "p", req.PubKey) {
		t.Fatalf("missing correlation tags: %#v", result.Tags)
	}
	if !hasTag(result.Tags, EncryptedRequestRoutingTag, EncryptedRequestWireVersion) {
		t.Fatalf("missing encrypted routing tag: %#v", result.Tags)
	}
	envelope := decryptResultEnvelope(t, result, testRequesterKey)
	if envelope.RequestEventID != req.ID || envelope.Status != "ok" {
		t.Fatalf("unexpected result envelope: %+v", envelope)
	}
}

func TestEncryptedRequestTransport_HandleEventPublishesDecryptFailure(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubkey, err := nostr.GetPublicKey(testServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	event := &nostr.Event{Kind: KindEncryptedRequest, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkey}, {EncryptedRequestRoutingTag, EncryptedRequestWireVersion}}, Content: "not-valid-nip44"}
	if err := event.Sign(testRequesterKey); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted error result, got %d events", len(publisher.events))
	}
	envelope := decryptResultEnvelope(t, publisher.events[0], testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "decrypt_failed" {
		t.Fatalf("unexpected error envelope: %+v", envelope)
	}
}

func TestEncryptedRequestTransport_HandleEventRejectsInvalidTimestamp(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	event := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: "orgs.list", RequesterPubkey: requesterPubkey})
	event.CreatedAt = nostr.Timestamp(time.Now().Add(nostrpool.InboundEventMaxFutureSkew + time.Minute).Unix())
	if err := event.Sign(testRequesterKey); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterHandler("orgs.list", func(context.Context, EncryptedRequest) (any, error) {
		called = true
		return map[string]any{"orgs": []any{}}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if called {
		t.Fatalf("invalid timestamp request should not reach handler")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("invalid trust-boundary events should be dropped without encrypted result, got %d", len(publisher.events))
	}
}

func TestEncryptedRequestTransport_HandleEventIgnoresUnroutedEncryptedKind(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	event := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: "notifications.list"})
	event.Tags = nostr.Tags{{"p", "c" + event.PubKey[1:]}, {EncryptedRequestRoutingTag, EncryptedRequestWireVersion}}
	if err := event.Sign(testRequesterKey); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{event.PubKey}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 0 {
		t.Fatalf("unrouted encrypted traffic should be ignored, got %d published events", len(publisher.events))
	}
}

func TestEncryptedRequestTransport_HandleEventRejectsUnauthorizedRequester(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
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
	event := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: "notifications.list", RequesterPubkey: requesterPubkey})
	transport := NewEncryptedRequestTransport(nil, responder, []string{otherPubkey}, zap.NewNop())
	transport.RegisterHandler("notifications.list", func(context.Context, EncryptedRequest) (any, error) {
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

func TestEncryptedRequestTransport_HandleEventPublishesHandlerFailure(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	event := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: EncryptedOperationNotificationChannelsCreate, RequesterPubkey: requesterPubkey, Payload: json.RawMessage(`{"name":"Ops Webhook"}`)})
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterHandler(EncryptedOperationNotificationChannelsCreate, func(context.Context, EncryptedRequest) (any, error) {
		return nil, fmt.Errorf("failed to create notification channel")
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted handler error result, got %d events", len(publisher.events))
	}
	result := publisher.events[0]
	if result.Kind != KindEncryptedResult || !hasTag(result.Tags, "e", event.ID) || !hasTag(result.Tags, "p", event.PubKey) {
		t.Fatalf("unexpected result event: kind=%d tags=%#v", result.Kind, result.Tags)
	}
	envelope := decryptResultEnvelope(t, result, testRequesterKey)
	if envelope.Status != "error" || envelope.Error == nil || envelope.Error.Code != "handler_failed" || envelope.Error.Message != "failed to create notification channel" {
		t.Fatalf("unexpected handler failure envelope: %+v", envelope)
	}
}

func TestEncryptedRequestTransport_HandleEventDispatchesAuthorizedOperation(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	event := makeEncryptedRequestEvent(t, testRequesterKey, EncryptedRequestEnvelope{Version: EncryptedRequestWireVersion, Operation: "payments.history", RequesterPubkey: requesterPubkey, Payload: json.RawMessage(`{"limit":10}`)})
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterHandler("payments.history", func(_ context.Context, request EncryptedRequest) (any, error) {
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

func TestContextVMTransport_DispatchesJSONRPCRequest(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, func(_ context.Context, request ContextVMRequest) (any, error) {
		if request.ProgressToken != "deploy-1" {
			t.Fatalf("progress token = %q", request.ProgressToken)
		}
		return map[string]any{"accepted": true, "method": request.RPC.Method}, nil
	})
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":7,"method":"service/deploy","params":{"service_id":"svc","_meta":{"progressToken":"deploy-1"}}}`)

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected ContextVM response, got %d events", len(publisher.events))
	}
	responseEvent := publisher.events[0]
	if responseEvent.Kind != KindContextVMMessage || !hasTag(responseEvent.Tags, "e", event.ID) || !hasTag(responseEvent.Tags, "p", event.PubKey) {
		t.Fatalf("unexpected ContextVM response event: kind=%d tags=%#v", responseEvent.Kind, responseEvent.Tags)
	}
	response := contextVMResponse(t, responseEvent)
	if string(response.ID) != "7" || response.Error != nil {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestContextVMTransport_JSONRPCParseAndMethodErrors(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	parseError := makeContextVMEvent(t, testRequesterKey, `{not-json`)
	unknownMethod := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"abc","method":"dns/zone-create","params":{"_meta":{"progressToken":"dns-1"}}}`)

	transport.HandleEvent(context.Background(), parseError)
	transport.HandleEvent(context.Background(), unknownMethod)

	if len(publisher.events) != 2 {
		t.Fatalf("expected two error responses, got %d", len(publisher.events))
	}
	if got := contextVMResponse(t, publisher.events[0]); got.Error == nil || got.Error.Code != -32700 {
		t.Fatalf("parse error response = %+v", got)
	}
	if got := contextVMResponse(t, publisher.events[1]); got.Error == nil || got.Error.Code != -32601 || string(got.ID) != `"abc"` {
		t.Fatalf("method error response = %+v", got)
	}
}

func TestContextVMTransport_AuthorizationRejectsBeforeDispatch(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	otherPubkey, err := nostr.GetPublicKey(testOtherKey)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{otherPubkey}, zap.NewNop())
	called := false
	transport.RegisterContextVMHandler(ContextVMMethodWorkerCordon, func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return nil, nil
	})
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":1,"method":"worker/cordon","params":{"_meta":{"progressToken":"cordon-1"}}}`)

	transport.HandleEvent(context.Background(), event)

	if called {
		t.Fatalf("unauthorized ContextVM request reached handler")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected unauthorized response, got %d", len(publisher.events))
	}
	if got := contextVMResponse(t, publisher.events[0]); got.Error == nil || got.Error.Code != -32001 {
		t.Fatalf("unauthorized response = %+v", got)
	}
}

func TestContextVMTransport_IdempotencyCachesProgressToken(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	calls := 0
	transport.RegisterContextVMHandler(ContextVMMethodPackagePromote, func(context.Context, ContextVMRequest) (any, error) {
		calls++
		return map[string]any{"call": calls}, nil
	})
	first := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":1,"method":"package/promote","params":{"_meta":{"progressToken":"promote-1"}}}`)
	second := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":2,"method":"package/promote","params":{"_meta":{"progressToken":"promote-1"}}}`)

	transport.HandleEvent(context.Background(), first)
	transport.HandleEvent(context.Background(), second)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("responses = %d, want 2", len(publisher.events))
	}
	if got := contextVMResponse(t, publisher.events[1]); string(got.ID) != "2" || got.Error != nil {
		t.Fatalf("cached response = %+v", got)
	}
}

func TestContextVMTransport_EncryptedGiftWrapDispatchesAndResponds(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey, err := nostr.GetPublicKey(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
		return map[string]any{"accepted": true}, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run","params":{"_meta":{"progressToken":"backup-1"}}}`)
	outer := wrapContextVMEvent(t, inner, testRequesterKey, KindContextVMEphemeralWrap)

	transport.HandleEvent(context.Background(), outer)

	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted ContextVM response, got %d", len(publisher.events))
	}
	if publisher.events[0].Kind != KindContextVMEphemeralWrap || !hasTag(publisher.events[0].Tags, "e", outer.ID) || !hasTag(publisher.events[0].Tags, "p", inner.PubKey) {
		t.Fatalf("unexpected wrapper response: kind=%d tags=%#v", publisher.events[0].Kind, publisher.events[0].Tags)
	}
	response := unwrapContextVMResponse(t, publisher.events[0], testRequesterKey)
	if string(response.ID) != `"backup"` || response.Error != nil {
		t.Fatalf("unexpected encrypted response = %+v", response)
	}
}
