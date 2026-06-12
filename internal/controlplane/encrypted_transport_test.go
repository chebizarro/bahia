package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
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

func testNostrSecretKey(t *testing.T, privateKey string) nostr.SecretKey {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		t.Fatalf("parse nostr secret key: %v", err)
	}
	return secret
}

func testNostrPubKeyFromPrivateKey(t *testing.T, privateKey string) nostr.PubKey {
	t.Helper()
	return testNostrSecretKey(t, privateKey).Public()
}

func makeEncryptedRequestEvent(t *testing.T, requesterKey string, envelope EncryptedRequestEnvelope) *nostr.Event {
	t.Helper()
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	servicePubkeyHex := servicePubkey.Hex()
	requesterSecret := testNostrSecretKey(t, requesterKey)
	content, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, requesterSecret)
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
		Tags:      nostr.Tags{{"p", servicePubkeyHex}, {EncryptedRequestRoutingTag, EncryptedRequestWireVersion}},
		Content:   ciphertext,
	}
	if err := event.Sign(requesterSecret); err != nil {
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
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	requesterSecret := testNostrSecretKey(t, requesterKey)
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, requesterSecret)
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
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	servicePubkeyHex := servicePubkey.Hex()
	requesterSecret := testNostrSecretKey(t, requesterKey)
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkeyHex}}, Content: content}
	if err := event.Sign(requesterSecret); err != nil {
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

func wrapContextVMEvent(t *testing.T, inner *nostr.Event, kind int) *nostr.Event {
	t.Helper()
	outer := wrapContextVMEventWithWrapperKey(t, inner, nostr.Generate().Hex(), kind)
	if outer.PubKey == inner.PubKey {
		t.Fatalf("test wrapper must use a random pubkey, got inner pubkey %s", inner.PubKey.Hex())
	}
	return outer
}

func wrapContextVMEventWithWrapperKey(t *testing.T, inner *nostr.Event, wrapperKey string, kind int) *nostr.Event {
	t.Helper()
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	servicePubkeyHex := servicePubkey.Hex()
	wrapperSecret := testNostrSecretKey(t, wrapperKey)
	content, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner ContextVM event: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(servicePubkey, wrapperSecret)
	if err != nil {
		t.Fatalf("conversation key: %v", err)
	}
	ciphertext, err := nip44.Encrypt(string(content), conversationKey)
	if err != nil {
		t.Fatalf("encrypt ContextVM event: %v", err)
	}
	outer := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkeyHex}}, Content: ciphertext}
	if err := outer.Sign(wrapperSecret); err != nil {
		t.Fatalf("sign ContextVM wrapper: %v", err)
	}
	return outer
}

func unwrapContextVMResponseEvent(t *testing.T, ev nostr.Event, requesterKey string) nostr.Event {
	t.Helper()
	requesterSecret := testNostrSecretKey(t, requesterKey)
	conversationKey, err := nip44.GenerateConversationKey(ev.PubKey, requesterSecret)
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
	return inner
}

func unwrapContextVMResponse(t *testing.T, ev nostr.Event, requesterKey string) ContextVMJSONRPCResponse {
	t.Helper()
	inner := unwrapContextVMResponseEvent(t, ev, requesterKey)
	return contextVMResponse(t, inner)
}

func TestEncryptedResponder_DecryptRequestContentRoundTrip(t *testing.T) {
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
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
	if !hasTag(result.Tags, "e", req.ID.Hex()) || !hasTag(result.Tags, "p", req.PubKey.Hex()) {
		t.Fatalf("missing correlation tags: %#v", result.Tags)
	}
	if !hasTag(result.Tags, EncryptedRequestRoutingTag, EncryptedRequestWireVersion) {
		t.Fatalf("missing encrypted routing tag: %#v", result.Tags)
	}
	envelope := decryptResultEnvelope(t, result, testRequesterKey)
	if envelope.RequestEventID != req.ID.Hex() || envelope.Status != "ok" {
		t.Fatalf("unexpected result envelope: %+v", envelope)
	}
}

func TestContextVMTransport_InvalidGiftWrapDropsWithoutResponse(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	event := &nostr.Event{Kind: KindContextVMGiftWrap, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", servicePubkey.Hex()}}, Content: "not-valid-nip44"}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 0 {
		t.Fatalf("invalid ContextVM gift wraps should be dropped without response, got %d events", len(publisher.events))
	}
}

