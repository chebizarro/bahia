package client

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

type recordingOperatorKeyer struct {
	nostr.Keyer
	encryptCalls int
	decryptCalls int
}

func (s *recordingOperatorKeyer) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	s.encryptCalls++
	return s.Keyer.Encrypt(ctx, plaintext, recipient)
}

func (s *recordingOperatorKeyer) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	s.decryptCalls++
	return s.Keyer.Decrypt(ctx, ciphertext, sender)
}

func wrappedContextVMResult(t *testing.T, operatorPubkey nostr.PubKey, outerRequest, innerRequest nostr.Event, responseSecret nostr.SecretKey, tamperSignature bool, result any) *nostr.Event {
	t.Helper()
	wrapperSecret := nostr.Generate()
	response := signedOperatorReply(t, responseSecret.Hex(), controlplane.KindContextVMMessage,
		nostr.Tags{{"e", innerRequest.ID.Hex(), "", "reply"}, {"p", operatorPubkey.Hex()}, {controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion}},
		contextVMResponseContent(t, innerRequest, result))
	if tamperSignature {
		response.Content += " "
	}
	plaintext, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal encrypted inner response: %v", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(operatorPubkey, wrapperSecret)
	if err != nil {
		t.Fatalf("derive encrypted response key: %v", err)
	}
	ciphertext, err := nip44.Encrypt(string(plaintext), conversationKey)
	if err != nil {
		t.Fatalf("encrypt response: %v", err)
	}
	outer := &nostr.Event{
		Kind: nostr.Kind(controlplane.KindContextVMGiftWrap), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"e", outerRequest.ID.Hex(), "", "reply"}, {"p", operatorPubkey.Hex()}}, Content: ciphertext,
	}
	if err := outer.Sign(wrapperSecret); err != nil {
		t.Fatalf("sign encrypted response wrapper: %v", err)
	}
	return outer
}

type fakeOperatorTransport struct {
	mu               sync.Mutex
	events           chan *nostr.Event
	eose             chan struct{}
	operatorEOSE     chan struct{}
	relayEOSE        chan nostrpool.RelayEOSE
	closedEvents     chan nostrpool.RelayClosed
	relayURLs        []string
	autoActivate     bool
	subscribeNotify  chan struct{}
	publishFn        func(context.Context, nostr.Event) (int, error)
	publishResultsFn func(context.Context, nostr.Event) ([]nostrpool.PublishResult, error)
	subscribeErr     error
	published        []nostr.Event
	filters          []nostr.Filter
	calls            []string
	closed           bool
}

func newFakeOperatorTransport() *fakeOperatorTransport {
	operatorEOSE := make(chan struct{})
	close(operatorEOSE)
	return &fakeOperatorTransport{
		events:          make(chan *nostr.Event, 32),
		eose:            make(chan struct{}),
		operatorEOSE:    operatorEOSE,
		relayEOSE:       make(chan nostrpool.RelayEOSE, 8),
		closedEvents:    make(chan nostrpool.RelayClosed, 8),
		relayURLs:       []string{"wss://relay.example"},
		autoActivate:    true,
		subscribeNotify: make(chan struct{}, 8),
	}
}

func (f *fakeOperatorTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "publish")
	f.published = append(f.published, ev)
	fn := f.publishFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, ev)
	}
	return 1, nil
}

func (f *fakeOperatorTransport) PublishWithResults(ctx context.Context, ev nostr.Event) ([]nostrpool.PublishResult, error) {
	f.mu.Lock()
	fn := f.publishResultsFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, ev)
	}
	published, err := f.Publish(ctx, ev)
	if published <= 0 {
		return nil, err
	}
	results := make([]nostrpool.PublishResult, 0, published)
	for i := 0; i < published; i++ {
		results = append(results, nostrpool.PublishResult{RelayURL: "wss://relay.example", Accepted: true})
	}
	return results, err
}

func (f *fakeOperatorTransport) SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "subscribe")
	f.filters = append(f.filters, filters...)
	err := f.subscribeErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &nostrpool.MergedSubscription{
		Events:            f.events,
		EndOfStoredEvents: f.eose,
		RelayEOSE:         f.relayEOSE,
		Closed:            f.closedEvents,
	}, nil
}

func (f *fakeOperatorTransport) SubscribeOperator(ctx context.Context, filters []nostr.Filter) (*operatorSubscription, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "subscribe")
	f.filters = append(f.filters, filters...)
	err := f.subscribeErr
	relayURLs := append([]string(nil), f.relayURLs...)
	autoActivate := f.autoActivate
	notify := f.subscribeNotify
	f.mu.Unlock()
	if notify != nil {
		notify <- struct{}{}
	}
	if autoActivate {
		for _, relayURL := range relayURLs {
			f.relayEOSE <- nostrpool.RelayEOSE{RelayURL: relayURL}
		}
	}
	if err != nil {
		return nil, err
	}
	return &operatorSubscription{
		Events:            f.events,
		EndOfStoredEvents: f.operatorEOSE,
		RelayEOSE:         f.relayEOSE,
		Closed:            f.closedEvents,
		relayURLs:         relayURLs,
	}, nil
}

