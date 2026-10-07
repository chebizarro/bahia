package nostrutil

import (
	"container/heap"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip40"
)

// This file is the one place Go consumers resolve event state (and
// in docs/architecture/event-lifecycle.md):
//
// - NIP-01 replaceable and addressable events: per (kind, pubkey, d) the
// latest created_at wins, and on equal created_at the lowest id wins.
// - NIP-09 deletion requests (kind 5): an `e` reference deletes that id when
// the requester is its author; an `a` reference deletes every version of
// the requester's coordinate up to the request's created_at. Deleted
// events never come back, whichever order the request and the target
// arrive in.
// - NIP-40 expiration: an expired event is ignored on arrival, and a live
// event is dropped when its expiration passes.

// Address is the NIP-01 coordinate of a replaceable or addressable event.
// Plain replaceable kinds (0, 3, 10000-19999) have an empty D.
type Address struct {
	Kind   canonicalnostr.Kind
	PubKey canonicalnostr.PubKey
	D      string
}

// String renders the coordinate as used in `a` tags: "<kind>:<pubkey>:<d>".
func (a Address) String() string {
	return strconv.Itoa(int(a.Kind)) + ":" + a.PubKey.Hex() + ":" + a.D
}

// IsStateKind reports whether events of kind are replaceable or addressable
// state, whose current version is the latest one per Address.
func IsStateKind(kind canonicalnostr.Kind) bool {
	return kind.IsReplaceable() || kind.IsAddressable()
}

// MaxEventAge bounds how old an event of an AgeCapped kind may be when a
// Bahia consumer or relay admits it: a year-old one-shot fact, request
// or command is a replay, not news, and the cap keeps replay protection and
// dedup memory bounded.
const MaxEventAge = 365 * 24 * time.Hour

// AgeCapped reports whether MaxEventAge applies to events of kind. It is the
// one rule both the daemon's inbound validation and the relay sidecar's write
// policy apply. Regular and ephemeral kinds are capped. Replaceable and
// addressable events are exempt: they are current state until a newer version
// replaces them, however old (a NIP-65 list, ACL, relay set or trust list
// untouched for a year is still in force), and archives must be able to
// republish them. Deletion requests are exempt because they stay in force for
// as long as their targets can be republished; dropping an old one would let
// deleted state return.
func AgeCapped(kind canonicalnostr.Kind) bool {
	return !IsStateKind(kind) && kind != canonicalnostr.KindDeletion
}

// AddressOf returns the coordinate of a replaceable or addressable event.
func AddressOf(ev *canonicalnostr.Event) (Address, bool) {
	if ev == nil || !IsStateKind(ev.Kind) {
		return Address{}, false
	}
	address := Address{Kind: ev.Kind, PubKey: ev.PubKey}
	if ev.Kind.IsAddressable() {
		address.D = ev.Tags.GetD()
	}
	return address, true
}

// ParseAddress parses an `a` tag value. Only replaceable and addressable
// kinds form valid coordinates.
func ParseAddress(value string) (Address, bool) {
	parts := strings.SplitN(strings.TrimSpace(value), ":", 3)
	if len(parts) != 3 {
		return Address{}, false
	}
	kind, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return Address{}, false
	}
	pubkey, err := PubKeyFromHex(parts[1])
	if err != nil {
		return Address{}, false
	}
	address := Address{Kind: canonicalnostr.Kind(kind), PubKey: pubkey, D: parts[2]}
	if !IsStateKind(address.Kind) || (address.Kind.IsReplaceable() && address.D != "") {
		return Address{}, false
	}
	return address, true
}

// Version orders competing versions of one piece of state. ID is the
// lowercase hex event id, so string order equals byte order.
type Version struct {
	CreatedAt canonicalnostr.Timestamp
	ID        string
}

// VersionOf returns the NIP-01 ordering key of an event.
func VersionOf(ev *canonicalnostr.Event) Version {
	if ev == nil {
		return Version{}
	}
	return Version{CreatedAt: ev.CreatedAt, ID: ev.ID.Hex()}
}

