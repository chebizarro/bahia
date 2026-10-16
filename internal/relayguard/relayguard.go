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
	"sync"
	"sync/atomic"

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

// IsControlPlaneKind reports whether kind, signed by the service key, is
// Bahia control plane: canonical cp-state (30900, including OCK envelopes and
// NIP-44 confidential payloads), audit (4903), intent status (30315),
// identity, replay and readiness records (31410, 31411, 30360), relay and
// discovery lists and the other kinds only the service key publishes
// (kinds.IsBahiaProjectionKind), plaintext ContextVM messages (25910) and
// NIP-09 deletion requests (5) for any of them.
func IsControlPlaneKind(kind nostr.Kind) bool {
	return kinds.IsBahiaProjectionKind(int(kind)) || kind == kinds.ContextVMMessage || kind == nostr.KindDeletion
}

// IsGiftWrapKind reports whether kind is a NIP-59 gift wrap. Gift wraps are
// signed by ephemeral keys, so their author cannot show they carry Bahia's
// control plane; pools that carry it (CheckControlPlanePool) confine them.
func IsGiftWrapKind(kind nostr.Kind) bool {
	return kind == kinds.ContextVMGiftWrap || kind == kinds.ContextVMEphemeralGiftWrap
}

// Guard refuses to publish service-signed control-plane events to relays
// outside the controlled set. A nil *Guard allows everything; the daemon
// always constructs one with New.
type Guard struct {
	service           nostr.PubKey
	controlled        map[string]struct{}
	cleanup           map[string]struct{}
	old               nostr.PubKey
	authorizeDeletion func(nostr.Event) bool

	refusals atomic.Uint64
	reported sync.Map // refusalKey -> struct{}: refusals already reported once
}

type refusalKey struct {
	relay string
	kind  nostr.Kind
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

// NewRecoveryCleanup confines a dedicated recovery pool to signed old-key
// NIP-09 e-tag deletions on the explicitly configured cleanup set. It does
// not relax the ordinary control-plane guard or its public-relay denylist.
func NewRecoveryCleanup(service nostr.PubKey, controlled, cleanup []string, old nostr.PubKey, authorizeDeletion func(nostr.Event) bool) (*Guard, error) {
	g, err := New(service, controlled)
	if err != nil {
		return nil, err
	}
	if old == nostr.ZeroPK || old == service || len(cleanup) == 0 || authorizeDeletion == nil {
		return nil, fmt.Errorf("recovery cleanup signer and relays required")
	}
	g.old = old
	g.authorizeDeletion = authorizeDeletion
	g.cleanup = make(map[string]struct{}, len(cleanup))
	for _, relay := range cleanup {
		url := Normalize(relay)
		if url == "" {
			return nil, fmt.Errorf("empty cleanup relay")
		}
		g.cleanup[url] = struct{}{}
	}
	return g, nil
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
	if g != nil && g.cleanup != nil {
		_, listed := g.cleanup[Normalize(relayURL)]
		if !listed || ev.Kind != nostr.KindDeletion || ev.PubKey != g.old || !ev.CheckID() || !ev.VerifySignature() || !scopedETagDeletion(ev) || !g.authorizeDeletion(ev) {
			return g.refuse(relayURL, ev.Kind, "non-quarantined cleanup event")
		}
		return nil
	}
	if g == nil || ev.PubKey != g.service || !IsControlPlaneKind(ev.Kind) || g.Allows(relayURL) {
		return nil
	}
	return g.refuse(relayURL, ev.Kind, "service control-plane kind")
}

// CheckControlPlanePool is Check for a pool that carries the control plane
// (control-plane, ContextVM and service pools): gift wraps, whose ephemeral
// signer Check cannot attribute, also go only to controlled relays.
func (g *Guard) CheckControlPlanePool(relayURL string, ev nostr.Event) error {
	if g == nil {
		return nil
	}
	if IsGiftWrapKind(ev.Kind) && !g.Allows(relayURL) {
		return g.refuse(relayURL, ev.Kind, "control-plane pool gift wrap kind")
	}
	return g.Check(relayURL, ev)
}

func (g *Guard) refuse(relayURL string, kind nostr.Kind, what string) error {
	g.refusals.Add(1)
	refused := &RefusedError{Relay: Normalize(relayURL), Kind: kind, what: what}
	_, reported := g.reported.LoadOrStore(refusalKey{relay: refused.Relay, kind: kind}, struct{}{})
	refused.FirstForRelayKind = !reported
	return refused
}

// Refusals is the number of EVENT frames the guard has refused.
func (g *Guard) Refusals() uint64 {
	if g == nil {
		return 0
	}
	return g.refusals.Load()
}

// RefusedError is the guard's refusal. Its Reason is a NIP-01 "blocked:"
// message, so publishers treat it like a relay's permanent rejection of the
// event at that relay.
type RefusedError struct {
	Relay string
	Kind  nostr.Kind
	// FirstForRelayKind is true for the guard's first refusal of this kind
	// to this relay, so callers report each (relay, kind) once.
	FirstForRelayKind bool
	what              string
}

func (e *RefusedError) Error() string { return e.Reason() }

// Reason is the OK-style rejection message for the refused relay.
func (e *RefusedError) Reason() string {
	what := e.what
	if what == "" {
		what = "service control-plane kind"
	}
	return fmt.Sprintf("blocked: %s is not a controlled relay (nostr.controlled_relays); %s %d is never published there", e.Relay, what, e.Kind)
}

func scopedETagDeletion(ev nostr.Event) bool {
	count := 0
	for _, tag := range ev.Tags {
		if len(tag) != 2 {
			return false
		}
		switch tag[0] {
		case "e":
			id, err := nostr.IDFromHex(tag[1])
			if err != nil || id.Hex() != tag[1] {
				return false
			}
			count++
		case "k":
			// Kind hints are allowed only alongside concrete e-tags.
		default:
			return false
		}
	}
	return count > 0
}