func (f *fakeOperatorTransport) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

func (f *fakeOperatorTransport) onlyPublished(t *testing.T) nostr.Event {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(f.published))
	}
	return f.published[0]
}

func (f *fakeOperatorTransport) onlyFilter(t *testing.T) nostr.Filter {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.filters) != 1 {
		t.Fatalf("filter count = %d, want 1", len(f.filters))
	}
	return f.filters[0]
}

func mustOperatorTestSecret(t *testing.T, privateKey string) nostr.SecretKey {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(privateKey))
	if err != nil {
		t.Fatalf("parse nostr private key: %v", err)
	}
	return secret
}

func mustOperatorTestPubKey(t *testing.T, privateKey string) string {
	t.Helper()
	return mustOperatorTestSecret(t, privateKey).Public().Hex()
}

func newTestOperatorClient(t *testing.T, privateKey string, transport operatorRelayTransport) *ContextVMRequestClient {
	t.Helper()
	normalized, err := NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("NormalizeNostrPrivateKey() error = %v", err)
	}
	secret := mustOperatorTestSecret(t, normalized)
	localKeyer := keyer.NewPlainKeySigner(secret)
	pubkey := secret.Public().Hex()
	return &ContextVMRequestClient{relays: []string{"wss://relay.example"}, signer: localKeyer, cipher: localKeyer, pubkey: pubkey, transport: transport}
}

func decodePublishedContextVMRequest(t *testing.T, event nostr.Event) contextVMRPCRequest {
	t.Helper()
	var rpc contextVMRPCRequest
	if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
		t.Fatalf("decode ContextVM request content: %v", err)
	}
	return rpc
}

func signedContextVMResult(t *testing.T, privateKey string, request nostr.Event, result any) *nostr.Event {
	t.Helper()
	return signedOperatorReply(t, privateKey, controlplane.KindContextVMMessage, nostr.Tags{{"e", request.ID.Hex(), "", "reply"}, {"p", request.PubKey.Hex()}, {controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion}}, contextVMResponseContent(t, request, result))
}

func signedContextVMError(t *testing.T, privateKey string, request nostr.Event, message string) *nostr.Event {
	t.Helper()
	return signedContextVMErrorCode(t, privateKey, request, -32000, message)
}

func signedContextVMErrorCode(t *testing.T, privateKey string, request nostr.Event, code int, message string) *nostr.Event {
	t.Helper()
	rpc := decodePublishedContextVMRequest(t, request)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "error": map[string]any{"code": code, "message": message}})
	if err != nil {
		t.Fatalf("encode ContextVM error: %v", err)
	}
	return signedOperatorReply(t, privateKey, controlplane.KindContextVMMessage, nostr.Tags{{"e", request.ID.Hex(), "", "reply"}, {"p", request.PubKey.Hex()}, {controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion}}, string(body))
}

func contextVMResponseContent(t *testing.T, request nostr.Event, result any) string {
	t.Helper()
	rpc := decodePublishedContextVMRequest(t, request)
	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode ContextVM result: %v", err)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": json.RawMessage(resultBytes)})
	if err != nil {
		t.Fatalf("encode ContextVM response: %v", err)
	}
	return string(body)
}

func signedOperatorReply(t *testing.T, privateKey string, kind int, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Tags: tags, Content: content}
	if err := event.Sign(mustOperatorTestSecret(t, privateKey)); err != nil {
		t.Fatalf("sign reply event: %v", err)
	}
	return event
}

func assertSignedEvent(t *testing.T, event nostr.Event) {
	t.Helper()
	if !event.CheckID() {
		t.Fatalf("event ID does not match serialized event: %#v", event)
	}
	if !event.VerifySignature() {
		t.Fatalf("event signature invalid")
	}
}

func assertTagValue(t *testing.T, tags nostr.Tags, name, value string) {
	t.Helper()
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return
		}
	}
	t.Fatalf("missing tag %s=%s in %#v", name, value, tags)
}

// testContextVMTransportRequest exercises the retained log-fetch request.
type testLogFetchResult struct {
	RunID  string `json:"run_id"`
	Stdout string `json:"stdout"`
}

func testContextVMTransportRequest(c *ContextVMRequestClient, ctx context.Context) (*testLogFetchResult, error) {
	event, err := c.Request(ctx, controlplane.ContextVMMethodDeploymentRunLogsGet, map[string]any{"run_id": "run-1"}, nil, nil)
	if err != nil {
		return nil, err
	}
	var result testLogFetchResult
	if err := json.Unmarshal([]byte(event.Content), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