// Supersedes reports whether v replaces current under NIP-01: a later
// created_at wins, and on equal created_at the lowest id wins. A version
// never supersedes itself.
func (v Version) Supersedes(current Version) bool {
	if v.CreatedAt != current.CreatedAt {
		return v.CreatedAt > current.CreatedAt
	}
	return v.ID < current.ID
}

// ExpiresAt returns the NIP-40 expiration of an event, or 0 when it has none
// (or an unparseable one).
func ExpiresAt(ev *canonicalnostr.Event) canonicalnostr.Timestamp {
	if ev == nil {
		return 0
	}
	if expiresAt := nip40.GetExpiration(ev.Tags); expiresAt > 0 {
		return expiresAt
	}
	return 0
}

// Expired reports whether the event's NIP-40 expiration is at or before now.
func Expired(ev *canonicalnostr.Event, now time.Time) bool {
	expiresAt := ExpiresAt(ev)
	return expiresAt > 0 && expiresAt <= canonicalnostr.Timestamp(now.Unix())
}

// Deletion is a parsed NIP-09 deletion request. Addresses only holds
// coordinates of the requester's own events; references to other authors'
// coordinates can never apply and are dropped.
type Deletion struct {
	Author    canonicalnostr.PubKey
	CreatedAt canonicalnostr.Timestamp
	IDs       []canonicalnostr.ID
	Addresses []Address
}

// ParseDeletion parses a kind-5 deletion request.
func ParseDeletion(ev *canonicalnostr.Event) (Deletion, bool) {
	if ev == nil || ev.Kind != canonicalnostr.KindDeletion {
		return Deletion{}, false
	}
	deletion := Deletion{Author: ev.PubKey, CreatedAt: ev.CreatedAt}
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "e":
			if id, err := canonicalnostr.IDFromHex(strings.ToLower(strings.TrimSpace(tag[1]))); err == nil {
				deletion.IDs = append(deletion.IDs, id)
			}
		case "a":
			if address, ok := ParseAddress(tag[1]); ok && address.PubKey == ev.PubKey {
				deletion.Addresses = append(deletion.Addresses, address)
			}
		}
	}
	return deletion, true
}

// Entry is one live event the Lifecycle tracks: the current version of a
// replaceable or addressable coordinate, or a regular event with an
// expiration. Address is only meaningful when HasAddress is true.
type Entry struct {
	ID         canonicalnostr.ID
	PubKey     canonicalnostr.PubKey
	Kind       canonicalnostr.Kind
	CreatedAt  canonicalnostr.Timestamp
	ExpiresAt  canonicalnostr.Timestamp
	Address    Address
	HasAddress bool
}

// Outcome is what a consumer must do with an observed event.
type Outcome int

const (
	// OutcomeAccept: apply the event. For state kinds it is the new current version
	// (Decision.Replaced is the version it replaces, if any was live).
	OutcomeAccept Outcome = iota + 1
	// OutcomeSuperseded: a newer version (or an equal-time lower id) is current.
	OutcomeSuperseded
	// OutcomeDuplicate: the event is already the current version.
	OutcomeDuplicate
	// OutcomeDeleted: a NIP-09 request from the author already deleted it.
	OutcomeDeleted
	// OutcomeExpired: its NIP-40 expiration has passed.
	OutcomeExpired
	// OutcomeDeletion: the event was a kind-5 request. It is recorded so its
	// targets never come back, and Decision.Removed lists the live entries it
	// deleted, which the consumer must drop.
	OutcomeDeletion
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAccept:
		return "accept"
	case OutcomeSuperseded:
		return "superseded"
	case OutcomeDuplicate:
		return "duplicate"
	case OutcomeDeleted:
		return "deleted"
	case OutcomeExpired:
		return "expired"
	case OutcomeDeletion:
		return "deletion"
	default:
		return "unknown"
	}
}

