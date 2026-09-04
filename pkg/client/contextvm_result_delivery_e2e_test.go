package client

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

func TestContextVMResultDeliveryE2EPlainRoundTrip(t *testing.T) {
	client, server, _ := newContextVMResultDeliveryHarness(t, false, false, []contextVME2ERelay{
		{url: "ws://127.0.0.1:7777", subscribes: true, acceptsRequests: true, publishesResponses: true},
	})
	server.RegisterContextVMHandler(controlplane.ContextVMMethodServiceUpdate, func(context.Context, controlplane.ContextVMRequest) (any, error) {
		return map[string]any{"status": "updated", "service_id": "service-1"}, nil
	})

	result, err := client.UpdateServiceNostr(context.Background(), UpdateServiceNostrRequest{
		ID:             "service-1",
		IdempotencyKey: "plain-round-trip-1",
	}, nil)
	if err != nil {
		t.Fatalf("UpdateServiceNostr() error = %v", err)
	}
	if result.Status != "updated" || result.ServiceID != "service-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestContextVMResultDeliveryE2ERetryReplaysCachedDesiredStateHash(t *testing.T) {
	client, server, relay := newContextVMResultDeliveryHarness(t, false, true, []contextVME2ERelay{
		{url: "ws://127.0.0.1:7777", subscribes: true, acceptsRequests: true, publishesResponses: true},
	})
	client.resultTimeout = 25 * time.Millisecond
	client.resultRetries = 1
	var handlerCalls atomic.Int32
	server.RegisterContextVMHandler(controlplane.ContextVMMethodServiceDeployPreview, func(context.Context, controlplane.ContextVMRequest) (any, error) {
		handlerCalls.Add(1)
		return map[string]any{
			"status":             "success",
			"desired_state_hash": "sha256:0123456789abcdef",
		}, nil
	})

	result, err := client.publishAndAwait(context.Background(), operatorRequest{
		Method: controlplane.ContextVMMethodServiceDeployPreview,
		Tags:   nostr.Tags{{"d", "preview-hash-replay-1"}},
		Payload: map[string]any{
			"service_id":     "service-1",
			"environment_id": "environment-1",
		},
	}, nil)
	if err != nil {
		t.Fatalf("publishAndAwait() error = %v", err)
	}
	assertContextVMResultField(t, result, "desired_state_hash", "sha256:0123456789abcdef")
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1 (retry must replay cached response)", got)
	}
	if got := relay.requestPublishCount(); got != 2 {
		t.Fatalf("request publishes = %d, want 2", got)
	}
}

func TestContextVMResultDeliveryE2EEncryptedRoundTrip(t *testing.T) {
	client, server, _ := newContextVMResultDeliveryHarness(t, true, false, []contextVME2ERelay{
		{url: "wss://relay.example", subscribes: true, acceptsRequests: true, publishesResponses: true},
	})
	server.RegisterContextVMHandler("test/encrypted-round-trip", func(context.Context, controlplane.ContextVMRequest) (any, error) {
		return map[string]any{"status": "success", "payload": map[string]any{"delivered": "encrypted"}}, nil
	})

	result, err := client.publishAndAwait(context.Background(), operatorRequest{
		Method:  "test/encrypted-round-trip",
		Tags:    nostr.Tags{{"d", "encrypted-round-trip-1"}},
		Payload: map[string]any{"service_id": "service-1"},
	}, nil)
	if err != nil {
		t.Fatalf("publishAndAwait() error = %v", err)
	}
	assertContextVMResultField(t, result, "delivered", "encrypted")
}

func TestContextVMResultDeliveryE2EDualRelaySubscriptionPartialFailure(t *testing.T) {
	client, server, _ := newContextVMResultDeliveryHarness(t, false, false, []contextVME2ERelay{
		{url: "wss://relay-a.example", subscribes: false, acceptsRequests: true, publishesResponses: false},
		{url: "wss://relay-b.example", subscribes: true, acceptsRequests: true, publishesResponses: true},
	})
	server.RegisterContextVMHandler("test/dual-relay", func(context.Context, controlplane.ContextVMRequest) (any, error) {
		return map[string]any{"status": "success", "payload": map[string]any{"relay": "b"}}, nil
	})

	result, err := client.publishAndAwait(context.Background(), operatorRequest{
		Method:  "test/dual-relay",
		Tags:    nostr.Tags{{"d", "dual-relay-1"}},
		Payload: map[string]any{"service_id": "service-1"},
	}, nil)
	if err != nil {
		t.Fatalf("publishAndAwait() error = %v", err)
	}
	assertContextVMResultField(t, result, "relay", "b")
}

func assertContextVMResultField(t *testing.T, event *nostr.Event, field, want string) {
	t.Helper()
	if event == nil {
		t.Fatal("result event is nil")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
		t.Fatalf("decode result payload: %v", err)
	}
	got := payload[field]
	if got == nil {
		if nested, ok := payload["payload"].(map[string]any); ok {
			got = nested[field]
		}
	}
	if got != want {
		t.Fatalf("result field %s = %#v, want %q; payload=%s", field, got, want, event.Content)
	}
}

type contextVME2ERelay struct {
	url                string
	subscribes         bool
	acceptsRequests    bool
	publishesResponses bool
}

type contextVME2ESubscription struct {
	events    chan *nostr.Event
	relayURLs []string
	closed    bool
}

type contextVME2ERelayTransport struct {
	mu                sync.Mutex
	relays            []contextVME2ERelay
	subscriptions     []*contextVME2ESubscription
	server            *controlplane.EncryptedRequestTransport
	servicePubkey     string
	dropFirstTerminal bool
	droppedTerminal   bool
	requestPublishes  int
}

