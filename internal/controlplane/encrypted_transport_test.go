package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

const (
	testServiceKey   = "0000000000000000000000000000000000000000000000000000000000000001"
	testRequesterKey = "0000000000000000000000000000000000000000000000000000000000000002"
	testOtherKey     = "0000000000000000000000000000000000000000000000000000000000000003"
)

type mockEncryptedPublisher struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (m *mockEncryptedPublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, ev)
	return 1, nil
}

type blockingEncryptedPublisher struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingEncryptedPublisher) Publish(ctx context.Context, _ nostr.Event) (int, error) {
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	select {
	case <-p.release:
		return 1, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
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

func contextVMNotification(t *testing.T, ev nostr.Event) ContextVMJSONRPCNotification {
	t.Helper()
	var notification ContextVMJSONRPCNotification
	if err := json.Unmarshal([]byte(ev.Content), &notification); err != nil {
		t.Fatalf("unmarshal ContextVM notification: %v content=%s", err, ev.Content)
	}
	return notification
}

func assertContextVMProgressAck(t *testing.T, ev nostr.Event, request *nostr.Event) {
	t.Helper()
	assertContextVMProgressAckWithRequestID(t, ev, request, request.ID.Hex())
}

func assertContextVMProgressAckWithRequestID(t *testing.T, ev nostr.Event, request *nostr.Event, requestID string) {
	t.Helper()
	if ev.Kind != KindContextVMMessage || !hasTag(ev.Tags, "e", request.ID.Hex()) || !hasTag(ev.Tags, "p", request.PubKey.Hex()) {
		t.Fatalf("unexpected progress ack event: kind=%d tags=%#v", ev.Kind, ev.Tags)
	}
	notification := contextVMNotification(t, ev)
	if notification.JSONRPC != "2.0" || notification.Method != ContextVMProgressNotificationMethod {
		t.Fatalf("unexpected progress ack notification: %+v", notification)
	}
	params, ok := notification.Params.(map[string]any)
	if !ok || params["requestId"] != requestID || params["status"] != ContextVMProgressStatusProcessing {
		t.Fatalf("unexpected progress ack params: %#v", notification.Params)
	}
}

func wrapContextVMEvent(t *testing.T, inner *nostr.Event, kind int) *nostr.Event {
	t.Helper()
	wrapperKey := nostr.Generate().Hex()
	if kind == KindContextVMGiftWrap {
		wrapperKey = testRequesterKey
	}
	outer := wrapContextVMEventWithWrapperKey(t, inner, wrapperKey, kind)
	if kind == KindContextVMEphemeralWrap && outer.PubKey == inner.PubKey {
		t.Fatalf("test wrapper must use a random pubkey, got inner pubkey %s", inner.PubKey.Hex())
	}
	return outer
}

func wrapContextVMEventWithWrapperKey(t *testing.T, inner *nostr.Event, wrapperKey string, kind int) *nostr.Event {
	t.Helper()
	servicePubkey := testNostrPubKeyFromPrivateKey(t, testServiceKey)
	servicePubkeyHex := servicePubkey.Hex()
	if kind == KindContextVMGiftWrap {
		signer, err := NewPrivateKeySigner(wrapperKey)
		if err != nil {
			t.Fatalf("NewPrivateKeySigner: %v", err)
		}
		outer, _, err := cascontextvm.Wrap(context.Background(), signer, servicePubkeyHex, json.RawMessage(inner.Content))
		if err != nil {
			t.Fatalf("cascadia ContextVM wrap: %v", err)
		}
		return outer
	}
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

type scriptedEncryptedRequestSubscriber struct {
	subscribeRequests chan *scriptedEncryptedSubscription
	authRequests      chan string
}

type scriptedEncryptedSubscription struct {
	events chan *nostr.Event
	eose   chan struct{}
	relay  chan nostrpool.RelayEOSE
	closed chan nostrpool.RelayClosed
}

func newScriptedEncryptedRequestSubscriber() *scriptedEncryptedRequestSubscriber {
	return &scriptedEncryptedRequestSubscriber{
		subscribeRequests: make(chan *scriptedEncryptedSubscription, 4),
		authRequests:      make(chan string, 4),
	}
}

func (s *scriptedEncryptedRequestSubscriber) SubscribeAllWithEOSE(_ context.Context, _ []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	sub := &scriptedEncryptedSubscription{
		events: make(chan *nostr.Event, 4),
		eose:   make(chan struct{}),
		relay:  make(chan nostrpool.RelayEOSE, 4),
		closed: make(chan nostrpool.RelayClosed, 4),
	}
	s.subscribeRequests <- sub
	return &nostrpool.MergedSubscription{
		Events:            sub.events,
		EndOfStoredEvents: sub.eose,
		RelayEOSE:         sub.relay,
		Closed:            sub.closed,
	}, nil
}

func (s *scriptedEncryptedRequestSubscriber) AuthenticateRelay(_ context.Context, relayURL string) error {
	s.authRequests <- relayURL
	return nil
}

func receiveEncryptedSubscription(t *testing.T, ch <-chan *scriptedEncryptedSubscription) *scriptedEncryptedSubscription {
	t.Helper()
	select {
	case sub := <-ch:
		return sub
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription")
		return nil
	}
}

func receiveAuthRequest(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case relayURL := <-ch:
		return relayURL
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay auth")
		return ""
	}
}

func assertNoAuthRequest(t *testing.T, ch <-chan string) {
	t.Helper()
	select {
	case relayURL := <-ch:
		t.Fatalf("unexpected additional relay auth for %s", relayURL)
	default:
	}
}

func TestEncryptedRequestTransport_RunAuthenticatesAndResubscribesOnAuthRequiredClosed(t *testing.T) {
	subscriber := newScriptedEncryptedRequestSubscriber()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(subscriber, newResponder(t, publisher), nil, zap.NewNop())
	processed := make(chan string, 1)
	transport.RegisterContextVMHandler(ContextVMMethodServiceCreate, func(_ context.Context, request ContextVMRequest) (any, error) {
		processed <- request.RPC.Method
		return map[string]string{"status": "ok"}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- transport.Run(ctx) }()

	first := receiveEncryptedSubscription(t, subscriber.subscribeRequests)
	first.closed <- nostrpool.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-1", Reason: "auth-required: restricted kind"}
	if got := receiveAuthRequest(t, subscriber.authRequests); got != "wss://relay.example" {
		t.Fatalf("unexpected auth relay URL: %s", got)
	}

	second := receiveEncryptedSubscription(t, subscriber.subscribeRequests)
	second.closed <- nostrpool.RelayClosed{RelayURL: "wss://relay.example", SubscriptionID: "sub-2", Reason: "auth-required: still restricted"}
	assertNoAuthRequest(t, subscriber.authRequests)

	request := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"req-1","method":"service/create","params":{}}`)
	second.events <- request

	select {
	case method := <-processed:
		if method != ContextVMMethodServiceCreate {
			t.Fatalf("unexpected processed method: %s", method)
		}
		assertNoAuthRequest(t, subscriber.authRequests)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ContextVM request processing")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != context.Canceled {
			t.Fatalf("unexpected Run error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run shutdown")
	}
}

func TestContextVMSubscriptionFiltersAllowBackdatedNIP59OuterEvents(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	filters := contextVMSubscriptionFilters("service-pubkey", now)
	if len(filters) != 2 {
		t.Fatalf("filter count = %d, want 2", len(filters))
	}
	if len(filters[0].Kinds) != 2 || filters[0].Kinds[0] != KindContextVMMessage || filters[0].Kinds[1] != KindContextVMEphemeralWrap {
		t.Fatalf("unexpected direct/ephemeral filter kinds: %v", filters[0].Kinds)
	}
	if filters[0].Since != nostr.Timestamp(now.Add(-encryptedRequestReplayLookback).Unix()) {
		t.Fatalf("direct filter since = %d", filters[0].Since)
	}
	if len(filters[1].Kinds) != 1 || filters[1].Kinds[0] != KindContextVMGiftWrap {
		t.Fatalf("unexpected NIP-59 filter kinds: %v", filters[1].Kinds)
	}
	if filters[1].Since != nostr.Timestamp(now.Add(-contextVMNIP59OuterLookback).Unix()) {
		t.Fatalf("NIP-59 filter since = %d", filters[1].Since)
	}
	for _, filter := range filters {
		if got := filter.Tags[tagRecipientPubkey]; len(got) != 1 || got[0] != "service-pubkey" {
			t.Fatalf("unexpected recipient filter: %v", got)
		}
	}
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

func TestContextVMTransport_PublishesProgressAckBeforeResponseForAuthorizedRoutedRequest(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"ack-1","method":"service/deploy","params":{"service_id":"svc-1"}}`)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, func(context.Context, ContextVMRequest) (any, error) {
		return map[string]any{"accepted": true}, nil
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 2 {
		t.Fatalf("expected progress ack and terminal response, got %d", len(publisher.events))
	}
	assertContextVMProgressAck(t, publisher.events[0], event)
	terminal := contextVMResponse(t, publisher.events[1])
	if terminal.Error != nil || string(terminal.ID) != `"ack-1"` {
		t.Fatalf("unexpected terminal response after progress ack: %+v", terminal)
	}
}

func TestContextVMTransport_ProgressAckBackpressureDoesNotGateHandler(t *testing.T) {
	publisher := &blockingEncryptedPublisher{started: make(chan struct{}), release: make(chan struct{})}
	signer, err := NewPrivateKeySigner(testServiceKey)
	if err != nil {
		t.Fatalf("NewPrivateKeySigner: %v", err)
	}
	responder := NewEncryptedResponder(publisher, signer, testServiceKey, zap.NewNop())
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"ack-blocked","method":"service/deploy","params":{"service_id":"svc-1"}}`)
	transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
	handled := make(chan struct{})
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, func(context.Context, ContextVMRequest) (any, error) {
		close(handled)
		return map[string]any{"accepted": true}, nil
	})

	done := make(chan struct{})
	go func() {
		transport.HandleEvent(context.Background(), event)
		close(done)
	}()

	select {
	case <-publisher.started:
	case <-time.After(time.Second):
		t.Fatal("progress acknowledgement publication did not start")
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handler remained gated behind progress acknowledgement publication")
	}
	close(publisher.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport did not finish after publisher was released")
	}
}

func TestContextVMTransport_DoesNotPublishProgressAckForRoutingMismatch(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	event := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"ack-mismatch","method":"service/deploy"}`)
	event.Tags = nostr.Tags{{"p", "c" + event.PubKey.Hex()[1:]}}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatal(err)
	}
	transport := NewEncryptedRequestTransport(nil, responder, []string{event.PubKey.Hex()}, zap.NewNop())
	transport.RegisterContextVMHandler(ContextVMMethodServiceDeploy, func(context.Context, ContextVMRequest) (any, error) {
		t.Fatalf("routing mismatch reached handler")
		return nil, nil
	})

	transport.HandleEvent(context.Background(), event)

	if len(publisher.events) != 0 {
		t.Fatalf("routing mismatch must stay silent without progress ack, got %d events", len(publisher.events))
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

	if len(publisher.events) != 2 {
		t.Fatalf("expected ContextVM progress ack plus handler error result, got %d events", len(publisher.events))
	}
	assertContextVMProgressAck(t, publisher.events[0], event)
	result := publisher.events[1]
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

	if len(publisher.events) != 2 {
		t.Fatalf("expected ContextVM progress ack plus success result, got %d events", len(publisher.events))
	}
	assertContextVMProgressAck(t, publisher.events[0], event)
	response := contextVMResponse(t, publisher.events[1])
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

	if len(publisher.events) != 2 {
		t.Fatalf("expected ContextVM progress ack plus response, got %d events", len(publisher.events))
	}
	assertContextVMProgressAck(t, publisher.events[0], event)
	responseEvent := publisher.events[1]
	if responseEvent.Kind != KindContextVMMessage || !hasTag(responseEvent.Tags, "e", event.ID.Hex()) || !hasTag(responseEvent.Tags, "p", event.PubKey.Hex()) {
		t.Fatalf("unexpected ContextVM response event: kind=%d tags=%#v", responseEvent.Kind, responseEvent.Tags)
	}
	response := contextVMResponse(t, responseEvent)
	if string(response.ID) != "7" || response.Error != nil {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestRegisterServiceContextVMHandlers_RegistersDeployMethods(t *testing.T) {
	for _, method := range []string{ContextVMMethodServiceDeployPreview, ContextVMMethodServiceDeploy, ContextVMMethodServiceRollback} {
		t.Run(method, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			responder := newResponder(t, publisher)
			requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
			transport := NewEncryptedRequestTransport(nil, responder, []string{requesterPubkey}, zap.NewNop())
			RegisterServiceContextVMHandlers(transport, EncryptedServiceHandlersConfig{})
			event := makeContextVMEvent(t, testRequesterKey, fmt.Sprintf(`{"jsonrpc":"2.0","id":"deploy-registration","method":%q,"params":{}}`, method))

			transport.HandleEvent(context.Background(), event)

			if len(publisher.events) != 2 {
				t.Fatalf("expected progress acknowledgement and ContextVM response, got %d events", len(publisher.events))
			}
			assertContextVMProgressAck(t, publisher.events[0], event)
			response := contextVMResponse(t, publisher.events[1])
			if response.Error == nil {
				t.Fatal("expected missing dependency error")
			}
			if response.Error.Message == "method not found" {
				t.Fatalf("%s was not registered: %+v", method, response.Error)
			}
			if response.Error.Message != "service deployment control plane is not configured" {
				t.Fatalf("unexpected error: %+v", response.Error)
			}
		})
	}
}

func TestDesiredStateHashesEqual(t *testing.T) {
	hash := "sha256:" + strings.Repeat("ab", 32)
	if !desiredStateHashesEqual(hash, strings.TrimPrefix(hash, "sha256:")) {
		t.Fatal("equivalent desired-state hashes should match")
	}
	if desiredStateHashesEqual(hash, "sha256:"+strings.Repeat("cd", 32)) {
		t.Fatal("different desired-state hashes must not match")
	}
	if desiredStateHashesEqual("not-a-hash", hash) {
		t.Fatal("malformed desired-state hashes must not match")
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
	if len(publisher.events) != 3 {
		t.Fatalf("responses = %d, want 3", len(publisher.events))
	}
	assertContextVMProgressAck(t, publisher.events[0], first)
	if got := contextVMResponse(t, publisher.events[2]); string(got.ID) != "2" || got.Error != nil {
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
				if tc.kind == KindContextVMEphemeralWrap && (request.OuterEvent.PubKey.Hex() == requesterPubkey || request.OuterEvent.PubKey == servicePubkey) {
					t.Fatalf("wrapper pubkey %s must be random, not requester/service", request.OuterEvent.PubKey.Hex())
				}
				return map[string]any{"accepted": true}, nil
			})
			inner := makeContextVMEvent(t, testRequesterKey, `{"jsonrpc":"2.0","id":"backup","method":"backup/run","params":{"_meta":{"progressToken":"backup-1"}}}`)
			outer := wrapContextVMEvent(t, inner, tc.kind)

			transport.HandleEvent(context.Background(), outer)

			if len(publisher.events) != 2 {
				t.Fatalf("expected encrypted ContextVM progress ack plus response, got %d", len(publisher.events))
			}
			wrappedAck := publisher.events[0]
			if wrappedAck.Kind != nostr.Kind(tc.kind) || !hasTag(wrappedAck.Tags, "p", inner.PubKey.Hex()) || !hasTag(wrappedAck.Tags, "e", outer.ID.Hex()) {
				t.Fatalf("unexpected wrapper progress ack: kind=%d tags=%#v", wrappedAck.Kind, wrappedAck.Tags)
			}
			innerAck := unwrapContextVMResponseEvent(t, wrappedAck, testRequesterKey)
			if tc.kind == KindContextVMEphemeralWrap {
				assertContextVMProgressAckWithRequestID(t, innerAck, inner, outer.ID.Hex())
			} else {
				notification := contextVMNotification(t, innerAck)
				if notification.JSONRPC != "2.0" || notification.Method != ContextVMProgressNotificationMethod {
					t.Fatalf("unexpected progress ack notification: %+v", notification)
				}
			}
			wrappedResponse := publisher.events[1]
			if err := nostrpool.ValidateInboundEvent(&wrappedResponse, time.Now().UTC(), nostrpool.InboundEventMaxFutureSkew); err != nil {
				t.Fatalf("response wrapper failed NIP-01 validation: %v", err)
			}
			if wrappedResponse.Kind != nostr.Kind(tc.kind) || !hasTag(wrappedResponse.Tags, "p", inner.PubKey.Hex()) || !hasTag(wrappedResponse.Tags, "e", outer.ID.Hex()) {
				t.Fatalf("unexpected wrapper response: kind=%d tags=%#v", wrappedResponse.Kind, wrappedResponse.Tags)
			}
			if wrappedResponse.PubKey == servicePubkey || wrappedResponse.PubKey.Hex() == requesterPubkey {
				t.Fatalf("response wrapper pubkey %s must be random", wrappedResponse.PubKey.Hex())
			}
			innerResponse := unwrapContextVMResponseEvent(t, wrappedResponse, testRequesterKey)
			if innerResponse.Kind != KindContextVMMessage || innerResponse.PubKey != servicePubkey || !hasTag(innerResponse.Tags, "p", inner.PubKey.Hex()) || (tc.kind == KindContextVMEphemeralWrap && !hasTag(innerResponse.Tags, "e", inner.ID.Hex())) {
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
	if publisher.events[0].Kind != KindContextVMGiftWrap || !hasTag(publisher.events[0].Tags, "p", inner.PubKey.Hex()) {
		t.Fatalf("unexpected unauthorized wrapper response: kind=%d tags=%#v", publisher.events[0].Kind, publisher.events[0].Tags)
	}
	response := unwrapContextVMResponse(t, publisher.events[0], testRequesterKey)
	if response.Error == nil || response.Error.Code != -32001 || string(response.ID) != "null" {
		t.Fatalf("unauthorized encrypted response = %+v", response)
	}
}

func TestContextVMTransport_AcceptsCascadiaStoredWrapperPubkey(t *testing.T) {
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

	if !called {
		t.Fatalf("cascadia stored wrapper should reach handler")
	}
	if len(publisher.events) != 2 {
		t.Fatalf("expected stored wrapper progress ack plus response, got %d events", len(publisher.events))
	}
}

func TestContextVMTransport_UnwrapsConformantNIP59WorkerResponse(t *testing.T) {
	ctx := context.Background()
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	transport := NewEncryptedRequestTransport(nil, responder, nil, zap.NewNop())
	workerSigner, err := NewPrivateKeySigner(testRequesterKey)
	if err != nil {
		t.Fatalf("worker signer: %v", err)
	}
	servicePubkey := responder.ServicePubkey()
	secretPath := "/srv/fleet/worktrees/private-repository"
	outer, rumor, err := cascontextvm.WrapEventNIP59(ctx, workerSigner, servicePubkey, &nostr.Event{
		Kind:      KindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"p", servicePubkey}, {"e", strings.Repeat("a", 64)}},
		Content:   `{"jsonrpc":"2.0","id":"scan-1","result":{"path":"` + secretPath + `"}}`,
	}, cascontextvm.StoredGiftWrap)
	if err != nil {
		t.Fatalf("wrap worker response: %v", err)
	}
	inner, format, err := transport.unwrapContextVMEvent(ctx, outer)
	if err != nil {
		t.Fatalf("unwrap worker response: %v", err)
	}
	if format != cascontextvm.EnvelopeFormatNIP59 || inner.ID != rumor.ID || inner.PubKey != rumor.PubKey || !strings.Contains(inner.Content, secretPath) {
		t.Fatalf("unexpected worker response rumor: format=%q inner=%+v", format, inner)
	}
	if inner.Sig != ([64]byte{}) {
		t.Fatal("conformant NIP-59 rumor must remain unsigned")
	}
	if err := validateContextVMInnerEvent(inner, format, time.Now().UTC()); err != nil {
		t.Fatalf("authenticated unsigned rumor rejected by ingress validation: %v", err)
	}
}