// Decision is the result of Lifecycle.Observe.
type Decision struct {
	Outcome  Outcome
	Replaced *Entry
	Removed  []Entry
}

type lifecycleHead struct {
	version Version
	id      canonicalnostr.ID
	live    bool
}

type deletedID struct {
	id     canonicalnostr.ID
	author canonicalnostr.PubKey
}

// Lifecycle is the per-consumer state machine for NIP-01 replacement, NIP-09
// deletion and NIP-40 expiration. It is safe for concurrent use.
//
// Memory: one head per coordinate ever seen (kept after deletion or expiry as
// the floor that stops older versions coming back), one tombstone per
// deleted id or coordinate, and one expiry record per live expiring event.
// Regular events without an expiration are not tracked, so an `e` deletion
// of one is recorded (it cannot be re-accepted) but cannot be reported as
// Removed.
type Lifecycle struct {
	mu           sync.Mutex
	heads        map[Address]*lifecycleHead
	live         map[canonicalnostr.ID]Entry
	deletedIDs   map[deletedID]struct{}
	deletedAddrs map[Address]canonicalnostr.Timestamp
	expiries     expiryHeap
	changed      chan struct{}
	newTimer     func(time.Duration) (<-chan time.Time, func() bool)
}

// NewLifecycle returns an empty Lifecycle.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{
		heads:        make(map[Address]*lifecycleHead),
		live:         make(map[canonicalnostr.ID]Entry),
		deletedIDs:   make(map[deletedID]struct{}),
		deletedAddrs: make(map[Address]canonicalnostr.Timestamp),
		changed:      make(chan struct{}, 1),
		newTimer: func(d time.Duration) (<-chan time.Time, func() bool) {
			timer := time.NewTimer(d)
			return timer.C, timer.Stop
		},
	}
}

