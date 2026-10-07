package soulfactory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestRelayClientPublishDefaultQuorumSucceedsAndCollectsFailures(t *testing.T) {
	accepted := newFakeRelayEndpoint(t)
	accepted.publishResults = []RelayPublishResult{{Accepted: true}}
	rejected := newFakeRelayEndpoint(t)
	rejected.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{accepted, rejected})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	results, err := bus.PublishWithResults(t.Context(), nostr.Event{ID: soulTestID("event-1")})
	if err != nil {
		t.Fatalf("PublishWithResults() error = %v, want default quorum of one relay to succeed", err)
	}
	if len(results) != 2 || !results[0].Accepted || results[1].Reason != "blocked: policy" {
		t.Fatalf("PublishWithResults() results = %+v, want both relay outcomes", results)
	}
}

func TestRelayClientPublishAllRelaysQuorumReportsFailures(t *testing.T) {
	accepted := newFakeRelayEndpoint(t)
	accepted.publishResults = []RelayPublishResult{{Accepted: true}}
	rejected := newFakeRelayEndpoint(t)
	rejected.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{accepted, rejected}, withRelayPublishQuorum(RelayPublishQuorumAll))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("event-all")})
	if err == nil {
		t.Fatal("Publish() error = nil, want failure below the all-relays quorum")
	}
	if count != 1 || !containsAll(err.Error(), "accepted by 1 of 2", "blocked: policy") {
		t.Fatalf("Publish() = %d, %q; want count 1 and per-relay failure detail", count, err.Error())
	}
}

func TestRelayClientPublishQuorumAndDuplicateOK(t *testing.T) {
	duplicate := newFakeRelayEndpoint(t)
	duplicate.publishResults = []RelayPublishResult{{Accepted: false, Reason: "duplicate: already have it"}}
	down := newFakeRelayEndpoint(t)
	down.publishResults = []RelayPublishResult{{Error: errors.New("connection refused")}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{duplicate, down})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("quorum")})
	if err != nil {
		t.Fatalf("Publish() error = %v, want duplicate OK to satisfy the default quorum", err)
	}
	if count != 1 {
		t.Fatalf("Publish() accepted count = %d, want 1", count)
	}
}

func TestRelayClientPublishDoesNotCancelOtherRelaysAfterFirstOK(t *testing.T) {
	acceptedReturned := make(chan struct{})
	accepted := newFakeRelayEndpoint(t)
	accepted.publishFn = func(context.Context, nostr.Event) RelayPublishResult {
		defer close(acceptedReturned)
		return RelayPublishResult{RelayURL: accepted.url, Accepted: true}
	}

	// The slow relay answers only after the first relay's OK has been
	// returned to the bus. The old bus cancelled it at that point.
	slow := newFakeRelayEndpoint(t)
	slow.publishFn = func(ctx context.Context, _ nostr.Event) RelayPublishResult {
		<-acceptedReturned
		if err := ctx.Err(); err != nil {
			return RelayPublishResult{RelayURL: slow.url, Error: err}
		}
		return RelayPublishResult{RelayURL: slow.url, Accepted: true}
	}

	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{accepted, slow})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	results, err := bus.PublishWithResults(t.Context(), nostr.Event{ID: soulTestID("no-cancel")})
	if err != nil {
		t.Fatalf("PublishWithResults() error = %v, want both relays to accept", err)
	}
	if len(results) != 2 || !results[0].Accepted || !results[1].Accepted {
		t.Fatalf("PublishWithResults() results = %+v, want the slow relay's OK collected too", results)
	}
}