func TestContextVMTransport_IgnoresNIP59WorkerResponses(t *testing.T) {
	ctx := context.Background()
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)

	for _, tc := range []struct {
		name              string
		content           string
		authorizedPubkeys []string
	}{
		{
			name:              "result before authorization",
			content:           `{"jsonrpc":"2.0","id":"scan-1","result":{"candidates":[]}}`,
			authorizedPubkeys: []string{otherPubkey},
		},
		{
			name:              "error before request validation",
			content:           `{"jsonrpc":"2.0","id":"scan-1","error":{"code":-32000,"message":"scan failed"}}`,
			authorizedPubkeys: []string{workerPubkey},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			responder := newResponder(t, publisher)
			transport := NewEncryptedRequestTransport(nil, responder, tc.authorizedPubkeys, zap.NewNop())
			workerSigner, err := NewPrivateKeySigner(testRequesterKey)
			if err != nil {
				t.Fatalf("worker signer: %v", err)
			}
			servicePubkey := responder.ServicePubkey()
			outer, _, err := cascontextvm.WrapEventNIP59(ctx, workerSigner, servicePubkey, &nostr.Event{
				Kind:      KindContextVMMessage,
				CreatedAt: nostr.Now(),
				Tags:      nostr.Tags{{"p", servicePubkey}, {"e", strings.Repeat("a", 64)}},
				Content:   tc.content,
			}, cascontextvm.StoredGiftWrap)
			if err != nil {
				t.Fatalf("wrap worker response: %v", err)
			}

			transport.HandleEvent(ctx, outer)

			if len(publisher.events) != 0 {
				t.Fatalf("worker response provoked %d outbound events", len(publisher.events))
			}
		})
	}
}

