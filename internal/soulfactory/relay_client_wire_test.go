package soulfactory

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
)

func newKhatruTestRelay(t *testing.T, configure func(*khatru.Relay)) string {
	t.Helper()
	relay := khatru.NewRelay()
	configure(relay)
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestRelayClientPublishCollectsSlowRelayOKOverTheWire(t *testing.T) {
	signer := newFakeSigner(t)
	fastAccepted := make(chan struct{})
	fast := newKhatruTestRelay(t, func(relay *khatru.Relay) {
		relay.OnEventSaved = func(context.Context, nostr.Event) { close(fastAccepted) }
	})
	slow := newKhatruTestRelay(t, func(relay *khatru.Relay) {
		// Answer only after the fast relay has already accepted the event.
		relay.OnEvent = func(ctx context.Context, _ nostr.Event) (bool, string) {
			select {
			case <-fastAccepted:
				return false, ""
			case <-ctx.Done():
				return true, "error: client went away"
			}
		}
	})

	bus, err := NewRelayClient([]string{fast, slow}, WithRelaySigner(signer), withRelayAdmission(generousTestAdmission()))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	results, err := bus.PublishWithResults(ctx, *signedRelayBusEvent(t, signer, 1, "two relays"))
	if err != nil {
		t.Fatalf("PublishWithResults() error = %v, want both relays to accept", err)
	}
	for _, result := range results {
		if !result.Accepted {
			t.Fatalf("relay %s result = %+v, want accepted", result.RelayURL, result)
		}
	}
}

// TestReactorPublishesOverOneSharedPool: the reactor publishes over its one
// relay client, not a pool per publish: every publish to its
// relay set reuses one connection per relay, and the client's quorum (one
// relay) still applies when the other relay refuses.
func TestReactorPublishesOverOneSharedPool(t *testing.T) {
	signer := newFakeSigner(t)
	var firstConnections, secondConnections atomic.Int32
	first := newKhatruTestRelay(t, func(relay *khatru.Relay) {
		relay.OnConnect = func(context.Context) { firstConnections.Add(1) }
	})
	second := newKhatruTestRelay(t, func(relay *khatru.Relay) {
		relay.OnConnect = func(context.Context) { secondConnections.Add(1) }
		relay.OnEvent = func(context.Context, nostr.Event) (bool, string) {
			return true, "blocked: refusing test publication"
		}
	})
	reactor := NewReactor(Config{Relays: []string{first}, AdditionalRelays: []string{second}}, nil, signer, slog.Default(), withReactorRelayAdmission(generousTestAdmission()))
	defer reactor.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for i := range 3 {
		ev := signedRelayBusEvent(t, signer, 1, string(rune('a'+i)))
		if err := reactor.publish(ctx, ev, reactor.provisioningPublicationRelays()); err != nil {
			t.Fatalf("publish %d with one-of-two quorum: %v", i, err)
		}
	}
	// A subset of the relay set is a view of the same pool.
	if err := reactor.publish(ctx, signedRelayBusEvent(t, signer, 1, "subset"), []string{first}); err != nil {
		t.Fatalf("publish to a subset: %v", err)
	}
	if err := reactor.publish(ctx, signedRelayBusEvent(t, signer, 1, "outside"), []string{"wss://elsewhere.example"}); err == nil {
		t.Fatal("publish outside the reactor's relay set must fail, not dial a new pool")
	}
	if got := [2]int32{firstConnections.Load(), secondConnections.Load()}; got != [2]int32{1, 1} {
		t.Fatalf("connections = %v; want one per relay", got)
	}
}

// TestSharedRelayClientViews: the shared client hands runtime adapters views
// of its pool. A view reads and writes only its relays over the shared
// connections and closing it leaves the pool open; a relay set outside the
// client gets its own client. A reactor given the client does not close it.
func TestSharedRelayClientViews(t *testing.T) {
	signer := newFakeSigner(t)
	var connections atomic.Int32
	stored := signedRelayBusEvent(t, signer, 1, "stored")
	withStore := func(relay *khatru.Relay) {
		store := &slicestore.SliceStore{}
		if err := store.Init(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(store.Close)
		relay.UseEventstore(store, 100)
		relay.OnConnect = func(context.Context) { connections.Add(1) }
	}
	first := newKhatruTestRelay(t, withStore)
	second := newKhatruTestRelay(t, withStore)
	other := newKhatruTestRelay(t, func(*khatru.Relay) {})
	shared, err := NewRelayClient([]string{first, second}, WithRelaySigner(signer), withRelayAdmission(generousTestAdmission()))
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	reactor := NewReactor(Config{Relays: []string{first, second}}, nil, signer, slog.Default(), WithRelayClient(shared))
	if reactor.relayClient != shared {
		t.Fatal("reactor did not take the shared client")
	}
	reactor.Close()

	transports := shared.RuntimeTransports()
	transport, err := transports([]string{first})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := transport.(*RelayClient)
	if !ok || view.pool != shared.pool || !slices.Equal(view.Relays(), []string{nostr.NormalizeURL(first)}) {
		t.Fatalf("transport for a held relay is not a view of the shared pool: %+v", transport)
	}
	if accepted, err := view.Publish(ctx, *stored); err != nil || accepted != 1 {
		t.Fatalf("view publish accepted=%d err=%v; want only its relay", accepted, err)
	}
	transport.Close()
	events, err := shared.Query(ctx, []nostr.Filter{{IDs: []nostr.ID{stored.ID}}})
	if err != nil || len(events) != 1 {
		t.Fatalf("shared pool after closing a view: events=%d err=%v", len(events), err)
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d; views must reuse the shared ones", got)
	}

	outside, err := transports([]string{other})
	if err != nil {
		t.Fatal(err)
	}
	defer outside.Close()
	if client := outside.(*RelayClient); client.pool == shared.pool || client.view {
		t.Fatal("a relay outside the shared set must get its own client")
	}
}
