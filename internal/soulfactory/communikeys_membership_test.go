package soulfactory

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestCommunikeysMembershipAssignRepublishesControllerOwnedProfileList(t *testing.T) {
	admin := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	existingMember := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}

	original := signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{
		{"d", "Apps"},
		{"title", "Applications"},
		{"p", existingMember, "wss://member.example"},
	}, "preserved content")
	queueCommunikeysQuery(endpoint, original)

	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusSigner(admin),
	)
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Apps"}}},
		bus:         bus,
	}

	assigned, err := membership.Assign(t.Context(), target)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	wantCoordinate := "30000:" + admin.pubkey + ":Apps"
	if len(assigned) != 1 || assigned[0] != wantCoordinate {
		t.Fatalf("Assign() = %v, want [%s]", assigned, wantCoordinate)
	}
	select {
	case <-endpoint.authCalls:
	default:
		t.Fatal("NIP-42 authentication was not attempted")
	}
	select {
	case filters := <-endpoint.subscribeCalls:
		if len(filters) != 1 {
			t.Fatalf("subscription filters = %#v", filters)
		}
		filter := filters[0]
		if len(filter.Kinds) != 1 || filter.Kinds[0] != communikeysProfileListKind {
			t.Fatalf("filter kinds = %#v", filter.Kinds)
		}
		if len(filter.Authors) != 1 || filter.Authors[0].Hex() != admin.pubkey {
			t.Fatalf("filter authors = %#v", filter.Authors)
		}
		if got := filter.Tags["d"]; len(got) != 1 || got[0] != "Apps" {
			t.Fatalf("filter d tags = %#v", got)
		}
		if filter.Limit != 1 {
			t.Fatalf("filter limit = %d, want 1", filter.Limit)
		}
	default:
		t.Fatal("profile-list subscription was not attempted")
	}

	if len(endpoint.published) != 1 {
		t.Fatalf("published %d events, want 1", len(endpoint.published))
	}
	replacement := endpoint.published[0]
	if replacement.Kind != communikeysProfileListKind {
		t.Fatalf("replacement kind = %d", replacement.Kind)
	}
	if replacement.PubKey.Hex() != admin.pubkey {
		t.Fatalf("replacement pubkey = %s, want admin %s", replacement.PubKey.Hex(), admin.pubkey)
	}
	if replacement.Content != original.Content {
		t.Fatalf("replacement content = %q, want %q", replacement.Content, original.Content)
	}
	if !tagHasValue(replacement.Tags, "d", "Apps") ||
		!tagHasValue(replacement.Tags, "title", "Applications") ||
		!tagHasValue(replacement.Tags, "p", existingMember) ||
		!tagHasValue(replacement.Tags, "p", target) {
		t.Fatalf("replacement did not preserve and append tags: %#v", replacement.Tags)
	}
	if replacement.CreatedAt <= original.CreatedAt {
		t.Fatalf("replacement created_at = %d, want newer than %d", replacement.CreatedAt, original.CreatedAt)
	}
	if !replacement.CheckID() || !replacement.VerifySignature() {
		t.Fatal("replacement event is not validly controller-signed")
	}
}

func TestCommunikeysMembershipAssignIsIdempotentForExistingMember(t *testing.T) {
	admin := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	original := signedCommunikeysProfileList(t, admin, "Chat", nostr.Tags{
		{"d", "Chat"},
		{"p", target},
	}, "")
	queueCommunikeysQuery(endpoint, original)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(admin))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Chat"}}},
		bus:         bus,
	}

	assigned, err := membership.Assign(t.Context(), target)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 {
		t.Fatalf("Assign() = %v", assigned)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events for existing member, want 0", len(endpoint.published))
	}
}

func TestCommunikeysMembershipAssignSelectsLatestProfileListFromQueryResults(t *testing.T) {
	admin := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}

	older := signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{{"d", "Apps"}, {"title", "Old"}}, "")
	older.CreatedAt = nostr.Now() - 2
	if err := admin.Sign(t.Context(), older); err != nil {
		t.Fatalf("resign older list: %v", err)
	}
	newer := signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{{"d", "Apps"}, {"title", "Current"}}, "")
	queueCommunikeysQuery(endpoint, newer, older)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(admin))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Apps"}}},
		bus:         bus,
	}

	if _, err := membership.Assign(t.Context(), target); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(endpoint.published) != 1 || !tagHasValue(endpoint.published[0].Tags, "title", "Current") || tagHasValue(endpoint.published[0].Tags, "title", "Old") {
		t.Fatalf("replacement did not use latest profile list: %#v", endpoint.published)
	}
}

func TestCommunikeysMembershipAssignRetriesPublishAfterAuthRace(t *testing.T) {
	admin := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{
		{Accepted: false, Reason: "auth-required: challenge pending"},
		{Accepted: true},
	}
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{{"d", "Apps"}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(admin))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Apps"}}},
		bus:         bus,
	}

	if _, err := membership.Assign(t.Context(), newFakeSigner(t).pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if endpoint.publishCalls != 2 {
		t.Fatalf("publish calls = %d, want auth-race retry", endpoint.publishCalls)
	}
}

func TestCommunikeysMembershipAssignFailsClosedWithoutAdminProfileList(t *testing.T) {
	admin := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	queueCommunikeysQuery(endpoint, nil)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(admin))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"General"}}},
		bus:         bus,
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "no valid admin-owned profile list") {
		t.Fatalf("Assign() error = %v, want missing admin list", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events without an admin list", len(endpoint.published))
	}
}

func TestCommunikeysMembershipAssignFailsClosedOnRelayRejection(t *testing.T) {
	admin := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted"}}
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{{"d", "Apps"}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(admin))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      admin,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Apps"}}},
		bus:         bus,
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Assign() error = %v, want relay rejection", err)
	}
}

func TestCommunikeysMembershipAssignRejectsNonOwnerController(t *testing.T) {
	admin := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, admin, "Apps", nostr.Tags{{"d", "Apps"}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := &communikeysMembership{
		signer:      controller,
		communities: []CommunikeysCommunity{{Pubkey: admin.pubkey, Sections: []string{"Apps"}}},
		bus:         bus,
	}

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "does not own configured community") {
		t.Fatalf("Assign() error = %v, want controller ownership failure", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events with non-owner controller", len(endpoint.published))
	}
}

func signedCommunikeysProfileList(t *testing.T, signer fakeSigner, section string, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		Kind:      communikeysProfileListKind,
		CreatedAt: nostr.Now() - 1,
		Tags:      cloneCommunikeysTags(tags),
		Content:   content,
	}
	if !tagHasValue(event.Tags, "d", section) {
		event.Tags = append(event.Tags, nostr.Tag{"d", section})
	}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign profile list: %v", err)
	}
	return event
}

func queueCommunikeysQuery(endpoint *fakeRelayEndpoint, events ...*nostr.Event) {
	subscription := newFakeRelaySubscription()
	for _, event := range events {
		if event != nil {
			subscription.events <- event
		}
	}
	close(subscription.eose)
	endpoint.subscribeQueue <- subscription
}