func TestContextVMTransport_RejectsInvalidTimestamp(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	called := false
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"orgs-1","method":"orgs/list"}`)
	event.CreatedAt = nostr.Timestamp(time.Now().Add(nostrpool.InboundEventMaxFutureSkew + time.Minute).Unix())
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler("orgs/list", func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return map[string]any{"orgs": []any{}}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if called {
		t.Fatalf("invalid timestamp request should not reach handler")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("invalid trust-boundary events should be dropped without ContextVM response, got %d", len(publisher.events))
	}
}

func TestContextVMTransport_IgnoresUnroutedMessage(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"notifications-1","method":"notifications/list"}`)
	event.Tags = nostr.Tags{{"p", "c" + event.PubKey.Hex()[1:]}}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{event.PubKey.Hex()}, zap.NewNop())

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 0 {
		t.Fatalf("unrouted ContextVM traffic should be ignored, got %d published events", len(publisher.events))
	}
}

func TestContextVMTransport_RejectsUnauthorizedRequester(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	called := false
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"notifications-1","method":"notifications/list"}`)
	transport := NewEncryptedRequestTransport(nil, responder, []string{otherPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler("notifications/list", func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return map[string]any{"ok": true}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if called {
		t.Fatalf("unauthorized requester should not reach handler")
	}
	if event.PubKey.Hex() != requesterPubkey {
		t.Fatalf("event pubkey = %s, want %s", event.PubKey.Hex(), requesterPubkey)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected ContextVM unauthorized result, got %d events", len(publisher.events))
	}
	response := contextVMResponse(t, publisher.events[0])
	if response.Error == nil || response.Error.Code != -32001 {
		t.Fatalf("unexpected unauthorized response: %+v", response)
	}
}

func TestContextVMTransport_PublishesHandlerFailure(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"create-1","method":"notifications.channels.create","params":{"name":"Ops Webhook"}}`)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler(EncryptedOperationNotificationChannelsCreate, func(context.Context, ContextVMRequest) (any, error) {
		return nil, fmt.Errorf("failed to create notification channel")
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected ContextVM handler error result, got %d events", len(publisher.events))
	}
	result := publisher.events[0]
	if result.Kind != KindContextVMMessage || !hasTag(result.Tags, "e", event.ID.Hex()) || !hasTag(result.Tags, "p", event.PubKey.Hex()) {
		t.Fatalf("unexpected result event: kind=%d tags=%#v", result.Kind, result.Tags)
	}
	response := contextVMResponse(t, result)
	if string(response.ID) != `"create-1"` || response.Error == nil || response.Error.Code != -32000 || response.Error.Message != "failed to create notification channel" {
		t.Fatalf("unexpected handler failure response: %+v", response)
	}
}

func TestContextVMTransport_DispatchesAuthorizedOperation(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"payments-1","method":"payments/history","params":{"limit":10}}`)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler("payments/history", func(_ context.Context, request ContextVMRequest) (any, error) {
		if string(request.RPC.Params) != `{"limit":10}` {
			t.Fatalf("handler payload = %s", request.RPC.Params)
		}
		return map[string]any{"records": []any{}}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 1 {
		t.Fatalf("expected ContextVM success result, got %d events", len(publisher.events))
	}
	response := contextVMResponse(t, publisher.events[0])
	if response.Error != nil || string(response.ID) != `"payments-1"` {
		t.Fatalf("unexpected success response: %+v", response)
	}
}

func TestContextVMTransport_DispatchesJSONRPCRequest(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
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
	if responseEvent.Kind != KindContextVMMessage || !hasTag(responseEvent.Tags, "e", event.ID.Hex()) || !hasTag(responseEvent.Tags, "p", event.PubKey.Hex()) {
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
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
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
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
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
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
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

func TestContextVMTransport_RandomKeyGiftWrapDispatchesAndResponds(t *testing.T) {
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	for _, tc := range []struct {
		name string
		kind int
	}{
		{name: "kind 1059", kind: KindContextVMGiftWrap},
		{name: "kind 21059", kind: KindContextVMEphemeralWrap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			responder := newResponder(t, publisher)
			transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
			transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(_ context.Context, request ContextVMRequest) (any, error) {
				if request.Event.PubKey.Hex() != requesterPubkey {
					t.Fatalf("handler saw sender %s, want inner requester %s", request.Event.PubKey.Hex(), requesterPubkey)
				}
				if request.OuterEvent == nil || request.OuterEvent.Kind != nostr.Kind(tc.kind) {
					t.Fatalf("handler outer event = %+v, want kind %d", request.OuterEvent, tc.kind)
				}
				if request.OuterEvent.PubKey.Hex() == requesterPubkey || request.OuterEvent.PubKey == servicePubkey {
					t.Fatalf("wrapper pubkey %s must be random, not requester/service", request.OuterEvent.PubKey.Hex())
				}
				return map[string]any{"accepted": true}, nil
			})
			inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run","params":{"_meta":{"progressToken":"backup-1"}}}`)
			outer := wrapContextVMEvent(t, inner, tc.kind)

			transport.HandleEvent(context.Background(), outer)

			if len(publisher.events) != 1 {
				t.Fatalf("expected encrypted ContextVM response, got %d", len(publisher.events))
			}
			wrappedResponse := publisher.events[0]
			if err := nostrpool.ValidateInboundEvent(&wrappedResponse, time.Now().UTC(), nostrpool.InboundEventMaxFutureSkew); err != nil {
				t.Fatalf("response wrapper failed NIP-01 validation: %v", err)
			}
			if wrappedResponse.Kind != nostr.Kind(tc.kind) || !hasTag(wrappedResponse.Tags, "e", outer.ID.Hex()) || !hasTag(wrappedResponse.Tags, "p", inner.PubKey.Hex()) {
				t.Fatalf("unexpected wrapper response: kind=%d tags=%#v", wrappedResponse.Kind, wrappedResponse.Tags)
			}
			if wrappedResponse.PubKey == servicePubkey || wrappedResponse.PubKey.Hex() == requesterPubkey {
				t.Fatalf("response wrapper pubkey %s must be random", wrappedResponse.PubKey.Hex())
			}
			innerResponse := unwrapContextVMResponseEvent(t, wrappedResponse, testRequesterKey)
			if innerResponse.Kind != KindContextVMMessage || innerResponse.PubKey != servicePubkey || !hasTag(innerResponse.Tags, "e", inner.ID.Hex()) || !hasTag(innerResponse.Tags, "p", inner.PubKey.Hex()) {
				t.Fatalf("unexpected inner response event: kind=%d pubkey=%s tags=%#v", innerResponse.Kind, innerResponse.PubKey.Hex(), innerResponse.Tags)
			}
			if !innerResponse.VerifySignature() {
				t.Fatalf("inner response signature invalid")
			}
			response := contextVMResponse(t, innerResponse)
			if string(response.ID) != `"backup"` || response.Error != nil {
				t.Fatalf("unexpected encrypted response = %+v", response)
			}
		})
	}
}

