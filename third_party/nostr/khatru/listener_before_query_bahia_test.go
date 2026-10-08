package khatru

import (
	"context"
	"iter"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
)

// TestListenerIsRegisteredBeforeTheStoredQuery (Bahia patch, see
// BAHIA_PATCHES.md): khatru handles every websocket message on its own
// goroutine and used to register a REQ's live listener only after the stored
// query returned. An event saved while the query ran was therefore neither
// replayed (the query had passed its point) nor broadcast (the listener did
// not exist yet): a live subscriber missed it forever even though its
// publisher got OK true. Bahia's relay pool hit this as a CI-only hang in
// TestRelayPoolPagesPastNIP11MaxLimit, where the event published after the
// paged answer raced the live follow-up REQ.
//
// The test forces the interleaving deterministically: the target REQ's
// QueryStored hook publishes an event on a second connection and waits for
// its OK, so the save and the broadcast complete inside the query window,
// and then replays nothing, so the only possible delivery is through the
// live listener.
func TestListenerIsRegisteredBeforeTheStoredQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rl := NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	rl.UseEventstore(store, 100)

	server := httptest.NewServer(rl)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http")

	subscriber, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { subscriber.Close() })
	publisher, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { publisher.Close() })

	sk := nostr.Generate()
	var (
		once       sync.Once
		published  = make(chan nostr.Event, 1)
		publishErr = make(chan error, 1)
	)
	inner := rl.QueryStored
	rl.QueryStored = func(qctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
		target := false
		once.Do(func() { target = true })
		if !target {
			return inner(qctx, filter)
		}
		return func(yield func(nostr.Event) bool) {
			// Inside the stored query: save and broadcast one event through
			// a second connection, waiting for its OK so both complete
			// before the query (and therefore the listener registration, in
			// the unpatched ordering) finishes. Replay nothing: any delivery
			// can only come from a listener registered before the query.
			ev := nostr.Event{
				Kind:      1,
				CreatedAt: nostr.Now(),
				Content:   "saved while the stored query ran",
				PubKey:    sk.Public(),
			}
			if err := ev.Sign(sk); err != nil {
				publishErr <- err
				return
			}
			pctx, pcancel := context.WithTimeout(ctx, 15*time.Second)
			defer pcancel()
			if err := publisher.Publish(pctx, ev); err != nil {
				publishErr <- err
				return
			}
			published <- ev
		}
	}

	sub, err := subscriber.Subscribe(ctx, nostr.Filter{Kinds: []nostr.Kind{1}}, nostr.SubscriptionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Unsub)

	var want nostr.Event
	select {
	case want = <-published:
	case err := <-publishErr:
		t.Fatalf("publish inside the stored query failed: %v", err)
	case <-ctx.Done():
		t.Fatal("the stored query never published")
	}

	select {
	case got := <-sub.Events:
		if got.ID != want.ID {
			t.Fatalf("got event %s, want the event saved during the query %s", got.ID, want.ID)
		}
	case <-ctx.Done():
		t.Fatal("the event saved during the stored query never reached the subscription: " +
			"the listener was registered after the query")
	}
	select {
	case <-sub.EndOfStoredEvents:
	case <-ctx.Done():
		t.Fatal("EOSE never arrived")
	}
}
