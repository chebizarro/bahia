package soulfactory

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
)

const (
	// concordDMRelayListKind is the NIP-17 DM relay list a recipient publishes
	// to name their giftwrap inbox.
	concordDMRelayListKind nostr.Kind = 10050
	// concordRelayListKind is the NIP-65 relay list metadata used as fallback.
	concordRelayListKind nostr.Kind = 10002
	// concordMaxInboxRelays bounds a recipient-controlled relay list. The list
	// is attacker-supplied input for any npub Bahia did not provision, so it is
	// capped exactly like a bundle's relay set.
	concordMaxInboxRelays = 5
)

// concordInbox is a recipient's resolved giftwrap inbox (CORD-05 §6): the
// relays in their kind-10050 DM relay list when one exists, their NIP-65 read
// relays otherwise. An empty inbox means the recipient has published neither,
// which is the normal case for an agent Bahia has just provisioned.
type concordInbox struct {
	relays []string
	source string
}

func (i concordInbox) empty() bool { return len(i.relays) == 0 }

// resolveConcordInbox looks the recipient's inbox up on the SoulFactory relays.
// A transport failure is an error rather than an empty inbox: silently falling
// back would publish an invite where the recipient may never look.
func (m *concordMembership) resolveConcordInbox(ctx context.Context, recipient nostr.PubKey) (concordInbox, error) {
	// Latest-wins routing lookup of replaceable relay lists: the newest list of
	// a relay majority is accepted and the degradation logged. Fewer answers
	// than a majority stay an error. See RelayReadPolicy.
	read, err := m.relayClient.QueryWithPolicy(ctx, "concord.inbox", RelayReadLatestQuorum(), []nostr.Filter{{
		Kinds:   []nostr.Kind{concordDMRelayListKind, concordRelayListKind},
		Authors: []nostr.PubKey{recipient},
	}})
	if err != nil {
		return concordInbox{}, fmt.Errorf("resolve giftwrap inbox for %s: %w", recipient.Hex(), err)
	}

	var dmRelayList, relayListMetadata *nostr.Event
	for _, event := range read.Events {
		// A relay may return anything; only the recipient's own signed lists count.
		if event == nil || event.PubKey != recipient || !validSignedEvent(event) {
			continue
		}
		switch event.Kind {
		case concordDMRelayListKind:
			dmRelayList = newerRelayEvent(dmRelayList, event)
		case concordRelayListKind:
			relayListMetadata = newerRelayEvent(relayListMetadata, event)
		}
	}

	if relays := boundConcordInboxRelays(concordDMRelayTags(dmRelayList)); len(relays) > 0 {
		return concordInbox{relays: relays, source: "kind-10050 DM relay list"}, nil
	}
	if relayListMetadata != nil {
		if relays := boundConcordInboxRelays(parseNIP65RelayPolicy(relayListMetadata).Read); len(relays) > 0 {
			return concordInbox{relays: relays, source: "NIP-65 read relays"}, nil
		}
	}
	return concordInbox{}, nil
}

func concordDMRelayTags(event *nostr.Event) []string {
	if event == nil {
		return nil
	}
	relays := make([]string, 0, len(event.Tags))
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == "relay" {
			relays = append(relays, tag[1])
		}
	}
	return relays
}

func boundConcordInboxRelays(relays []string) []string {
	bounded := make([]string, 0, concordMaxInboxRelays)
	for _, relay := range normalizeSoulRelays(relays) {
		if !validConcordRelayURL(relay) {
			continue
		}
		bounded = append(bounded, relay)
		if len(bounded) == concordMaxInboxRelays {
			break
		}
	}
	return bounded
}

// validConcordRelayURL accepts only a ws or wss URL with a host and no embedded
// credentials.
func validConcordRelayURL(relay string) bool {
	parsed, err := url.Parse(relay)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return parsed.Scheme == "ws" || parsed.Scheme == "wss"
}

// publishConcordInviteToInbox delivers the wrap to the recipient's inbox. Unlike
// the community relays, a recipient's list is not operator-controlled and may
// name a dead relay, so one acceptance is enough; zero is a delivery failure.
// An inbox relay the SoulFactory relay client already holds is used through
// it; any other is reached through a short-lived client on the same stack.
func publishConcordInviteToInbox(ctx context.Context, client *RelayClient, relays []string, event nostr.Event) error {
	failures := make([]string, 0, len(relays))
	for _, relay := range relays {
		if err := publishConcordInviteToInboxRelay(ctx, client, relay, event); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("no inbox relay accepted the invite: %s", strings.Join(failures, "; "))
}

func publishConcordInviteToInboxRelay(ctx context.Context, client *RelayClient, relay string, event nostr.Event) error {
	if client.holds(relay) {
		return publishConcordInvite(ctx, client, []string{relay}, event)
	}
	inbox, err := NewRelayClient([]string{relay}, WithRelaySigner(client.signer), WithRelayLogger(client.logger),
		withRelayEventValidator(client.validateEvent), withRelayResubscribeBackoff(client.resubscribeBackoff),
		withRelayAdmission(client.admission))
	if err != nil {
		return fmt.Errorf("%s: %w", relay, err)
	}
	defer inbox.Close()
	return publishConcordInvite(ctx, inbox, inbox.Relays(), event)
}
