package soulfactory

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

// opaqueCommunityID is deliberately not a valid secp256k1 x-coordinate: under
// Communikeys V2 the community ID is opaque and MUST NOT be curve-checked.
const opaqueCommunityID = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

func TestCommunikeysMembershipAssignRepublishesDelegatedAuthorProfileList(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	existingMember := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}

	identifier := opaqueCommunityID + "-apps"
	coordinate := "30000:" + controller.pubkey + ":" + identifier
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"name", "Fleet"},
		{"content", "Apps"},
		{"k", "1"},
		{"a", coordinate},
	}))
	original := signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{
		{"d", identifier},
		{"title", "Applications"},
		{"p", existingMember, "wss://member.example"},
	}, "preserved content")
	queueCommunikeysQuery(endpoint, original)

	bus, err := newSoulFactoryRelayBusFromEndpoints(
		[]relayBusEndpoint{endpoint},
		WithRelayBusSigner(controller),
	)
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"apps"},
	})

	assigned, err := membership.Assign(t.Context(), target)
	if err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(assigned) != 1 || assigned[0] != coordinate {
		t.Fatalf("Assign() = %v, want [%s]", assigned, coordinate)
	}
	select {
	case <-endpoint.authCalls:
	default:
		t.Fatal("NIP-42 authentication was not attempted")
	}
	select {
	case filters := <-endpoint.subscribeCalls:
		if len(filters) != 1 {
			t.Fatalf("definition subscription filters = %#v", filters)
		}
		filter := filters[0]
		if len(filter.Kinds) != 1 || filter.Kinds[0] != communikeysDefinitionKind {
			t.Fatalf("definition filter kinds = %#v", filter.Kinds)
		}
		if len(filter.Authors) != 1 || filter.Authors[0].Hex() != owner.pubkey {
			t.Fatalf("definition filter authors = %#v", filter.Authors)
		}
		if got := filter.Tags["d"]; len(got) != 1 || got[0] != opaqueCommunityID {
			t.Fatalf("definition filter d tags = %#v", got)
		}
	default:
		t.Fatal("definition subscription was not attempted")
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
		if len(filter.Authors) != 1 || filter.Authors[0].Hex() != controller.pubkey {
			t.Fatalf("filter authors = %#v, want delegated list author", filter.Authors)
		}
		if got := filter.Tags["d"]; len(got) != 1 || got[0] != identifier {
			t.Fatalf("filter d tags = %#v, want section-scoped identifier", got)
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
	if replacement.PubKey.Hex() != controller.pubkey {
		t.Fatalf("replacement pubkey = %s, want delegated list author %s", replacement.PubKey.Hex(), controller.pubkey)
	}
	if replacement.Content != original.Content {
		t.Fatalf("replacement content = %q, want %q", replacement.Content, original.Content)
	}
	if !tagHasValue(replacement.Tags, "d", identifier) ||
		!tagHasValue(replacement.Tags, "title", "Applications") ||
		!tagHasValue(replacement.Tags, "p", existingMember) ||
		!tagHasValue(replacement.Tags, "p", target) {
		t.Fatalf("replacement did not preserve and append tags: %#v", replacement.Tags)
	}
	if replacement.CreatedAt <= original.CreatedAt {
		t.Fatalf("replacement created_at = %d, want newer than %d", replacement.CreatedAt, original.CreatedAt)
	}
	if !replacement.CheckID() || !replacement.VerifySignature() {
		t.Fatal("replacement event is not validly signed by the list author")
	}
}

func TestCommunikeysMembershipAssignIsIdempotentForExistingMember(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	identifier := opaqueCommunityID + "-chat"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Chat"},
		{"k", "9"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{
		{"d", identifier},
		{"p", target},
	}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"chat"},
	})

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
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	target := newFakeSigner(t).pubkey
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}

	identifier := opaqueCommunityID + "-apps"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Apps"},
		{"k", "1"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	older := signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}, {"title", "Old"}}, "")
	older.CreatedAt = nostr.Now() - 2
	if err := controller.Sign(t.Context(), older); err != nil {
		t.Fatalf("resign older list: %v", err)
	}
	newer := signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}, {"title", "Current"}}, "")
	queueCommunikeysQuery(endpoint, newer, older)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"apps"},
	})

	if _, err := membership.Assign(t.Context(), target); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(endpoint.published) != 1 || !tagHasValue(endpoint.published[0].Tags, "title", "Current") || tagHasValue(endpoint.published[0].Tags, "title", "Old") {
		t.Fatalf("replacement did not use latest profile list: %#v", endpoint.published)
	}
}

