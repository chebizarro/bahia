package soulfactory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

type fakeRelayEndpoint struct {
	url string

	publishResults []RelayPublishResult
	publishCalls   int
	published      []nostr.Event

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

func (e *fakeRelayEndpoint) Publish(_ context.Context, event nostr.Event) RelayPublishResult {
	e.published = append(e.published, event)
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
	event := &nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Now(), Content: content}
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

func newEOSEOnlyRelayBus(t *testing.T) *SoulFactoryRelayBus {
	t.Helper()
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	for i := 0; i < cap(endpoint.subscribeQueue); i++ {
		subscription := newFakeRelaySubscription()
		endpoint.subscribeQueue <- subscription
		close(subscription.eose)
	}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	return bus
}

func TestRelayBusPublishRequiresAtLeastOneAcceptedOK(t *testing.T) {
	accepted := newFakeRelayEndpoint("wss://accepted.example")
	accepted.publishResults = []RelayPublishResult{{Accepted: true}}
	rejected := newFakeRelayEndpoint("wss://rejected.example")
	rejected.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{accepted, rejected})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("event-1")})
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

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("event-2")})
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

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
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

func TestRelayBusQueryDrainsHistoricalEventBufferedBeforeEOSE(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusBackoff(immediateRelayBusBackoff))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	result := make(chan []*nostr.Event, 1)
	errs := make(chan error, 1)
	go func() {
		events, queryErr := bus.Query(t.Context(), []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(31952)}}})
		result <- events
		errs <- queryErr
	}()
	mustReceiveFilters(t, endpoint.subscribeCalls)

	historical := signedRelayBusEvent(t, signer, 31952, "draft")
	subscription.events <- historical
	close(subscription.eose)

	if queryErr := <-errs; queryErr != nil {
		t.Fatalf("Query() error = %v", queryErr)
	}
	events := <-result
	if len(events) != 1 || events[0].ID != historical.ID {
		t.Fatalf("Query() events = %#v, want buffered historical event %s", events, historical.ID)
	}
}

func TestRelayBusEOSEWaitsForRelayThatRecoversAfterInitialSubscribeFailure(t *testing.T) {
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

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, healthy.subscribeCalls)
	mustReceiveFilters(t, failed.subscribeCalls)
	close(healthySub.eose)
	select {
	case <-sub.EndOfStoredEvents:
		t.Fatal("EOSE closed before failed relay recovered and sent EOSE")
	default:
	}

	recoveredSub := newFakeRelaySubscription()
	failed.subscribeQueue <- recoveredSub
	mustReceiveFilters(t, failed.subscribeCalls)
	close(recoveredSub.eose)
	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")
}

func TestRelayBusClosedBeforeEOSEReissuesWithoutCompletingBackfill(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://closed.example")
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)
	first.closed <- "closed: relay restart"
	mustReceiveFilters(t, endpoint.subscribeCalls)
	select {
	case <-sub.EndOfStoredEvents:
		t.Fatal("EOSE closed after CLOSED-before-EOSE instead of waiting for reissued subscription")
	default:
	}
	close(second.eose)
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

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
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

	if _, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}}); err != nil {
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

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
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
	filters := []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}, "t": []string{"task-1"}}}}

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

func TestRelayBusPublishOKFalseWithEmptyReason(t *testing.T) {
	endpoint := newFakeRelayEndpoint("wss://silent.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: false}} // OK false, no reason
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("ok-false-empty")})
	if err == nil {
		t.Fatal("Publish() error = nil, want error for OK false")
	}
	if count != 0 {
		t.Fatalf("Publish() accepted = %d, want 0", count)
	}
	if !strings.Contains(err.Error(), "OK false") {
		t.Fatalf("Publish() error = %q, want default 'OK false' reason", err.Error())
	}
}