// Observe decides what a consumer does with a validated event and records
// the event's effect. Callers must verify id and signature first.
func (l *Lifecycle) Observe(ev *canonicalnostr.Event, now time.Time) Decision {
	if ev == nil {
		return Decision{Outcome: OutcomeDuplicate}
	}
	if Expired(ev, now) {
		return Decision{Outcome: OutcomeExpired}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev.Kind == canonicalnostr.KindDeletion {
		return l.applyDeletion(ev)
	}
	if _, ok := l.deletedIDs[deletedID{id: ev.ID, author: ev.PubKey}]; ok {
		return Decision{Outcome: OutcomeDeleted}
	}
	entry := Entry{ID: ev.ID, PubKey: ev.PubKey, Kind: ev.Kind, CreatedAt: ev.CreatedAt, ExpiresAt: ExpiresAt(ev)}
	address, stateful := AddressOf(ev)
	if !stateful {
		if entry.ExpiresAt > 0 {
			if _, ok := l.live[ev.ID]; ok {
				return Decision{Outcome: OutcomeDuplicate}
			}
			l.track(entry)
		}
		return Decision{Outcome: OutcomeAccept}
	}
	entry.Address, entry.HasAddress = address, true
	if deletedUpTo, ok := l.deletedAddrs[address]; ok && ev.CreatedAt <= deletedUpTo {
		return Decision{Outcome: OutcomeDeleted}
	}
	decision := Decision{Outcome: OutcomeAccept}
	version := VersionOf(ev)
	if head := l.heads[address]; head != nil {
		if head.id == ev.ID {
			return Decision{Outcome: OutcomeDuplicate}
		}
		if !version.Supersedes(head.version) {
			return Decision{Outcome: OutcomeSuperseded}
		}
		if head.live {
			if replaced, ok := l.live[head.id]; ok {
				decision.Replaced = &replaced
			}
			delete(l.live, head.id)
		}
	}
	l.heads[address] = &lifecycleHead{version: version, id: ev.ID, live: true}
	l.track(entry)
	return decision
}

func (l *Lifecycle) track(entry Entry) {
	l.live[entry.ID] = entry
	if entry.ExpiresAt <= 0 {
		return
	}
	heap.Push(&l.expiries, expiryItem{at: entry.ExpiresAt, id: entry.ID})
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

func (l *Lifecycle) applyDeletion(ev *canonicalnostr.Event) Decision {
	deletion, _ := ParseDeletion(ev)
	decision := Decision{Outcome: OutcomeDeletion}
	for _, id := range deletion.IDs {
		l.deletedIDs[deletedID{id: id, author: deletion.Author}] = struct{}{}
		entry, ok := l.live[id]
		if !ok || entry.PubKey != deletion.Author || entry.Kind == canonicalnostr.KindDeletion {
			continue
		}
		decision.Removed = append(decision.Removed, l.drop(entry))
	}
	for _, address := range deletion.Addresses {
		if deletion.CreatedAt > l.deletedAddrs[address] {
			l.deletedAddrs[address] = deletion.CreatedAt
		}
		head := l.heads[address]
		if head == nil || !head.live || head.version.CreatedAt > deletion.CreatedAt {
			continue
		}
		if entry, ok := l.live[head.id]; ok {
			decision.Removed = append(decision.Removed, l.drop(entry))
		}
	}
	return decision
}

// drop removes a live entry. A head stays as the ordering floor.
func (l *Lifecycle) drop(entry Entry) Entry {
	delete(l.live, entry.ID)
	if entry.HasAddress {
		if head := l.heads[entry.Address]; head != nil && head.id == entry.ID {
			head.live = false
		}
	}
	return entry
}

// Release forgets an accepted event the consumer failed to apply, so a
// redelivery of it is accepted again. Deletion tombstones are kept.
func (l *Lifecycle) Release(id canonicalnostr.ID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.live[id]
	if !ok {
		return
	}
	delete(l.live, id)
	if entry.HasAddress {
		if head := l.heads[entry.Address]; head != nil && head.id == id {
			delete(l.heads, entry.Address)
		}
	}
}

// Expire drops and returns every live entry whose expiration is at or
// before now.
func (l *Lifecycle) Expire(now time.Time) []Entry {
	cutoff := canonicalnostr.Timestamp(now.Unix())
	l.mu.Lock()
	defer l.mu.Unlock()
	var expired []Entry
	for l.expiries.Len() > 0 && l.expiries[0].at <= cutoff {
		item := heap.Pop(&l.expiries).(expiryItem)
		if entry, ok := l.live[item.id]; ok && entry.ExpiresAt == item.at {
			expired = append(expired, l.drop(entry))
		}
	}
	return expired
}

// NextExpiry returns when the earliest live entry expires.
func (l *Lifecycle) NextExpiry() (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.expiries.Len() > 0 {
		item := l.expiries[0]
		if entry, ok := l.live[item.id]; ok && entry.ExpiresAt == item.at {
			return time.Unix(int64(item.at), 0), true
		}
		heap.Pop(&l.expiries)
	}
	return time.Time{}, false
}

// RunExpiry calls drop with the entries that expire, when they expire, until
// ctx ends. It waits on a timer for the earliest expiration and re-arms when
// Observe tracks a new one; it never polls.
func (l *Lifecycle) RunExpiry(ctx context.Context, now func() time.Time, drop func([]Entry)) error {
	if now == nil {
		now = time.Now
	}
	for {
		var fire <-chan time.Time
		stop := func() bool { return false }
		if at, ok := l.NextExpiry(); ok {
			wait := at.Sub(now())
			if wait < 0 {
				wait = 0
			}
			fire, stop = l.newTimer(wait)
		}
		select {
		case <-ctx.Done():
			stop()
			return nil
		case <-l.changed:
			stop()
		case <-fire:
			if expired := l.Expire(now()); len(expired) > 0 && drop != nil {
				drop(expired)
			}
		}
	}
}

type expiryItem struct {
	at canonicalnostr.Timestamp
	id canonicalnostr.ID
}

type expiryHeap []expiryItem

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].at < h[j].at }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(expiryItem)) }
func (h *expiryHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}