func TestCommunikeysMembershipAssignPrefersLowestEventIDAtEqualTimestamp(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: true}}

	identifier := opaqueCommunityID + "-apps"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Apps"},
		{"k", "1"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	first := signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}, {"title", "First"}}, "")
	second := signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}, {"title", "Second"}}, "")
	second.CreatedAt = first.CreatedAt
	if err := controller.Sign(t.Context(), second); err != nil {
		t.Fatalf("resign second list: %v", err)
	}
	wantTitle := "First"
	if second.ID.Hex() < first.ID.Hex() {
		wantTitle = "Second"
	}
	queueCommunikeysQuery(endpoint, first, second)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"apps"},
	})

	if _, err := membership.Assign(t.Context(), newFakeSigner(t).pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if len(endpoint.published) != 1 || !tagHasValue(endpoint.published[0].Tags, "title", wantTitle) {
		t.Fatalf("replacement did not select the lexicographically lowest event ID at equal created_at: %#v", endpoint.published)
	}
}

func TestCommunikeysMembershipAssignRetriesPublishAfterAuthRace(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{
		{Accepted: false, Reason: "auth-required: challenge pending"},
		{Accepted: true},
	}
	identifier := opaqueCommunityID + "-apps"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Apps"},
		{"k", "1"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"apps"},
	})

	if _, err := membership.Assign(t.Context(), newFakeSigner(t).pubkey); err != nil {
		t.Fatalf("Assign() error = %v", err)
	}
	if endpoint.publishCalls != 2 {
		t.Fatalf("publish calls = %d, want auth-race retry", endpoint.publishCalls)
	}
}

func TestCommunikeysMembershipAssignFailsClosedWithoutDefinition(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	queueCommunikeysQuery(endpoint, nil)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"general"},
	})

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "no valid owner-signed community definition") {
		t.Fatalf("Assign() error = %v, want missing definition", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events without a definition", len(endpoint.published))
	}
}

func TestCommunikeysMembershipAssignFailsClosedWhenCoordinateUnreferenced(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	identifier := opaqueCommunityID + "-general"
	// The coordinate appears only BEFORE the first content tag: top-level `a`
	// tags are not section references, and placement is authoritative.
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"a", "30000:" + controller.pubkey + ":" + identifier},
		{"content", "General"},
		{"k", "1"},
	}))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"general"},
	})

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "does not reference profile list") {
		t.Fatalf("Assign() error = %v, want unreferenced coordinate", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events for an unreferenced coordinate", len(endpoint.published))
	}
}

func TestCommunikeysMembershipAssignFailsClosedWithoutDelegatedProfileList(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	identifier := opaqueCommunityID + "-general"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "General"},
		{"k", "1"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	queueCommunikeysQuery(endpoint, nil)

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"general"},
	})

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "no valid list-author-signed profile list") {
		t.Fatalf("Assign() error = %v, want missing delegated list", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events without a delegated list", len(endpoint.published))
	}
}

func TestCommunikeysMembershipAssignFailsClosedOnRelayRejection(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	endpoint.publishResults = []RelayPublishResult{{Accepted: false, Reason: "restricted"}}
	identifier := opaqueCommunityID + "-apps"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Apps"},
		{"k", "1"},
		{"a", "30000:" + controller.pubkey + ":" + identifier},
	}))
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, controller, identifier, nostr.Tags{{"d", identifier}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        controller.pubkey,
		Purposes:          []string{"apps"},
	})

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Fatalf("Assign() error = %v, want relay rejection", err)
	}
}