func TestRelayBusPublishNetworkErrorCombinedWithOKFalse(t *testing.T) {
	errEndpoint := newFakeRelayEndpoint("wss://error.example")
	errEndpoint.publishResults = []RelayPublishResult{{Error: errors.New("connection reset")}}
	rejectedEndpoint := newFakeRelayEndpoint("wss://rejected.example")
	rejectedEndpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "rate-limited"}}
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{errEndpoint, rejectedEndpoint})
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("net-err-ok-false")})
	if err == nil {
		t.Fatal("Publish() error = nil, want error for combined network error + OK false")
	}
	if count != 0 {
		t.Fatalf("Publish() accepted = %d, want 0", count)
	}
	if !containsAll(err.Error(), "connection reset", "rate-limited") {
		t.Fatalf("Publish() error = %q, want both failure reasons", err.Error())
	}
}

func TestRelayBusMultiRelayDeduplicatesSameEvent(t *testing.T) {
	signer := newFakeSigner(t)
	relay1 := newFakeRelayEndpoint("wss://relay1.example")
	relay2 := newFakeRelayEndpoint("wss://relay2.example")
	sub1 := newFakeRelaySubscription()
	sub2 := newFakeRelaySubscription()
	relay1.subscribeQueue <- sub1
	relay2.subscribeQueue <- sub2
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{relay1, relay2},
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, relay1.subscribeCalls)
	mustReceiveFilters(t, relay2.subscribeCalls)

	// Same event delivered by both relays — should be deduped to one.
	shared := signedRelayBusEvent(t, signer, 1, "shared-event")
	sentinel := signedRelayBusEvent(t, signer, 1, "sentinel-multi")
	sub1.events <- shared
	sub2.events <- shared
	sub1.events <- sentinel

	got1 := mustReceiveRelayEvent(t, sub.Events)
	got2 := mustReceiveRelayEvent(t, sub.Events)

	if got1.ID != shared.ID {
		t.Fatalf("first event ID = %s, want shared %s", got1.ID, shared.ID)
	}
	if got2.ID != sentinel.ID {
		t.Fatalf("second event ID = %s, want sentinel %s (duplicate was not deduped)", got2.ID, sentinel.ID)
	}
}

func TestRelayBusInvalidEventFilteredByValidator(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://relay.example")
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription

	// Custom validator that rejects events with specific content.
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusBackoff(immediateRelayBusBackoff),
		WithRelayBusEventValidator(func(ev *nostr.Event) bool {
			return ev != nil && ev.Content != "invalid"
		}),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)

	invalid := signedRelayBusEvent(t, signer, 1, "invalid")
	valid := signedRelayBusEvent(t, signer, 1, "valid")
	subscription.events <- invalid
	subscription.events <- valid

	got := mustReceiveRelayEvent(t, sub.Events)
	if got.Content != "valid" {
		t.Fatalf("received event content = %q, want 'valid' (invalid event was not filtered)", got.Content)
	}
}

func TestRelayBusClosedWithMultipleReasonsReissuesCorrectly(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://flaky.example")
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	third := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	endpoint.subscribeQueue <- third
	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusBackoff(immediateRelayBusBackoff),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)

	// First CLOSED (non-auth) triggers reconnect.
	first.closed <- "closed: maintenance"
	mustReceiveFilters(t, endpoint.subscribeCalls)

	// Second CLOSED also triggers reconnect.
	second.closed <- "closed: busy"
	mustReceiveFilters(t, endpoint.subscribeCalls)

	// Third subscription completes EOSE normally.
	event := signedRelayBusEvent(t, signer, 1, "after-reconnects")
	third.events <- event
	close(third.eose)

	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")
	got := mustReceiveRelayEvent(t, sub.Events)
	if got.ID != event.ID {
		t.Fatalf("event after multiple reconnects = %s, want %s", got.ID, event.ID)
	}
}

func TestRelayBusNilEventIgnored(t *testing.T) {
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

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)

	sentinel := signedRelayBusEvent(t, signer, 1, "after-nil")
	subscription.events <- nil
	subscription.events <- sentinel

	got := mustReceiveRelayEvent(t, sub.Events)
	if got.ID != sentinel.ID {
		t.Fatalf("received event = %s after nil, want sentinel %s", got.ID, sentinel.ID)
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
