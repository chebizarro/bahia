package relaysidecar

import (
	"slices"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"github.com/openagentsinc/bahia/internal/config"
)

// retentionClass is how long the sidecar keeps an event, by kind (C-19).
type retentionClass int

const (
	// retentionEphemeral kinds (20000-29999) are broadcast and never stored.
	retentionEphemeral retentionClass = iota
	// retentionLatestWins replaceable and addressable kinds are never
	// age-swept: each coordinate keeps only its newest version.
	retentionLatestWins
	// retentionTombstone kind-5 deletion requests are kept for good, so a
	// deleted event is never re-accepted.
	retentionTombstone
	// retentionRequest request/transport kinds use the short request retention.
	retentionRequest
	// retentionRegular kinds (audit and state facts such as 4903) are durable
	// unless an event retention cap is configured.
	retentionRegular
)

// retentionPolicy is the sidecar's per-class retention. NIP-40 expiration
// applies on top of every class.
type retentionPolicy struct {
	regular      time.Duration // 0 keeps regular events durably
	request      time.Duration
	requestKinds []nostr.Kind // sorted
}

func newRetentionPolicy(cfg config.RelaySidecarConfig) retentionPolicy {
	policy := retentionPolicy{regular: max(cfg.EventRetention, 0), request: cfg.RequestRetention}
	if policy.request <= 0 {
		policy.request = config.DefaultRelaySidecarRequestRetention
	}
	requestKinds := cfg.RequestRetentionKinds
	if requestKinds == nil {
		requestKinds = config.DefaultRelaySidecarRequestRetentionKinds()
	}
	for _, kind := range requestKinds {
		k := nostr.Kind(kind)
		// Validation rejects these; never let a latest-wins or tombstone kind
		// be age-swept even if an unvalidated config lists it.
		if kind < 0 || kind > 65535 || k == nostr.KindDeletion || k.IsReplaceable() || k.IsAddressable() {
			continue
		}
		if !slices.Contains(policy.requestKinds, k) {
			policy.requestKinds = append(policy.requestKinds, k)
		}
	}
	slices.Sort(policy.requestKinds)
	return policy
}

func (p retentionPolicy) classOf(kind nostr.Kind) retentionClass {
	switch {
	case kind.IsEphemeral():
		return retentionEphemeral
	case kind.IsReplaceable() || kind.IsAddressable():
		return retentionLatestWins
	case kind == nostr.KindDeletion:
		return retentionTombstone
	case slices.Contains(p.requestKinds, kind):
		return retentionRequest
	default:
		return retentionRegular
	}
}

// storedRequestKinds are the request kinds the store can hold: ephemeral ones
// are never stored, so sweeping them is pointless.
func (p retentionPolicy) storedRequestKinds() []nostr.Kind {
	var stored []nostr.Kind
	for _, kind := range p.requestKinds {
		if !kind.IsEphemeral() {
			stored = append(stored, kind)
		}
	}
	return stored
}

// nip11 describes the policy as NIP-11 `retention` entries, most specific
// first (clients apply the first entry matching a kind). A single kind is
// written as the range [k, k]; an entry without time or count is kept
// indefinitely.
func (p retentionPolicy) nip11() []*nip11.RelayRetentionDocument {
	var entries []*nip11.RelayRetentionDocument
	if stored := p.storedRequestKinds(); len(stored) > 0 {
		entry := &nip11.RelayRetentionDocument{Time: int64(p.request / time.Second)}
		for _, kind := range stored {
			entry.Kinds = append(entry.Kinds, []int{int(kind), int(kind)})
		}
		entries = append(entries, entry)
	}
	// Latest-wins kinds and deletion requests are never age-swept.
	entries = append(entries, &nip11.RelayRetentionDocument{Kinds: [][]int{
		{0, 0}, {3, 3}, {int(nostr.KindDeletion), int(nostr.KindDeletion)}, {10000, 19999}, {30000, 39999},
	}})
	if p.regular > 0 {
		entries = append(entries, &nip11.RelayRetentionDocument{Time: int64(p.regular / time.Second)})
	}
	return entries
}

// sweepResult counts the events one retention sweep deleted, by cause.
type sweepResult struct {
	Expired int64 // NIP-40 expiration passed
	Request int64 // request/transport kinds older than the request retention
	Regular int64 // regular kinds older than the event retention cap
}

func (r sweepResult) total() int64 { return r.Expired + r.Request + r.Regular }