func TestContextVMTransport_EncryptedGiftWrapAuthorizesInnerSenderNotWrapper(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	transport := NewEncryptedRequestTransport(nil, responder, []string{otherPubkey}, zap.NewNop())
	called := false
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return map[string]any{"accepted": true}, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run","params":{"_meta":{"progressToken":"backup-unauthorized"}}}`)
	outer := wrapContextVMEvent(t, inner, KindContextVMGiftWrap)

	transport.HandleEvent(context.Background(), outer)

	if called {
		t.Fatalf("unauthorized inner sender should not reach handler")
	}
	if len(publisher.events) != 1 {
		t.Fatalf("expected encrypted unauthorized response, got %d", len(publisher.events))
	}
	if publisher.events[0].Kind != KindContextVMGiftWrap || !hasTag(publisher.events[0].Tags, "e", outer.ID.Hex()) || !hasTag(publisher.events[0].Tags, "p", inner.PubKey.Hex()) {
		t.Fatalf("unexpected unauthorized wrapper response: kind=%d tags=%#v", publisher.events[0].Kind, publisher.events[0].Tags)
	}
	response := unwrapContextVMResponse(t, publisher.events[0], testRequesterKey)
	if response.Error == nil || response.Error.Code != -32001 || string(response.ID) != "null" {
		t.Fatalf("unauthorized encrypted response = %+v", response)
	}
}

func TestContextVMTransport_RejectsNonRandomRequesterWrapperPubkey(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	called := false
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return nil, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run"}`)
	outer := wrapContextVMEventWithWrapperKey(t, inner, testRequesterKey, KindContextVMGiftWrap)

	transport.HandleEvent(context.Background(), outer)

	if called {
		t.Fatalf("non-random requester wrapper should not reach handler")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("non-random requester wrapper should be dropped without response, got %d events", len(publisher.events))
	}
}

func TestContextVMTransport_RejectsInvalidRandomKeyWrapper(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	called := false
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return nil, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run"}`)
	outer := wrapContextVMEvent(t, inner, KindContextVMGiftWrap)
	outer.Content = "tampered-" + outer.Content

	transport.HandleEvent(context.Background(), outer)

	if called {
		t.Fatalf("invalid wrapper should not reach handler")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("invalid wrapper should be dropped without response, got %d events", len(publisher.events))
	}
}

func TestContextVMTransport_RejectsInvalidWrappedInnerEvent(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	called := false
	transport.RegisterContextVMHandler(ContextVMMethodBackupRun, func(context.Context, ContextVMRequest) (any, error) {
		called = true
		return nil, nil
	})
	inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run"}`)
	inner.Content = `{"jsonrpc":"2.0","id":"backup","method":"tools/call"}`
	outer := wrapContextVMEvent(t, inner, KindContextVMEphemeralWrap)

	transport.HandleEvent(context.Background(), outer)

	if called {
		t.Fatalf("invalid inner event should not reach handler")
	}
	if len(publisher.events) != 0 {
		t.Fatalf("invalid inner event should be dropped without response, got %d events", len(publisher.events))
	}
}
