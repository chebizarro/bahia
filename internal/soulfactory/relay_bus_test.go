package soulfactory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

type fakeRelayEndpoint struct {
	url string

	publishResults []RelayPublishResult
	publishCalls   int

	subscribeQueue chan *fakeRelaySubscription
	subscribeCalls chan []nostr.Filter
	authCalls      chan struct{}
}

func newFakeRelayEndpoint(url string) *fakeRelayEndpoint {
	return &fakeRelayEndpoint{
		url:            url,
		subscribeQueue: make(chan *fakeRelaySubscription, 8),
		subscribeCalls: make(chan []nostr.Filter, 8),
		authCalls:      make(chan struct{}, 8),
	}
}

func (e *fakeRelayEndpoint) URL() string { return e.url }

func (e *fakeRelayEndpoint) Publish(context.Context, nostr.Event) RelayPublishResult {
	result := RelayPublishResult{RelayURL: e.url, Error: errors.New("unexpected publish")}
	if e.publishCalls < len(e.publishResults) {
		result = e.publishResults[e.publishCalls]
	}
	e.publishCalls++
	if result.RelayURL == "" {
		result.RelayURL = e.url
	}
	return result
}

func (e *fakeRelayEndpoint) Subscribe(_ context.Context, filters []nostr.Filter) (relayBusRelaySubscription, error) {
	copied := append([]nostr.Filter(nil), filters...)
	e.subscribeCalls <- copied
	sub := <-e.subscribeQueue
	if sub.err != nil {
		return nil, sub.err
	}
	return sub, nil
}

func (e *fakeRelayEndpoint) Auth(context.Context, relayAuthSigner) error {
	e.authCalls <- struct{}{}
	return nil
}

func (e *fakeRelayEndpoint) Close() {}

type fakeRelaySubscription struct {
	events chan *nostr.Event
	eose   chan struct{}
	closed chan string
	err    error
}

func newFakeRelaySubscription() *fakeRelaySubscription {
	return &fakeRelaySubscription{
		events: make(chan *nostr.Event, 8),
		eose:   make(chan struct{}, 1),
		closed: make(chan string, 2),
	}
}

func (s *fakeRelaySubscription) Events() <-chan *nostr.Event        { return s.events }
func (s *fakeRelaySubscription) EndOfStoredEvents() <-chan struct{} { return s.eose }
func (s *fakeRelaySubscription) ClosedReason() <-chan string        { return s.closed }
func (s *fakeRelaySubscription) Close()                             {}

func signedRelayBusEvent(t *testing.T, signer fakeSigner, kind int, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Content: content}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}

func mustReceiveRelayEvent(t *testing.T, ch <-chan *nostr.Event) *nostr.Event {
	t.Helper()
	ev, ok := <-ch
	if !ok {
		t.Fatal("event channel closed before expected event")
	}
	return ev
}

func mustReceiveFilters(t *testing.T, ch <-chan []nostr.Filter) []nostr.Filter {
	t.Helper()
	filters, ok := <-ch
	if !ok {
		t.Fatal("subscribe call channel closed before expected call")
	}
	return filters
}

func mustReceiveSignal[T any](t *testing.T, ch <-chan T, _ string) {
	t.Helper()
	<-ch
}

func immediateRelayBusBackoff(context.Context, int) error { return nil }

func TestRelayBusPublishRequiresAtLeastOneAcceptedOK(t *testing.T) {
	accepted := newFakeRelayEndpoint("wss://accepted.example")
	accepted.publishResults = []RelayPublishResult{{Accepted: true}}
	rejected := newFakeRelayEndpoint("wss://rejected.example")
	rejected.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{accepted, rejected})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: "event-1"})
	if err != nil {
		t.Fatalf("Publish() error = %v, want success with one accepted OK", err)
	}
	if count != 1 {
		t.Fatalf("Publish() accepted count = %d, want 1", count)
	}
}