func TestCommunikeysMembershipAssignRejectsSignerOtherThanListAuthor(t *testing.T) {
	owner := newFakeSigner(t)
	listAuthor := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	identifier := opaqueCommunityID + "-apps"
	queueCommunikeysQuery(endpoint, signedCommunikeysDefinition(t, owner, opaqueCommunityID, nostr.Tags{
		{"content", "Apps"},
		{"k", "1"},
		{"a", "30000:" + listAuthor.pubkey + ":" + identifier},
	}))
	queueCommunikeysQuery(endpoint, signedCommunikeysProfileList(t, listAuthor, identifier, nostr.Tags{{"d", identifier}}, ""))

	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	membership := newTestCommunikeysMembership(t, controller, bus, CommunikeysCommunity{
		DefinitionAddress: "32222:" + owner.pubkey + ":" + opaqueCommunityID,
		ListAuthor:        listAuthor.pubkey,
		Purposes:          []string{"apps"},
	})

	_, err = membership.Assign(t.Context(), newFakeSigner(t).pubkey)
	if err == nil || !strings.Contains(err.Error(), "is not the configured delegated list author") {
		t.Fatalf("Assign() error = %v, want list-author mismatch", err)
	}
	if len(endpoint.published) != 0 {
		t.Fatalf("published %d events with a mismatched signer", len(endpoint.published))
	}
}

