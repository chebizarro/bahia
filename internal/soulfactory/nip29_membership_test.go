package soulfactory

import (
	"strings"
	"testing"
)

func TestNIP29MembershipAssignPublishesControllerSignedPutUser(t *testing.T) {
	signer := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, WithRelaySigner(signer))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	membership := &nip29Membership{
		signer:       signer,
		groups:       []NIP29Group{{Relay: endpoint.url, ID: "fleet-dev"}},
		relayClients: map[string]*RelayClient{endpoint.url: bus},
	}

	assigned, err := membership.Assign(t.Context(), target)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != endpoint.url+"'fleet-dev" {
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
	endpoint := newFakeRelayEndpoint(t)
	endpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted"}}
	bus, err := newRelayClientFromEndpoints([]*fakeRelayEndpoint{endpoint}, WithRelaySigner(signer))
	if err != nil {
		t.Fatalf("new relay client: %v", err)
	}
	membership := &nip29Membership{
		signer:       signer,
		groups:       []NIP29Group{{Relay: endpoint.url, ID: "fleet-ops"}},
		relayClients: map[string]*RelayClient{endpoint.url: bus},
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Assign() error = %v, want relay rejection", err)
	}
}
