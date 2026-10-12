// Package relayguard keeps Bahia's service-signed control-plane events on the
// relays Bahia controls.
//
// Configuration validation and the relay pool's publish path apply the same
// two rules from here:
//
//   - A well-known public relay (IsWellKnownPublic) is never a controlled
//     relay. It may appear only in a non-control-plane relay list, and only
//     when the operator lists it in nostr.allowed_public_relays.
//   - A Guard refuses to put a control-plane event (IsControlPlaneKind)
//     signed by the service key on any relay outside the controlled set.
//
// Relay URLs are compared after nostr.NormalizeURL, the normalization the
// relay pool applies to its own relay URLs.
package relayguard

import (
	"fmt"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// wellKnownPublicDomains are operators of open public relays. A relay whose
// host is one of these domains or a subdomain of one is public: anyone can
// read what it stores, so Bahia's control plane is never published there.
var wellKnownPublicDomains = []string{
	"damus.io",
	"nos.lol",
	"primal.net",
	"nostr.wine",
	"nostr.band",
	"snort.social",
	"nostr.mom",
	"offchain.pub",
	"nostr.bg",
	"purplepag.es",
	"nostr.land",
	"current.fyi",
	"wellorder.net",
	"mostr.pub",
	"nostr.oxtr.dev",
	"bitcoiner.social",
	"nostr.info",
	"nostrplebs.com",
}

// Normalize returns the relay URL in the form the relay pool uses.
func Normalize(relayURL string) string {
	return nostr.NormalizeURL(relayURL)
}

// IsWellKnownPublic reports whether relayURL is served by a well-known public
// relay operator.
func IsWellKnownPublic(relayURL string) bool {
	parsed, err := url.Parse(Normalize(relayURL))
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	for _, domain := range wellKnownPublicDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// IsControlPlaneKind reports whether kind is one of Bahia's control-plane
// kinds: canonical cp-state (30900, including OCK envelopes and NIP-44
// confidential payloads), audit (4903), intent status (30315), identity,
// replay and readiness records (31410, 31411, 30360), relay and discovery
// lists, and the other kinds only the service key publishes
// (kinds.IsBahiaProjectionKind).
func IsControlPlaneKind(kind nostr.Kind) bool {
	return kinds.IsBahiaProjectionKind(int(kind))
}

// Guard refuses to publish service-signed control-plane events to relays
// outside the controlled set. A nil *Guard allows everything; the daemon
// always constructs one with New.
type Guard struct {
	service    nostr.PubKey
	controlled map[string]struct{}
}

// New builds a guard for the service identity. controlled is the set of
// relays Bahia operates; New fails if any of them is a well-known public
// relay. An empty set refuses every service control-plane publication.
func New(service nostr.PubKey, controlled []string) (*Guard, error) {
	if service == nostr.ZeroPK {
		return nil, fmt.Errorf("relay guard: service pubkey is required")
	}
	set := make(map[string]struct{}, len(controlled))
	for _, relay := range controlled {
		normalized := Normalize(relay)
		if normalized == "" {
			continue
		}
		if IsWellKnownPublic(normalized) {
			return nil, fmt.Errorf("relay guard: %s is a public relay and cannot be a controlled relay", normalized)
		}
		set[normalized] = struct{}{}
	}
	return &Guard{service: service, controlled: set}, nil
}

// Allows reports whether relayURL is a controlled relay.
func (g *Guard) Allows(relayURL string) bool {
	if g == nil {
		return true
	}
	_, ok := g.controlled[Normalize(relayURL)]
	return ok
}

// Check returns a *RefusedError when ev is a control-plane event signed by
// the service key and relayURL is not a controlled relay; otherwise nil.
func (g *Guard) Check(relayURL string, ev nostr.Event) error {
	if g == nil || ev.PubKey != g.service || !IsControlPlaneKind(ev.Kind) || g.Allows(relayURL) {
		return nil
	}
	return &RefusedError{Relay: Normalize(relayURL), Kind: ev.Kind}
}

// RefusedError is the guard's refusal. Its Reason is a NIP-01 "blocked:"
// message, so publishers treat it like a relay's permanent rejection of the
// event at that relay.
type RefusedError struct {
	Relay string
	Kind  nostr.Kind
}

func (e *RefusedError) Error() string { return e.Reason() }

// Reason is the OK-style rejection message for the refused relay.
func (e *RefusedError) Reason() string {
	return fmt.Sprintf("blocked: %s is not a controlled relay (nostr.controlled_relays); service control-plane kind %d is never published there", e.Relay, e.Kind)
}