func TestNewCommunikeysMembershipValidation(t *testing.T) {
	owner := newFakeSigner(t)
	controller := newFakeSigner(t)
	endpoint := newFakeRelayEndpoint("wss://community.example")
	bus, err := newSoulFactoryRelayBusFromEndpoints([]relayBusEndpoint{endpoint}, WithRelayBusSigner(controller))
	if err != nil {
		t.Fatalf("new relay bus: %v", err)
	}
	address := "32222:" + owner.pubkey + ":" + opaqueCommunityID

	cases := []struct {
		name      string
		community CommunikeysCommunity
		want      string
	}{
		{
			name:      "bare pubkey is not a definition address",
			community: CommunikeysCommunity{DefinitionAddress: owner.pubkey, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			want:      "must have the form 32222:<owner-pubkey>:<community-id>",
		},
		{
			name:      "wrong kind prefix",
			community: CommunikeysCommunity{DefinitionAddress: "30000:" + owner.pubkey + ":" + opaqueCommunityID, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			want:      "must have the form 32222:<owner-pubkey>:<community-id>",
		},
		{
			name:      "short community id",
			community: CommunikeysCommunity{DefinitionAddress: "32222:" + owner.pubkey + ":abcdef", ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			want:      "community id",
		},
		{
			name:      "non-hex owner",
			community: CommunikeysCommunity{DefinitionAddress: "32222:not-hex:" + opaqueCommunityID, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			want:      "owner",
		},
		{
			name:      "non-hex list author",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: "not-hex", Purposes: []string{"general"}},
			want:      "list author must be a real signing pubkey",
		},
		{
			name:      "list author must lift to a curve point unlike the opaque community id",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: opaqueCommunityID, Purposes: []string{"general"}},
			want:      "list author must be a real signing pubkey",
		},
		{
			name:      "uppercase purpose",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"General"}},
			want:      "section purpose",
		},
		{
			name:      "leading hyphen purpose",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"-apps"}},
			want:      "section purpose",
		},
		{
			name:      "empty purposes",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{" ", ""}},
			want:      "requires at least one section purpose",
		},
		{
			name:      "negative shard",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"general"}, Shard: -1},
			want:      "shard must be 0/1 (unsharded) or >= 2",
		},
		{
			name:      "oversize identifier",
			community: CommunikeysCommunity{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{strings.Repeat("a", 200)}},
			want:      "exceeds 200 UTF-8 bytes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newCommunikeysMembership([]CommunikeysCommunity{tc.community}, controller, bus)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("newCommunikeysMembership() error = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("valid config lowercases and derives section coordinates", func(t *testing.T) {
		membership, err := newCommunikeysMembership([]CommunikeysCommunity{{
			DefinitionAddress: strings.ToUpper(address),
			ListAuthor:        strings.ToUpper(controller.pubkey),
			Purposes:          []string{" general ", "general", "room-creator"},
		}}, controller, bus)
		if err != nil {
			t.Fatalf("newCommunikeysMembership() error = %v", err)
		}
		if len(membership.communities) != 1 {
			t.Fatalf("communities = %#v", membership.communities)
		}
		community := membership.communities[0]
		if community.definitionAddress != address || community.communityID != opaqueCommunityID {
			t.Fatalf("community target = %#v", community)
		}
		if len(community.sections) != 2 ||
			community.sections[0].identifier != opaqueCommunityID+"-general" ||
			community.sections[1].identifier != opaqueCommunityID+"-room-creator" ||
			community.sections[0].coordinate != "30000:"+controller.pubkey+":"+opaqueCommunityID+"-general" {
			t.Fatalf("sections = %#v", community.sections)
		}
	})

	t.Run("shard appends canonical suffix", func(t *testing.T) {
		membership, err := newCommunikeysMembership([]CommunikeysCommunity{{
			DefinitionAddress: address,
			ListAuthor:        controller.pubkey,
			Purposes:          []string{"general"},
			Shard:             2,
		}}, controller, bus)
		if err != nil {
			t.Fatalf("newCommunikeysMembership() error = %v", err)
		}
		if got := membership.communities[0].sections[0].identifier; got != opaqueCommunityID+"-general.2" {
			t.Fatalf("sharded identifier = %q", got)
		}
	})

	t.Run("same community id under different owners stays distinct", func(t *testing.T) {
		otherOwner := newFakeSigner(t)
		membership, err := newCommunikeysMembership([]CommunikeysCommunity{
			{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			{DefinitionAddress: "32222:" + otherOwner.pubkey + ":" + opaqueCommunityID, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
		}, controller, bus)
		if err != nil {
			t.Fatalf("newCommunikeysMembership() error = %v", err)
		}
		if len(membership.communities) != 2 {
			t.Fatalf("same-ID branches were merged: %#v", membership.communities)
		}
	})

	t.Run("duplicate branch entries merge purposes", func(t *testing.T) {
		membership, err := newCommunikeysMembership([]CommunikeysCommunity{
			{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"general"}},
			{DefinitionAddress: address, ListAuthor: controller.pubkey, Purposes: []string{"general", "chat"}},
		}, controller, bus)
		if err != nil {
			t.Fatalf("newCommunikeysMembership() error = %v", err)
		}
		if len(membership.communities) != 1 || len(membership.communities[0].sections) != 2 {
			t.Fatalf("duplicate branches did not merge: %#v", membership.communities)
		}
	})
}

func TestSectionListD(t *testing.T) {
	cases := []struct {
		name    string
		purpose string
		shard   int
		want    string
		wantErr bool
	}{
		{name: "unsharded shard 0", purpose: "general", shard: 0, want: opaqueCommunityID + "-general"},
		{name: "unsharded shard 1", purpose: "general", shard: 1, want: opaqueCommunityID + "-general"},
		{name: "first shard", purpose: "general", shard: 2, want: opaqueCommunityID + "-general.2"},
		{name: "double digit shard", purpose: "room-creator", shard: 10, want: opaqueCommunityID + "-room-creator.10"},
		{name: "shard keeps multi-word purpose distinct", purpose: "general-2", shard: 2, want: opaqueCommunityID + "-general-2.2"},
		{name: "oversize identifier", purpose: strings.Repeat("a", 140), shard: 0, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sectionListD(opaqueCommunityID, tc.purpose, tc.shard)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("sectionListD() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sectionListD() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("sectionListD() = %q, want %q", got, tc.want)
			}
		})
	}
}

func newTestCommunikeysMembership(t *testing.T, signer fakeSigner, bus *SoulFactoryRelayBus, communities ...CommunikeysCommunity) *communikeysMembership {
	t.Helper()
	membership, err := newCommunikeysMembership(communities, signer, bus)
	if err != nil {
		t.Fatalf("new Communikeys membership: %v", err)
	}
	return membership
}

func signedCommunikeysDefinition(t *testing.T, owner fakeSigner, communityID string, tags nostr.Tags) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		Kind:      communikeysDefinitionKind,
		CreatedAt: nostr.Now() - 1,
		Tags:      append(nostr.Tags{{"d", communityID}}, cloneCommunikeysTags(tags)...),
	}
	if err := owner.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign community definition: %v", err)
	}
	return event
}

func signedCommunikeysProfileList(t *testing.T, signer fakeSigner, identifier string, tags nostr.Tags, content string) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		Kind:      communikeysProfileListKind,
		CreatedAt: nostr.Now() - 1,
		Tags:      cloneCommunikeysTags(tags),
		Content:   content,
	}
	if !tagHasValue(event.Tags, "d", identifier) {
		event.Tags = append(event.Tags, nostr.Tag{"d", identifier})
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
