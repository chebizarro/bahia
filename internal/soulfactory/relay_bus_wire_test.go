package soulfactory

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
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

func TestRelayBusPublishCollectsSlowRelayOKOverTheWire(t *testing.T) {
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

	bus, err := NewSoulFactoryRelayBus([]string{fast, slow}, WithRelayBusSigner(signer))
	if err != nil {
		t.Fatalf("new bus: %v", err)
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
