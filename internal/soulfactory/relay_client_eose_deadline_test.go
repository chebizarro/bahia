package soulfactory

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/khatru"
)

// relayBusCallerDeadline is the deadline each test caller owns. The assertions
// only need the call to return well within the test timeout.
const relayBusCallerDeadline = 200 * time.Millisecond

// newSilentKhatruRelay accepts every REQ and never sends EOSE: its stored-event
// query blocks until the client goes away. It sends no events, so tearing the
// subscription down at the deadline does not touch the pinned library's
// dispatch-versus-close race (bahia-irsry.17).
func newSilentKhatruRelay(t *testing.T) string {
	t.Helper()
	return newKhatruTestRelay(t, func(relay *khatru.Relay) {
		relay.QueryStored = func(ctx context.Context, _ nostr.Filter) iter.Seq[nostr.Event] {
			return func(func(nostr.Event) bool) { <-ctx.Done() }
		}
	})
}

func assertRelayBusDeadlinePartial(t *testing.T, err error, wantPendingRelay string) {
	t.Helper()
	var incomplete *RelayReadIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *RelayReadIncompleteError", err)
	}
	if !errors.Is(err, ErrRelayReadIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want ErrRelayReadIncomplete wrapping context.DeadlineExceeded", err)
	}
	for _, relay := range incomplete.Relays {
		if relay.RelayURL == wantPendingRelay && relay.Status == RelayStoredEventsPending {
			return
		}
	}
	t.Fatalf("incomplete relays = %+v, want %s pending", incomplete.Relays, wantPendingRelay)
}

// Over the wire, one relay answers with EOSE and the other never does. Query
// returns at the caller's deadline with the answering relay's events, flagged
// as a partial read rather than a complete one.
func TestRelayClientQueryAgainstRelayThatNeverSendsEOSEIsBoundedAndPartial(t *testing.T) {
	signer := newFakeSigner(t)
	event := signedRelayBusEvent(t, signer, 1, "stored on the answering relay")
	answering := newKhatruTestRelay(t, func(relay *khatru.Relay) {
		relay.QueryStored = func(context.Context, nostr.Filter) iter.Seq[nostr.Event] {
			return func(yield func(nostr.Event) bool) { yield(*event) }
		}
	})
	silent := newSilentKhatruRelay(t)
	bus, err := NewRelayClient([]string{answering, silent}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(t.Context(), relayBusCallerDeadline)
	defer cancel()
	events, err := bus.Query(ctx, []nostr.Filter{{Kinds: []nostr.Kind{1}}})
	assertRelayBusDeadlinePartial(t, err, normalizeSoulRelays([]string{silent})[0])
	if len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("partial events = %d, want the event already received", len(events))
	}
}

// A context without a deadline gets the bus bound; the caller's own earlier
// deadline is kept.
func TestBoundRelayBusWaitKeepsCallerDeadline(t *testing.T) {
	unbounded, cancel := boundRelayWait(context.Background(), time.Minute)
	defer cancel()
	if _, ok := unbounded.Deadline(); !ok {
		t.Fatal("boundRelayWait() left a deadline-free context unbounded")
	}
	callerDeadline := time.Now().Add(time.Second)
	owned, cancelOwned := context.WithDeadline(context.Background(), callerDeadline)
	defer cancelOwned()
	bounded, cancel := boundRelayWait(owned, time.Minute)
	defer cancel()
	if deadline, _ := bounded.Deadline(); !deadline.Equal(callerDeadline) {
		t.Fatalf("deadline = %v, want caller deadline %v", deadline, callerDeadline)
	}
}

// newSilentRelayClient returns a bus whose only relay accepts every REQ and never
// answers with EOSE or CLOSED.
func newSilentRelayClient(t *testing.T) (*RelayClient, string) {
	t.Helper()
	endpoint := newFakeRelayEndpoint(t)
	for i := 0; i < cap(endpoint.subscribeQueue); i++ {
		endpoint.subscribeQueue <- newFakeRelaySubscription()
	}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, withRelayResubscribeBackoff(fastRelayBackoff))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	return bus, endpoint.url
}

