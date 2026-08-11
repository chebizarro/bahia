package soulfactory

import (
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestConcordInviteDeliversToRecipientDMRelayInbox(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	inboxRelay := newFakeRelayEndpoint("wss://inbox.example")
	inboxRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	queueConcordInboxLookup(communityRelay, concordDMRelayList(t, recipient, "wss://inbox.example"))
	queueConcordInboxLookup(inboxRelay)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, inboxRelay)
	if _, err := membership.Assign(t.Context(), recipient.pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}

	if len(communityRelay.published) != 1 {
		t.Fatalf("community relay published = %d, want 1", len(communityRelay.published))
	}
	if len(inboxRelay.published) != 1 {
		t.Fatalf("inbox relay published = %d, want 1", len(inboxRelay.published))
	}
	if !validConcordGiftWrap(inboxRelay.published[0], recipient.pubkey) {
		t.Fatal("inbox delivery is not a valid CORD-05 giftwrap")
	}
	if inboxRelay.published[0].ID != communityRelay.published[0].ID {
		t.Fatal("inbox and community relays received different invites")
	}
}

func TestConcordInviteFallsBackToNIP65ReadRelays(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	inboxRelay := newFakeRelayEndpoint("wss://inbox.example")
	inboxRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	writeOnly := newFakeRelayEndpoint("wss://write-only.example")
	relayList := signedConcordEvent(t, recipient, concordRelayListKind, nostr.Tags{
		{"r", "wss://inbox.example", "read"},
		{"r", "wss://write-only.example", "write"},
	})
	queueConcordInboxLookup(communityRelay, relayList)
	queueConcordInboxLookup(inboxRelay)
	queueConcordInboxLookup(writeOnly)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, inboxRelay, writeOnly)
	if _, err := membership.Assign(t.Context(), recipient.pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}

	if len(inboxRelay.published) != 1 {
		t.Fatalf("NIP-65 read relay published = %d, want 1", len(inboxRelay.published))
	}
	if len(writeOnly.published) != 0 {
		t.Fatal("invite was published to a write-only relay")
	}
}

func TestConcordInvitePrefersDMRelayListOverNIP65(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	dmRelay := newFakeRelayEndpoint("wss://inbox.example")
	dmRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	nip65Relay := newFakeRelayEndpoint("wss://read.example")
	queueConcordInboxLookup(communityRelay,
		concordDMRelayList(t, recipient, "wss://inbox.example"),
		signedConcordEvent(t, recipient, concordRelayListKind, nostr.Tags{{"r", "wss://read.example", "read"}}),
	)
	queueConcordInboxLookup(dmRelay)
	queueConcordInboxLookup(nip65Relay)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, dmRelay, nip65Relay)
	if _, err := membership.Assign(t.Context(), recipient.pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(dmRelay.published) != 1 || len(nip65Relay.published) != 0 {
		t.Fatalf("kind-10050 did not take precedence: dm=%d nip65=%d", len(dmRelay.published), len(nip65Relay.published))
	}
}

func TestConcordInviteIgnoresForgedRelayLists(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	impostor := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	attacker := newFakeRelayEndpoint("wss://attacker.example")

	forgedAuthor := concordDMRelayList(t, impostor, "wss://attacker.example")
	tampered := concordDMRelayList(t, recipient, "wss://inbox.example")
	tampered.Tags = nostr.Tags{{"relay", "wss://attacker.example"}}
	queueConcordInboxLookup(communityRelay, forgedAuthor, tampered)
	queueConcordInboxLookup(attacker)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, attacker)
	if _, err := membership.Assign(t.Context(), recipient.pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(attacker.published) != 0 {
		t.Fatal("invite was delivered to a relay list Bahia could not verify")
	}
	if len(communityRelay.published) != 1 {
		t.Fatalf("community relay published = %d, want 1", len(communityRelay.published))
	}
}

func TestConcordInviteFailsClosedWhenNoInboxRelayAccepts(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	inboxRelay := newFakeRelayEndpoint("wss://inbox.example")
	inboxRelay.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted: not a paying member"}}
	queueConcordInboxLookup(communityRelay, concordDMRelayList(t, recipient, "wss://inbox.example"))
	queueConcordInboxLookup(inboxRelay)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, inboxRelay)
	_, err := membership.Assign(t.Context(), recipient.pubkey)
	if err == nil || !strings.Contains(err.Error(), "no inbox relay accepted the invite") {
		t.Fatalf("Assign() error = %v", err)
	}
}

