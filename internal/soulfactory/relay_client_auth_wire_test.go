package soulfactory

import (
	"context"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
)

// newNIP42KhatruRelay starts an in-process khatru relay that rejects every REQ
// and EVENT from an unauthenticated connection with "auth-required:". Like
// khatru itself, it re-sends the AUTH challenge before every such rejection;
// challengeOnConnect additionally sends one as soon as the socket opens.
func newNIP42KhatruRelay(t *testing.T, challengeOnConnect bool) string {
	t.Helper()
	return newKhatruTestRelay(t, func(relay *khatru.Relay) {
		store := &slicestore.SliceStore{}
		if err := store.Init(); err != nil {
			t.Fatalf("init slice store: %v", err)
		}
		t.Cleanup(store.Close)
		relay.UseEventstore(store, 500)
		if challengeOnConnect {
			relay.OnConnect = khatru.RequestAuth
		}
		relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
			if _, ok := khatru.GetAuthed(ctx); !ok {
				return true, "auth-required: this relay only serves authenticated clients"
			}
			return false, ""
		}
		relay.OnEvent = func(ctx context.Context, _ nostr.Event) (bool, string) {
			if _, ok := khatru.GetAuthed(ctx); !ok {
				return true, "auth-required: this relay only accepts authenticated writes"
			}
			return false, ""
		}
	})
}

// TestRelayClientNIP42AgainstChallengingRelayIsRaceFree drives Authenticate,
// Publish and Query against a relay that challenges on connect. Run with -race:
// the bus must never read the library's NIP-42 state while the relay reader
// goroutine may still be writing it.
func TestRelayClientNIP42AgainstChallengingRelayIsRaceFree(t *testing.T) {
	for _, tc := range []struct {
		name               string
		challengeOnConnect bool
		authenticateFirst  bool
	}{
		{name: "challenge on connect, explicit Authenticate", challengeOnConnect: true, authenticateFirst: true},
		{name: "challenge on connect, auth on demand", challengeOnConnect: true},
		{name: "challenge only with rejections", challengeOnConnect: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer := newFakeSigner(t)
			url := newNIP42KhatruRelay(t, tc.challengeOnConnect)
			bus, err := NewRelayClient([]string{url}, WithRelaySigner(signer), withRelayResubscribeBackoff(fastRelayBackoff), withRelayAdmission(generousTestAdmission()))
			if err != nil {
				t.Fatalf("new relay client: %v", err)
			}
			defer bus.Close()

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if tc.authenticateFirst {
				if err := bus.Authenticate(ctx); err != nil {
					t.Fatalf("Authenticate() error = %v", err)
				}
			}
			event := signedRelayBusEvent(t, signer, 1, "nip-42 "+tc.name)
			if _, err := bus.Publish(ctx, *event); err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			// Two filters become two REQs on the same connection.
			events, err := bus.Query(ctx, []nostr.Filter{{IDs: []nostr.ID{event.ID}}, {Kinds: []nostr.Kind{1}, Limit: 5}})
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if len(events) != 1 || events[0].ID != event.ID {
				t.Fatalf("Query() = %d events, want exactly the published event", len(events))
			}
		})
	}
}
