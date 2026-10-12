package relayguard

import (
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestIsWellKnownPublic(t *testing.T) {
	for relay, want := range map[string]bool{
		"wss://relay.damus.io":           true,
		"wss://RELAY.DAMUS.IO/":          true,
		"wss://nos.lol":                  true,
		"wss://relay.primal.net":         true,
		"wss://nostr.wine":               true,
		"wss://relay.nostr.band":         true,
		"relay.snort.social":             true,
		"wss://bahia.sharegap.net/relay": false,
		"wss://relay.sharegap.net":       false,
		"ws://relay:3334/relay":          false,
		"wss://notdamus.io":              false,
		"wss://damus.io.example.com":     false,
	} {
		if got := IsWellKnownPublic(relay); got != want {
			t.Errorf("IsWellKnownPublic(%q) = %v, want %v", relay, got, want)
		}
	}
}

func TestNewRejectsPublicControlledRelays(t *testing.T) {
	service := nostr.Generate().Public()
	if _, err := New(service, []string{"wss://relay.sharegap.net", "wss://relay.damus.io"}); err == nil || !strings.Contains(err.Error(), "relay.damus.io") {
		t.Fatalf("New with a public relay: err = %v, want refusal naming it", err)
	}
	empty, err := New(service, nil)
	if err != nil {
		t.Fatalf("New with no controlled relays: %v", err)
	}
	if empty.Check("wss://relay.sharegap.net", nostr.Event{PubKey: service, Kind: kinds.CASControlState}) == nil {
		t.Fatal("an empty controlled set must refuse every control-plane publication")
	}
	if _, err := New(nostr.ZeroPK, []string{"wss://relay.sharegap.net"}); err == nil {
		t.Fatal("New without a service pubkey succeeded")
	}
}

func TestCheckRefusesServiceControlPlaneEventsOutsideControlledRelays(t *testing.T) {
	service := nostr.Generate().Public()
	guard, err := New(service, []string{"wss://bahia.sharegap.net/relay", "wss://relay.sharegap.net/"})
	if err != nil {
		t.Fatal(err)
	}
	other := nostr.Generate().Public()

	cases := []struct {
		name    string
		relay   string
		event   nostr.Event
		refused bool
	}{
		{"cp-state to a public relay", "wss://relay.damus.io", nostr.Event{PubKey: service, Kind: kinds.CASControlState}, true},
		{"audit to an unlisted relay", "wss://relay.example.com", nostr.Event{PubKey: service, Kind: kinds.CASAudit}, true},
		{"identity status to an unlisted relay", "wss://relay.example.com", nostr.Event{PubKey: service, Kind: kinds.BahiaIdentityDefinition}, true},
		{"readiness status to an unlisted relay", "wss://relay.example.com", nostr.Event{PubKey: service, Kind: kinds.BahiaReadinessStatus}, true},
		{"cp-state to a controlled relay, normalized", "wss://RELAY.sharegap.net", nostr.Event{PubKey: service, Kind: kinds.CASControlState}, false},
		{"cp-state signed by another key", "wss://relay.damus.io", nostr.Event{PubKey: other, Kind: kinds.CASControlState}, false},
		{"plaintext ContextVM response to an unlisted relay", "wss://relay.example.com", nostr.Event{PubKey: service, Kind: kinds.ContextVMMessage}, true},
		{"service deletion request to a public relay", "wss://relay.damus.io", nostr.Event{PubKey: service, Kind: nostr.KindDeletion}, true},
		{"gift wrap outside a control-plane pool", "wss://relay.damus.io", nostr.Event{PubKey: other, Kind: kinds.ContextVMGiftWrap}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guard.Check(tc.relay, tc.event)
			if !tc.refused {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			var refused *RefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("Check = %v, want *RefusedError", err)
			}
			if !strings.HasPrefix(refused.Reason(), "blocked: ") {
				t.Fatalf("Reason = %q, want a blocked: OK message", refused.Reason())
			}
		})
	}
}

func TestNilGuardAllowsEverything(t *testing.T) {
	var guard *Guard
	if err := guard.Check("wss://relay.damus.io", nostr.Event{Kind: kinds.CASControlState}); err != nil {
		t.Fatalf("nil guard Check = %v", err)
	}
}

func TestCheckControlPlanePoolConfinesGiftWraps(t *testing.T) {
	service := nostr.Generate().Public()
	guard, err := New(service, []string{"wss://bahia.sharegap.net/relay"})
	if err != nil {
		t.Fatal(err)
	}
	ephemeral := nostr.Generate().Public()
	for _, kind := range []nostr.Kind{kinds.ContextVMGiftWrap, kinds.ContextVMEphemeralGiftWrap} {
		ev := nostr.Event{PubKey: ephemeral, Kind: kind}
		if err := guard.CheckControlPlanePool("wss://relay.damus.io", ev); err == nil {
			t.Fatalf("control-plane pool sent gift wrap kind %d to a public relay", kind)
		}
		if err := guard.CheckControlPlanePool("wss://bahia.sharegap.net/relay", ev); err != nil {
			t.Fatalf("gift wrap kind %d to a controlled relay: %v", kind, err)
		}
	}
	if err := guard.CheckControlPlanePool("wss://relay.damus.io", nostr.Event{PubKey: service, Kind: kinds.CASAudit}); err == nil {
		t.Fatal("CheckControlPlanePool must also apply Check")
	}
}

func TestRefusalsAreCountedAndReportedOncePerRelayKind(t *testing.T) {
	service := nostr.Generate().Public()
	guard, err := New(service, []string{"wss://bahia.sharegap.net/relay"})
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{PubKey: service, Kind: kinds.CASControlState}
	var first []bool
	for range 3 {
		var refused *RefusedError
		if !errors.As(guard.Check("wss://relay.damus.io", ev), &refused) {
			t.Fatal("expected a refusal")
		}
		first = append(first, refused.FirstForRelayKind)
	}
	var other *RefusedError
	errors.As(guard.Check("wss://nos.lol", ev), &other)
	if !first[0] || first[1] || first[2] || !other.FirstForRelayKind {
		t.Fatalf("first-report flags = %v, other relay = %v", first, other.FirstForRelayKind)
	}
	if guard.Refusals() != 4 {
		t.Fatalf("Refusals() = %d, want 4", guard.Refusals())
	}
}