func newContextVMResultDeliveryHarness(t *testing.T, encrypted, dropFirstTerminal bool, relays []contextVME2ERelay) (*OperatorControlPlaneClient, *controlplane.EncryptedRequestTransport, *contextVME2ERelayTransport) {
	t.Helper()
	operatorPrivateKey := nostr.Generate().Hex()
	serviceSecret := nostr.Generate()
	servicePrivateKey := serviceSecret.Hex()
	serviceKeyer := keyer.NewPlainKeySigner(serviceSecret)
	relay := &contextVME2ERelayTransport{relays: append([]contextVME2ERelay(nil), relays...), servicePubkey: serviceSecret.Public().Hex(), dropFirstTerminal: dropFirstTerminal}
	responder := controlplane.NewEncryptedResponder(relay, serviceKeyer, servicePrivateKey, zap.NewNop())
	server := controlplane.NewEncryptedRequestTransport(nil, responder, []string{mustOperatorTestPubKey(t, operatorPrivateKey)}, zap.NewNop())
	relay.server = server

	client := newTestOperatorClient(t, operatorPrivateKey, relay)
	client.relays = make([]string, 0, len(relays))
	for _, configured := range relays {
		client.relays = append(client.relays, configured.url)
	}
	client.servicePubkey = serviceSecret.Public().Hex()
	client.encrypted = encrypted
	client.activationTimeout = 100 * time.Millisecond
	client.resultTimeout = time.Second
	client.resultRetries = 0
	return client, server, relay
}

func (r *contextVME2ERelayTransport) PublishWithResults(ctx context.Context, event nostr.Event) ([]nostrpool.PublishResult, error) {
	if recipient := firstTagValue(event.Tags, "p"); recipient != "" && recipient != r.servicePubkey {
		_, results := r.publishResponse(event)
		return results, nil
	}

	r.mu.Lock()
	r.requestPublishes++
	server := r.server
	results := make([]nostrpool.PublishResult, 0, len(r.relays))
	accepted := false
	for _, relay := range r.relays {
		result := nostrpool.PublishResult{RelayURL: relay.url, Accepted: relay.acceptsRequests}
		if !relay.acceptsRequests {
			result.Reason = "simulated request publish rejection"
		} else {
			accepted = true
		}
		results = append(results, result)
	}
	r.mu.Unlock()
	if accepted && server != nil {
		server.HandleContextVMEvent(ctx, &event)
	}
	return results, nil
}

func (r *contextVME2ERelayTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	published, _ := r.publishResponse(event)
	return published, nil
}

func (r *contextVME2ERelayTransport) publishResponse(event nostr.Event) (int, []nostrpool.PublishResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	publishedRelays := make(map[string]struct{})
	results := make([]nostrpool.PublishResult, 0, len(r.relays))
	for _, relay := range r.relays {
		results = append(results, nostrpool.PublishResult{RelayURL: relay.url, Accepted: relay.publishesResponses})
		if relay.publishesResponses {
			publishedRelays[relay.url] = struct{}{}
		}
	}
	if len(publishedRelays) == 0 {
		return 0, results
	}
	if r.dropFirstTerminal && !r.droppedTerminal && isTerminalContextVMResponse(event) {
		r.droppedTerminal = true
		return len(publishedRelays), results
	}
	for _, sub := range r.subscriptions {
		if sub.closed || !hasRelayIntersection(sub.relayURLs, publishedRelays) {
			continue
		}
		sub.events <- cloneNostrEvent(&event)
	}
	return len(publishedRelays), results
}

func (r *contextVME2ERelayTransport) SubscribeOperator(_ context.Context, _ []nostr.Filter) (*operatorSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	active := make([]string, 0, len(r.relays))
	for _, relay := range r.relays {
		if relay.subscribes {
			active = append(active, relay.url)
		}
	}
	sub := &contextVME2ESubscription{events: make(chan *nostr.Event, 16), relayURLs: active}
	r.subscriptions = append(r.subscriptions, sub)
	relayEOSE := make(chan nostrpool.RelayEOSE, len(active))
	for _, relayURL := range active {
		relayEOSE <- nostrpool.RelayEOSE{RelayURL: relayURL}
	}
	endOfStoredEvents := make(chan struct{})
	close(endOfStoredEvents)
	closed := make(chan nostrpool.RelayClosed)
	return &operatorSubscription{
		Events:            sub.events,
		EndOfStoredEvents: endOfStoredEvents,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
		relayURLs:         append([]string(nil), active...),
		closeFn: func() {
			r.mu.Lock()
			sub.closed = true
			r.mu.Unlock()
		},
	}, nil
}

func (r *contextVME2ERelayTransport) AuthenticateRelay(context.Context, string) error { return nil }
func (r *contextVME2ERelayTransport) Close()                                          {}

func (r *contextVME2ERelayTransport) requestPublishCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requestPublishes
}

func isTerminalContextVMResponse(event nostr.Event) bool {
	if event.Kind != nostr.Kind(controlplane.KindContextVMMessage) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(event.Content), &fields) != nil {
		return false
	}
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	return hasResult || hasError
}

func hasRelayIntersection(relayURLs []string, published map[string]struct{}) bool {
	for _, relayURL := range relayURLs {
		if _, ok := published[relayURL]; ok {
			return true
		}
	}
	return false
}

func cloneNostrEvent(event *nostr.Event) *nostr.Event {
	if event == nil {
		return nil
	}
	clone := *event
	clone.Tags = append(nostr.Tags(nil), event.Tags...)
	return &clone
}