func TestContextVMTransport_DispatchesAuthenticatedNIP59ResponseWithoutResponderNoise(t *testing.T) {
	ctx := context.Background()
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)
	transport := NewEncryptedRequestTransport(nil, responder, []string{otherPubkey}, zap.NewNop())

	var received []ContextVMResponseEnvelope
	unregister := transport.RegisterContextVMResponseHandler(func(_ context.Context, response ContextVMResponseEnvelope) {
		received = append(received, response)
	})
	workerSigner, err := NewPrivateKeySigner(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubkey := responder.ServicePubkey()
	requestID := strings.Repeat("a", 64)
	result := `{"candidates":[],"scanned_at":"2026-09-02T12:00:00Z","total_candidates":0}`
	wrap := func(id string) *nostr.Event {
		outer, _, err := cascontextvm.WrapEventNIP59(ctx, workerSigner, servicePubkey, &nostr.Event{
			Kind: KindContextVMMessage, CreatedAt: nostr.Now(),
			Tags:    nostr.Tags{{"p", servicePubkey}, {"e", requestID}},
			Content: `{"jsonrpc":"2.0","id":` + fmt.Sprintf("%q", id) + `,"result":` + result + `}`,
		}, cascontextvm.StoredGiftWrap)
		if err != nil {
			t.Fatal(err)
		}
		return outer
	}

	transport.HandleEvent(ctx, wrap("scan-1"))
	if len(received) != 1 {
		t.Fatalf("response handler calls = %d, want 1", len(received))
	}
	got := received[0]
	if got.EnvelopeFormat != cascontextvm.EnvelopeFormatNIP59 || got.Event == nil || got.Event.PubKey.Hex() != testNostrPubKeyHexFromPrivateKey(t, testRequesterKey) {
		t.Fatalf("response provenance lost: %+v", got)
	}
	if got.JSONRPC != "2.0" || string(got.ID) != `"scan-1"` || !got.IDPresent || got.MethodPresent || !got.ResultPresent || got.ErrorPresent || string(got.Result) != result {
		t.Fatalf("lossless response decode = %+v", got)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("worker response provoked %d outbound events", len(publisher.events))
	}

	unregister()
	unregister()
	transport.HandleEvent(ctx, wrap("scan-2"))
	if len(received) != 1 {
		t.Fatalf("unregistered handler calls = %d, want 1", len(received))
	}
	if len(publisher.events) != 0 {
		t.Fatalf("unclaimed worker response provoked %d outbound events", len(publisher.events))
	}
}