// Every bus stored-event reader must end at its caller's deadline and report
// the read as partial, never as an empty or complete answer.
func TestRelayClientCallersReportPartialWhenRelayNeverSendsEOSE(t *testing.T) {
	signer := newFakeSigner(t)
	someID := soulTestID("deadline")
	newReactor := func(bus *RelayClient) *Reactor {
		reactor := NewReactor(Config{
			Relays:             []string{"wss://silent.example"},
			AuthorizedPubkeys:  []string{signer.pubkey},
			SoulFactoryPubkey:  signer.pubkey,
			FleetConfigEnabled: true,
		}, nil, signer, slog.Default())
		reactor.relayClient = bus
		return reactor
	}
	callers := map[string]func(context.Context, *RelayClient) error{
		"bus Query": func(ctx context.Context, bus *RelayClient) error {
			_, err := bus.Query(ctx, []nostr.Filter{{Kinds: []nostr.Kind{1}}})
			return err
		},
		"fleet reconcile souls": func(ctx context.Context, bus *RelayClient) error {
			_, err := newReactor(bus).listFleetReconcileSouls(ctx)
			return err
		},
		"fleet config revision": func(ctx context.Context, bus *RelayClient) error {
			_, err := newReactor(bus).getFleetConfigRevision(ctx, someID.Hex())
			return err
		},
		"provisioning fleet config": func(ctx context.Context, bus *RelayClient) error {
			_, err := newReactor(bus).getProvisioningFleetConfig(ctx)
			return err
		},
		"provisioning template": func(ctx context.Context, bus *RelayClient) error {
			_, err := newReactor(bus).getProvisioningTemplate(ctx, "template-ref")
			return err
		},
		"provisioning result idempotency": func(ctx context.Context, bus *RelayClient) error {
			_, err := newReactor(bus).findExistingProvisioningResult(ctx, signedRelayBusEvent(t, signer, 1, "request"))
			return err
		},
		"lifecycle terminal result": func(ctx context.Context, bus *RelayClient) error {
			handler := &LifecycleHandler{reactor: newReactor(bus)}
			_, err := handler.findExistingTerminalResult(ctx, someID.Hex())
			return err
		},
		"communikeys definition": func(ctx context.Context, bus *RelayClient) error {
			membership := &communikeysMembership{relayClient: bus}
			_, err := membership.latestDefinition(ctx, communikeysCommunityTarget{owner: soulTestPubKey("owner"), communityID: "community"})
			return err
		},
		"concord inbox": func(ctx context.Context, bus *RelayClient) error {
			membership := &concordMembership{relayClient: bus}
			_, err := membership.resolveConcordInbox(ctx, soulTestPubKey("recipient"))
			return err
		},
		"soul client collect": func(ctx context.Context, bus *RelayClient) error {
			client := &NostrClient{transport: bus}
			_, err := client.collectEvents(ctx, "test", []nostr.Filter{{Kinds: []nostr.Kind{1}}})
			return err
		},
		"runtime adapter collect": func(ctx context.Context, bus *RelayClient) error {
			_, err := collectRuntimeAdapterEvents(ctx, slog.Default(), "test", bus, []nostr.Filter{{Kinds: []nostr.Kind{1}}})
			return err
		},
		"runtime validation load": func(ctx context.Context, bus *RelayClient) error {
			source := &RelayRuntimeValidationEventSource{relayClient: bus}
			_, err := source.LoadEvents(ctx, []string{someID.Hex()})
			return err
		},
	}
	for name, call := range callers {
		t.Run(name, func(t *testing.T) {
			bus, relay := newSilentRelayClient(t)
			ctx, cancel := context.WithTimeout(t.Context(), relayBusCallerDeadline)
			defer cancel()
			assertRelayBusDeadlinePartial(t, call(ctx, bus), relay)
		})
	}
}