func TestRelayClientPublishReportsOKFalseAndAllRelayReject(t *testing.T) {
	blocked := newFakeRelayEndpoint(t)
	blocked.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked: policy"}}
	auth := newFakeRelayEndpoint(t)
	auth.publishResults = []RelayPublishResult{{Accepted: false, Reason: "auth-required: sign in"}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{blocked, auth})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientEOSETransitionsToRealtimeWithoutClosingEvents(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientQueryDrainsHistoricalEventBufferedBeforeEOSE(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientEOSEWaitsForRelayThatRecoversAfterInitialSubscribeFailure(t *testing.T) {
	signer := newFakeSigner(t)
	healthy := newFakeRelayEndpoint(t)
	healthySub := newFakeRelaySubscription()
	healthy.subscribeQueue <- healthySub
	failed := newFakeRelayEndpoint(t)
	failed.subscribeQueue <- &fakeRelaySubscription{err: errors.New("dial failed")}
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{healthy, failed},
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

// A CLOSED before EOSE is the relay's terminal answer to the stored-event
// read: backfill ends without waiting on the reissue, but it is reported as
// incomplete, never as complete. The REQ is still reissued for realtime events.
func TestRelayClientClosedBeforeEOSESettlesBackfillAsIncomplete(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{endpoint},
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)
	first.closed <- "error: relay restart"
	mustReceiveSignal(t, sub.EndOfStoredEvents, "EOSE")
	var incomplete *RelayReadIncompleteError
	if err := sub.StoredEventsIncomplete(nil); !errors.As(err, &incomplete) || !errors.Is(err, ErrRelayReadIncomplete) {
		t.Fatalf("StoredEventsIncomplete() = %v, want *RelayReadIncompleteError", err)
	}
	if incomplete.Cause != nil || len(incomplete.Relays) != 1 ||
		incomplete.Relays[0].Status != RelayStoredEventsClosed || incomplete.Relays[0].Reason != "error: relay restart" {
		t.Fatalf("incomplete = %+v, want the relay reported closed with its reason", incomplete)
	}
	mustReceiveFilters(t, endpoint.subscribeCalls)
}

func TestRelayClientClosedAuthRequiredAuthenticatesAndReissuesSubscription(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{endpoint},
		WithRelaySigner(signer),
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientClosedAuthRequiredIsHandledWhenEventsClosesFirst(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{endpoint},
		WithRelaySigner(signer),
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

// TestRelayClientDeduplicatesDuplicateEvents checks that an event delivered
// twice by one relay, and again by a second relay, reaches the subscriber
// once. The relay library dispatches each EVENT frame on its own goroutine,
// so a subscriber sees a relay's events in no guaranteed order; the test
// therefore waits for EOSE (every stored event is forwarded before it) and
// counts copies per ID instead of relying on arrival order.
func TestRelayClientDeduplicatesDuplicateEvents(t *testing.T) {
	signer := newFakeSigner(t)
	first, second := newFakeRelayEndpoint(t), newFakeRelayEndpoint(t)
	duplicate := signedRelayBusEvent(t, signer, 1, "duplicate")
	other := signedRelayBusEvent(t, signer, 1, "other")
	for _, script := range []struct {
		endpoint *fakeRelayEndpoint
		events   []*nostr.Event
	}{
		{first, []*nostr.Event{duplicate, duplicate, other}},
		{second, []*nostr.Event{duplicate}},
	} {
		subscription := newFakeRelaySubscription()
		for _, event := range script.events {
			subscription.events <- event
		}
		subscription.eose <- struct{}{}
		script.endpoint.subscribeQueue <- subscription
	}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{first, second})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	events, err := bus.Query(t.Context(), []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	copies := map[nostr.ID]int{}
	for _, event := range events {
		copies[event.ID]++
	}
	want := map[nostr.ID]int{duplicate.ID: 1, other.ID: 1}
	if !reflect.DeepEqual(copies, want) {
		t.Fatalf("delivered copies per event = %v, want %v", copies, want)
	}
}

func TestRelayClientReconnectReissuesSubscriptionWithSameFilters(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientPublishOKFalseWithEmptyReason(t *testing.T) {
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishResults = []RelayPublishResult{{Accepted: false}} // OK false, no reason
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientPublishNetworkErrorCombinedWithOKFalse(t *testing.T) {
	errEndpoint := newFakeRelayEndpoint(t)
	errEndpoint.publishResults = []RelayPublishResult{{Error: errors.New("connection reset")}}
	rejectedEndpoint := newFakeRelayEndpoint(t)
	rejectedEndpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "rate-limited"}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{errEndpoint, rejectedEndpoint})
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}

	count, err := bus.Publish(t.Context(), nostr.Event{ID: soulTestID("net-err-ok-false")})
	if err == nil {
		t.Fatal("Publish() error = nil, want error for combined network error + OK false")
	}
	if count != 0 {
		t.Fatalf("Publish() accepted = %d, want 0", count)
	}
	// The dropped connection surfaces as that relay's transport error.
	if !containsAll(err.Error(), errEndpoint.url, rejectedEndpoint.url, "rate-limited") {
		t.Fatalf("Publish() error = %q, want both relays' failures", err.Error())
	}
}

func TestRelayClientMultiRelayDeduplicatesSameEvent(t *testing.T) {
	signer := newFakeSigner(t)
	relay1 := newFakeRelayEndpoint(t)
	relay2 := newFakeRelayEndpoint(t)
	sub1 := newFakeRelaySubscription()
	sub2 := newFakeRelaySubscription()
	relay1.subscribeQueue <- sub1
	relay2.subscribeQueue <- sub2
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{relay1, relay2},
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sub, err := bus.SubscribeAllWithEOSE(ctx, []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(1)}, Tags: nostr.TagMap{"p": []string{signer.pubkey}}}})
	if err != nil {
		t.Fatalf("SubscribeAllWithEOSE() error = %v", err)
	}
	mustReceiveFilters(t, relay1.subscribeCalls)
	mustReceiveFilters(t, relay2.subscribeCalls)

	// Same event delivered by both relays — should be deduped to one. Each
	// relay's events are forwarded in order, but two relays race, so the
	// second relay sends its copy, then a sentinel, after the first copy was
	// delivered: the sentinel arriving next proves the copy was dropped.
	shared := signedRelayBusEvent(t, signer, 1, "shared-event")
	sentinel := signedRelayBusEvent(t, signer, 1, "sentinel-multi")
	sub1.events <- shared
	got1 := mustReceiveRelayEvent(t, sub.Events)
	sub2.events <- shared
	sub2.events <- sentinel
	got2 := mustReceiveRelayEvent(t, sub.Events)

	if got1.ID != shared.ID {
		t.Fatalf("first event ID = %s, want shared %s", got1.ID, shared.ID)
	}
	if got2.ID != sentinel.ID {
		t.Fatalf("second event ID = %s, want sentinel %s (duplicate was not deduped)", got2.ID, sentinel.ID)
	}
}

func TestRelayClientInvalidEventFilteredByValidator(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription

	// Custom validator that rejects events with specific content.
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{endpoint},
		withRelayResubscribeBackoff(fastRelayBackoff),
		withRelayEventValidator(func(ev *nostr.Event) bool {
			return ev != nil && ev.Content != "invalid"
		}),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientClosedWithMultipleReasonsReissuesCorrectly(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	first := newFakeRelaySubscription()
	second := newFakeRelaySubscription()
	third := newFakeRelaySubscription()
	endpoint.subscribeQueue <- first
	endpoint.subscribeQueue <- second
	endpoint.subscribeQueue <- third
	bus, err := newRelayClientFromEndpoints(
		[]*fakeRelayEndpoint{endpoint},
		withRelayResubscribeBackoff(fastRelayBackoff),
	)
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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

func TestRelayClientNilEventIgnored(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint(t)
	subscription := newFakeRelaySubscription()
	endpoint.subscribeQueue <- subscription
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
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
