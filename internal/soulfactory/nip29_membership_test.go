package soulfactory

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestNIP29MembershipAssignPublishesControllerSignedPutUser(t *testing.T) {
	signer := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://groups.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}
	bus := &SoulFactoryRelayBus{
		endpoints:     []relayBusEndpoint{endpoint},
		signer:        signer,
		validateEvent: func(*nostr.Event) bool { return true },
	}
	membership := &nip29Membership{
		signer: signer,
		groups: []NIP29Group{{Relay: endpoint.url, ID: "fleet-dev"}},
		buses:  map[string]*SoulFactoryRelayBus{endpoint.url: bus},
	}

	assigned, err := membership.Assign(t.Context(), target)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != "wss://groups.example'fleet-dev" {
		t.Fatalf("Assign() = %v", assigned)
	}
	if len(endpoint.published) != 1 {
		t.Fatalf("published %d events, want 1", len(endpoint.published))
	}
	select {
	case <-endpoint.authCalls:
	default:
		t.Fatal("NIP-42 authentication was not attempted")
	}
	event := endpoint.published[0]
	if event.Kind != nip29KindPutUser {
		t.Fatalf("event kind = %d, want %d", event.Kind, nip29KindPutUser)
	}
	if event.PubKey.Hex() != signer.pubkey {
		t.Fatalf("event pubkey = %s, want controller %s", event.PubKey.Hex(), signer.pubkey)
	}
	if tagValue(event.Tags, "h") != "fleet-dev" {
		t.Fatalf("event h tag = %v", event.Tags)
	}
	if tagValue(event.Tags, "p") != target {
		t.Fatalf("event p tag = %v", event.Tags)
	}
	if !event.VerifySignature() {
		t.Fatal("event signature is invalid")
	}
}

func TestNIP29MembershipAssignFailsClosedOnRelayRejection(t *testing.T) {
	signer := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://groups.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted"}}
	membership := &nip29Membership{
		signer: signer,
		groups: []NIP29Group{{Relay: endpoint.url, ID: "fleet-ops"}},
		buses: map[string]*SoulFactoryRelayBus{
			endpoint.url: {endpoints: []relayBusEndpoint{endpoint}, signer: signer},
		},
	}

	_, err := membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Assign() error = %v, want relay rejection", err)
	}
}