func TestRelayBusPublishReportsOKFalseAndAllRelayReject(t *testing.T) {
	blocked := newFakeRelayEndpoint("wss://blocked.example")
	blocked.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	auth := newFakeRelayEndpoint("wss://auth.example")
	auth.publishResults = []RelayPublishResult{{Accepted: false, Reason: "auth-required: sign in"}}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{blocked, auth})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: "event-2"})
	if err == nil {
		t.Fatal("Publish() error = nil, want all-relay reject error")
	}
	if count != 0 {
		t.Fatalf("Publish() accepted count = %d, want 0", count)
	}
	if got := err.Error(); !containsAll(got, "blocked: policy", "auth-required: sign in") {
		t.Fatalf("Publish() error = %q, want both OK false reasons", got)
	}
}

func TestRelayBusEOSETransitionsToRealtimeWithoutClosingEvents(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)

	historical := signedRelayBusEvent(t, signer, 1, "historical")
	subscription.events <- historical
	if got := mustReceiveRelayEvent(t, sub.Events); got.ID != historical.ID {
		t.Fatalf("historical event ID = %s, want %s", got.ID, historical.ID)
	}
	close(subscription.eose)
	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")

	realtime := signedRelayBusEvent(t, signer, 1, "realtime")
	subscription.events <- realtime
	if got := mustReceiveRelayEvent(t, sub.Events); got.ID != realtime.ID {
		t.Fatalf("realtime event ID = %s, want %s", got.ID, realtime.ID)
	}
}

func TestRelayBusEOSEDoesNotWaitForRelayThatFailsInitialSubscribe(t *testing.T) {
	signer := newFakeSigner(t)
	healthy := newFakeRelayEndpoint("wss://healthy.example")
	healthySub := newFakeRelaySubscription()
	healthy.subscribeQueue <- healthySub
	failed := newFakeRelayEndpoint("wss://failed.example")
	failed.subscribeQueue <- &fakeRelaySubscription{err: errors.New("dial failed")}
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{healthy, failed},
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, healthy.subscribeCalls)
	mustReceiveFilters(t, failed.subscribeCalls)
	close(healthySub.eose)
	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")
}

func TestRelayBusClosedAuthRequiredAuthenticatesAndReissuesSubscription(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://auth.example")
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusSigner(signer),
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)
	first.closed <- "auth-required: restricted"
	mustReceiveSignal(t, endpoint.authCalls, "auth")
	mustReceiveFilters(t, endpoint.subscribeCalls)

	close(second.eose)
	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")
}

func TestRelayBusClosedAuthRequiredIsHandledWhenEventsClosesFirst(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://auth-close.example")
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusSigner(signer),
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if _, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}}); err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)
	first.closed <- "auth-required: restricted"
	close(first.events)
	mustReceiveSignal(t, endpoint.authCalls, "auth")
	mustReceiveFilters(t, endpoint.subscribeCalls)
}

func TestRelayBusDeduplicatesDuplicateEvents(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)

	duplicate := signedRelayBusEvent(t, signer, 1, "duplicate")
	sentinel := signedRelayBusEvent(t, signer, 1, "sentinel")
	subscription.events <- duplicate
	subscription.events <- duplicate
	subscription.events <- sentinel

	if got := mustReceiveRelayEvent(t, sub.Events); got.ID != duplicate.ID {
		t.Fatalf("first event ID = %s, want duplicate %s", got.ID, duplicate.ID)
	}
	if got := mustReceiveRelayEvent(t, sub.Events); got.ID != sentinel.ID {
		t.Fatalf("second event ID = %s, want sentinel %s; duplicate was not filtered", got.ID, sentinel.ID)
	}
}

func TestRelayBusReconnectReissuesSubscriptionWithSameFilters(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	filters := []nostr.Filter{{Kinds: []int{1}, Tags: nostr.TagMap{"p": []string{signer.pubkey}, "t": []string{"task-1"}}}}

	if _, err := bus.SubscribeAllWithEOSE(ctx, filters); err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	firstFilters := mustReceiveFilters(t, endpoint.subscribeCalls)
	close(first.events)
	secondFilters := mustReceiveFilters(t, endpoint.subscribeCalls)

	if !reflect.DeepEqual(firstFilters, filters) {
		t.Fatalf("first subscription filters = %#v, want %#v", firstFilters, filters)
	}
	if !reflect.DeepEqual(secondFilters, filters) {
		t.Fatalf("reissued subscription filters = %#v, want %#v", secondFilters, filters)
	}
}

func containsAll(value string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(value, want) {
			return false
		}
	}
	return true
}
