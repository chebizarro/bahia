package nostr_test

import (
	"context"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
)

// newAuthRequiredKhatru starts an in-process khatru relay that challenges on
// connect and rejects every REQ from an unauthenticated connection with
// "auth-required:". Like khatru itself it re-sends the AUTH challenge before
// each such rejection, so a burst of rejected REQs is a burst of AUTH frames.
func newAuthRequiredKhatru(t *testing.T) string {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatalf("init slice store: %v", err)
	}
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	relay.OnConnect = khatru.RequestAuth
	relay.OnRequest = func(ctx context.Context, _ nostr.Filter) (bool, string) {
		if _, ok := khatru.GetAuthed(ctx); !ok {
			return true, "auth-required: this relay only serves authenticated clients"
		}
		return false, ""
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// TestRelayAuthHandlerWithOverlappingChallengesIsRaceFree is the regression
// test for the library's NIP-42 state (bahia-irsry.10.2). In the pristine
// library the reader goroutine rewrites Relay.challenge and resets
// Relay.performAuth on every AUTH frame, while AuthHandler goroutines (one per
// frame) and callers of Relay.Auth read them and run performAuth.Do. Under
// -race that is reported as a data race, and resetting the sync.Once while
// another goroutine is inside Do can abort the process with "sync: unlock of
// unlocked mutex". Run with -race.
func TestRelayAuthHandlerWithOverlappingChallengesIsRaceFree(t *testing.T) {
	const (
		rounds     = 10
		concurrent = 16
	)
	secret := nostr.Generate()
	sign := func(_ context.Context, event *nostr.Event) error { return event.Sign(secret) }

	for round := range rounds {
		url := newAuthRequiredKhatru(t)
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		relay, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{
			AuthHandler: func(ctx context.Context, _ *nostr.Relay, event *nostr.Event) error {
				return sign(ctx, event)
			},
		})
		if err != nil {
			cancel()
			t.Fatalf("round %d: connect: %v", round, err)
		}

		// Each REQ is answered with AUTH + CLOSED until the connection is
		// authenticated. The answering goroutines also call Relay.Auth, as a
		// caller recovering from "auth-required:" would.
		var wg sync.WaitGroup
		for i := range concurrent {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sub, err := relay.Subscribe(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(i + 1)}}, nostr.SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
				if err != nil {
					return
				}
				defer sub.Unsub()
				select {
				case <-sub.ClosedReason:
				case <-sub.EndOfStoredEvents:
				case <-ctx.Done():
					return
				}
				_ = relay.Auth(ctx, sign)
			}()
		}
		wg.Wait()

		if err := relay.Auth(ctx, sign); err != nil {
			t.Fatalf("round %d: Auth() after the challenge burst = %v", round, err)
		}
		sub, err := relay.Subscribe(ctx, nostr.Filter{Kinds: []nostr.Kind{1}}, nostr.SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
		if err != nil {
			t.Fatalf("round %d: subscribe after AUTH: %v", round, err)
		}
		select {
		case <-sub.EndOfStoredEvents:
		case reason := <-sub.ClosedReason:
			t.Fatalf("round %d: authenticated REQ was CLOSED: %s", round, reason)
		case <-ctx.Done():
			t.Fatalf("round %d: authenticated REQ got no EOSE", round)
		}
		sub.Unsub()
		_ = relay.Close()
		cancel()
	}
}

// TestRelayAuthResultIsObservable covers the other half of the bahia AUTH
// patch: AuthHandler's outcome reaches AuthResultHandler, a Relay.Auth call
// made while that attempt is in flight returns the same outcome instead of
// starting its own, and a failed attempt does not stop a later one.
func TestRelayAuthResultIsObservable(t *testing.T) {
	secret := nostr.Generate()
	sign := func(_ context.Context, event *nostr.Event) error { return event.Sign(secret) }

	t.Run("handler success", func(t *testing.T) {
		url := newAuthRequiredKhatru(t)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		results := make(chan error, 8)
		relay, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{
			AuthHandler: func(ctx context.Context, _ *nostr.Relay, event *nostr.Event) error {
				return sign(ctx, event)
			},
			AuthResultHandler: func(_ *nostr.Relay, err error) { results <- err },
		})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer relay.Close()

		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("AuthResultHandler got %v, want the relay's OK true", err)
			}
		case <-ctx.Done():
			t.Fatal("AuthResultHandler was not called for the connect-time challenge")
		}
		if err := relay.Auth(ctx, func(context.Context, *nostr.Event) error {
			t.Error("Auth signed again on an authenticated connection")
			return nil
		}); err != nil {
			t.Fatalf("Auth() on an authenticated connection = %v", err)
		}
		// Further challenges on an authenticated connection start nothing.
		sub, err := relay.Subscribe(ctx, nostr.Filter{Kinds: []nostr.Kind{1}}, nostr.SubscriptionOptions{MaxWaitForEOSE: math.MaxInt64})
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer sub.Unsub()
		select {
		case <-sub.EndOfStoredEvents:
		case reason := <-sub.ClosedReason:
			t.Fatalf("authenticated REQ CLOSED: %s", reason)
		case <-ctx.Done():
			t.Fatal("no EOSE")
		}
		select {
		case err := <-results:
			t.Fatalf("unexpected second AUTH attempt: %v", err)
		default:
		}
	})

	t.Run("handler failure is reported and joined", func(t *testing.T) {
		url := newAuthRequiredKhatru(t)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		started := make(chan struct{})
		release := make(chan struct{})
		results := make(chan error, 8)
		relay, err := nostr.RelayConnect(ctx, url, nostr.RelayOptions{
			AuthHandler: func(context.Context, *nostr.Relay, *nostr.Event) error {
				close(started)
				<-release
				return context.DeadlineExceeded
			},
			AuthResultHandler: func(_ *nostr.Relay, err error) { results <- err },
		})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer relay.Close()

		// The connect-time challenge started the handler, which is blocked. An
		// Auth call now must join that attempt rather than sign its own: with
		// an already cancelled context it returns the cancellation unsigned.
		<-started
		cancelled, cancelNow := context.WithCancel(ctx)
		cancelNow()
		if err := relay.Auth(cancelled, func(context.Context, *nostr.Event) error {
			t.Error("Auth started a second attempt while one was in flight")
			return nil
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Auth() while an attempt is in flight = %v, want the caller's cancellation", err)
		}
		close(release)
		if err := <-results; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("AuthResultHandler got %v, want the handler's error", err)
		}
		// A failed attempt leaves the connection able to authenticate.
		if err := relay.Auth(ctx, sign); err != nil {
			t.Fatalf("Auth() after a failed attempt = %v", err)
		}
	})
}