func TestContextVMTransport_DropsHistoricalNIP59InnerBeforeRoleDispatch(t *testing.T) {
	ctx := context.Background()
	publisher := &mockEncryptedPublisher{}
	responder := newResponder(t, publisher)
	transport := NewEncryptedRequestTransport(nil, responder, nil, zap.NewNop())
	workerSigner, err := NewPrivateKeySigner(testRequesterKey)
	if err != nil {
		t.Fatal(err)
	}
	servicePubkey := responder.ServicePubkey()
	outer, _, err := cascontextvm.WrapEventNIP59(ctx, workerSigner, servicePubkey, &nostr.Event{
		Kind:      KindContextVMMessage,
		CreatedAt: nostr.Now() - nostr.Timestamp((3*time.Minute)/time.Second),
		Tags:      nostr.Tags{{"p", servicePubkey}},
		Content:   `{"jsonrpc":"2.0","id":"old","result":{}}`,
	}, cascontextvm.StoredGiftWrap)
	if err != nil {
		t.Fatal(err)
	}
	innerSince := nostr.Now() - nostr.Timestamp(encryptedRequestReplayLookback/time.Second)
	transport.handleContextVMEventSince(ctx, outer, innerSince)
	if len(publisher.events) != 0 {
		t.Fatalf("historical NIP-59 response reached role dispatch: %d outbound events", len(publisher.events))
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

func TestContextVMIdempotencyKeySupportsCompatibilityAlias(t *testing.T) {
	tests := []struct {
		name    string
		params  string
		want    string
		wantErr bool
	}{
		{name: "progress token", params: `{"_meta":{"progressToken":"deploy-1"}}`, want: "deploy-1"},
		{name: "compatibility alias", params: `{"idempotency_key":"deploy-1"}`, want: "deploy-1"},
		{name: "matching values", params: `{"idempotency_key":"deploy-1","_meta":{"progressToken":"deploy-1"}}`, want: "deploy-1"},
		{name: "mismatch", params: `{"idempotency_key":"deploy-2","_meta":{"progressToken":"deploy-1"}}`, wantErr: true},
		{name: "non-string alias", params: `{"idempotency_key":42}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := contextVMIdempotencyKey(json.RawMessage(test.params))
			if test.wantErr {
				if err == nil {
					t.Fatal("expected idempotency validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("contextVMIdempotencyKey: %v", err)
			}
			if got != test.want {
				t.Fatalf("key = %q, want %q", got, test.want)
			}
		})
	}
}

func TestContextVMCacheKeyScopesBySignerAndMethod(t *testing.T) {
	base := contextVMCacheKey("requester-a", "service/deploy", "deploy-1")
	if base == contextVMCacheKey("requester-b", "service/deploy", "deploy-1") {
		t.Fatal("cache key must be signer scoped")
	}
	if base == contextVMCacheKey("requester-a", "service/delete", "deploy-1") {
		t.Fatal("cache key must be method scoped")
	}
}

func TestContextVMDedupCache_BoundsEntries(t *testing.T) {
	cache := newContextVMDedupCache(3)

	cache.put("a", ContextVMJSONRPCResponse{JSONRPC: "2.0"})
	cache.put("b", ContextVMJSONRPCResponse{JSONRPC: "2.0"})
	cache.put("c", ContextVMJSONRPCResponse{JSONRPC: "2.0"})

	if cache.len() != 3 {
		t.Fatalf("cache.len() = %d, want 3", cache.len())
	}

	// Adding a 4th entry should evict the oldest ("a").
	cache.put("d", ContextVMJSONRPCResponse{JSONRPC: "2.0"})

	if cache.len() != 3 {
		t.Fatalf("cache.len() = %d after eviction, want 3", cache.len())
	}
	if _, ok := cache.get("a"); ok {
		t.Fatal("oldest entry 'a' should have been evicted")
	}
	if _, ok := cache.get("b"); !ok {
		t.Fatal("entry 'b' should still be present")
	}
	if _, ok := cache.get("d"); !ok {
		t.Fatal("new entry 'd' should be present")
	}
}

func TestContextVMDedupCache_UpdateExistingDoesNotEvict(t *testing.T) {
	cache := newContextVMDedupCache(2)

	cache.put("a", ContextVMJSONRPCResponse{JSONRPC: "2.0"})
	cache.put("b", ContextVMJSONRPCResponse{JSONRPC: "2.0"})

	// Updating "a" should not grow the cache or evict "b".
	cache.put("a", ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: []byte(`"updated"`)})

	if cache.len() != 2 {
		t.Fatalf("cache.len() = %d after update, want 2", cache.len())
	}
	resp, ok := cache.get("a")
	if !ok {
		t.Fatal("updated entry 'a' should still be present")
	}
	if string(resp.ID) != `"updated"` {
		t.Fatalf("entry 'a' ID = %s, want \"updated\"", string(resp.ID))
	}
	if _, ok := cache.get("b"); !ok {
		t.Fatal("entry 'b' should still be present after update of 'a'")
	}
}

func TestContextVMDedupCache_DefaultLimit(t *testing.T) {
	cache := newContextVMDedupCache(0)
	if cache.limit != contextVMDedupDefaultLimit {
		t.Fatalf("default limit = %d, want %d", cache.limit, contextVMDedupDefaultLimit)
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