func TestConcordInviteSucceedsWhenOneInboxRelayAccepts(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	recipient := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	dead := newFakeRelayEndpoint("wss://dead.example")
	dead.publishResults = []RelayPublishResult{{Accepted: false, Reason: "blocked"}}
	live := newFakeRelayEndpoint("wss://live.example")
	live.publishResults = []RelayPublishResult{{Accepted: true}}
	queueConcordInboxLookup(communityRelay, concordDMRelayList(t, recipient, "wss://dead.example", "wss://live.example"))
	queueConcordInboxLookup(dead)
	queueConcordInboxLookup(live)

	membership := newConcordInboxMembership(t, community, staff, communityRelay, dead, live)
	if _, err := membership.Assign(t.Context(), recipient.pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(live.published) != 1 {
		t.Fatalf("live inbox relay published = %d, want 1", len(live.published))
	}
}

func TestBoundConcordInboxRelaysRejectsHostileLists(t *testing.T) {
	hostile := []string{
		"http://not-a-relay.example",
		"wss://user:pass@credentials.example",
		"not a url at all",
		"",
		"   ",
		"wss://good.example",
		"wss://good.example/",
	}
	for i := range 10 {
		hostile = append(hostile, "wss://flood-"+string(rune('a'+i))+".example")
	}

	bounded := boundConcordInboxRelays(hostile)
	if len(bounded) != concordMaxInboxRelays {
		t.Fatalf("bounded relays = %d, want the %d cap", len(bounded), concordMaxInboxRelays)
	}
	if bounded[0] != "wss://good.example" {
		t.Fatalf("bounded relays = %#v, want the malformed entries dropped", bounded)
	}
	for _, relay := range bounded {
		if !validConcordRelayURL(relay) {
			t.Fatalf("bounded relay %q is not a usable relay URL", relay)
		}
	}
	if len(boundConcordInboxRelays(nil)) != 0 {
		t.Fatal("an absent relay list must resolve to an empty inbox")
	}
}

func TestValidConcordRelayURLRejectsNonRelaySchemes(t *testing.T) {
	for _, relay := range []string{"http://relay.example", "https://relay.example", "wss://", "ws://user@host.example", "file:///etc/passwd", ""} {
		if validConcordRelayURL(relay) {
			t.Fatalf("validConcordRelayURL(%q) = true", relay)
		}
	}
	for _, relay := range []string{"ws://relay.example", "wss://relay.example", "wss://relay.example/nostr"} {
		if !validConcordRelayURL(relay) {
			t.Fatalf("validConcordRelayURL(%q) = false", relay)
		}
	}
}

func TestConcordInboxKeepsLatestRelayList(t *testing.T) {
	recipient := newFakeSigner(t)
	older := concordDMRelayList(t, recipient, "wss://old.example")
	newer := concordDMRelayList(t, recipient, "wss://new.example")
	older.CreatedAt = newer.CreatedAt - 60
	if err := recipient.Sign(t.Context(), older); err != nil {
		t.Fatalf("re-sign older list: %v", err)
	}

	if kept := laterConcordEvent(older, newer); kept != newer {
		t.Fatal("laterConcordEvent kept the stale relay list")
	}
	if kept := laterConcordEvent(newer, older); kept != newer {
		t.Fatal("laterConcordEvent replaced a newer list with a stale one")
	}
	if kept := laterConcordEvent(nil, older); kept != older {
		t.Fatal("laterConcordEvent dropped the only candidate")
	}
}

func TestConcordRotationRedistributesToSurvivorInbox(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	survivor := newFakeSigner(t)
	community := concordTestCommunity(t, nil)
	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, staff, string(community.InviteBundle))

	communityRelay := newFakeRelayEndpoint("wss://community.example")
	communityRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	inboxRelay := newFakeRelayEndpoint("wss://inbox.example")
	inboxRelay.publishResults = []RelayPublishResult{{Accepted: true}}
	queueConcordInboxLookup(communityRelay, concordDMRelayList(t, survivor, "wss://inbox.example"))
	queueConcordInboxLookup(inboxRelay)

	membership := newConcordInboxMembership(t,
		ConcordCommunity{CommunityID: community.CommunityID, SealedBundlePath: path},
		staff, communityRelay, inboxRelay)

	// A survivor is an existing member, not an agent Bahia just provisioned:
	// they may already read somewhere other than the community relays.
	if _, err := membership.Rotate(t.Context(), ConcordRotation{
		CommunityID: community.CommunityID,
		ChannelIDs:  []string{strings.Repeat("3", 64)},
		Recipients:  []string{survivor.pubkey},
	}); err != nil {
		t.Fatalf("Rotate() error = %v", err)
	}

	if len(inboxRelay.published) != 1 {
		t.Fatalf("survivor inbox published = %d, want the rotated invite", len(inboxRelay.published))
	}
	record, err := membership.communities[0].custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	delivered := concordUnwrapInvite(t, inboxRelay.published[0], survivor)
	if delivered != string(record.Bundle) {
		t.Fatal("survivor inbox received material other than the rotated bundle")
	}
	if strings.Contains(delivered, strings.Repeat("4", 64)) {
		t.Fatal("survivor inbox received the severed channel key")
	}
}

func newConcordInboxMembership(t *testing.T, community ConcordCommunity, staff fakeConcordSigner, endpoints ...*fakeRelayEndpoint) *concordMembership {
	t.Helper()
	busEndpoints := make([]relayBusEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		busEndpoints = append(busEndpoints, endpoint)
	}
	bus, err := newSoulFactoryRelayBusFromEndpoints(busEndpoints, WithRelayBusSigner(staff))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership, err := newConcordMembership([]ConcordCommunity{community}, staff, bus)
	if err != nil {
		t.Fatalf("newConcordMembership() error = %v", err)
	}
	return membership
}

func concordDMRelayList(t *testing.T, author fakeSigner, relays ...string) *nostr.Event {
	t.Helper()
	tags := make(nostr.Tags, 0, len(relays))
	for _, relay := range relays {
		tags = append(tags, nostr.Tag{"relay", relay})
	}
	return signedConcordEvent(t, author, concordDMRelayListKind, tags)
}

func signedConcordEvent(t *testing.T, author fakeSigner, kind nostr.Kind, tags nostr.Tags) *nostr.Event {
	t.Helper()
	event := &nostr.Event{Kind: kind, CreatedAt: nostr.Now(), Tags: tags}
	if err := author.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}
