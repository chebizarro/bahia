package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"fiatjaf.com/nostr"
)

// canonicalHeadAdapter is the common relay/local, accepted-inventory and
// exact-equivalence machinery for non-registry addressable recovery records.
// A domain supplies only its production wire builder and publisher.
type canonicalHeadAdapter struct {
	old       nostr.PubKey
	source    EventSource
	local     RecoveryLocalStore
	ledger    *Ledger
	kinds     []nostr.Kind
	name      string
	belongs   func(nostr.Event) bool
	preflight func(nostr.Event) error
	publish   func(context.Context, nostr.Keyer, nostr.Event, registryRecoverySink) error
}

func (a canonicalHeadAdapter) state() (State, error) {
	if a.old == nostr.ZeroPK || a.source == nil || a.local == nil || a.ledger == nil || len(a.kinds) == 0 || a.belongs == nil || a.preflight == nil || a.publish == nil {
		return State{}, errors.New("canonical recovery adapter is incomplete")
	}
	s, err := a.ledger.Load()
	if err != nil {
		return State{}, err
	}
	if s.OldPubkey != a.old.Hex() || s.InventoryDigest == "" || s.ActivationBarrier <= 0 {
		return State{}, errors.New("accepted recovery inventory unavailable")
	}
	digest, count, err := InventoryDigest(s.Inventory)
	if err != nil || digest != s.InventoryDigest || count != len(s.Inventory) {
		return State{}, errors.New("accepted recovery inventory digest mismatch")
	}
	return s, nil
}

func (a canonicalHeadAdapter) heads(ctx context.Context) (map[string]nostr.Event, error) {
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	accepted := map[nostr.ID]struct{}{}
	ids := make([]nostr.ID, 0)
	for _, item := range s.Inventory {
		if !slices.Contains(a.kinds, nostr.Kind(item.Kind)) {
			continue
		}
		id, err := nostr.IDFromHex(item.ID)
		if err != nil {
			return nil, errors.New("invalid accepted event ID")
		}
		accepted[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[string]nostr.Event{}, nil
	}
	relay, err := a.source.Read(ctx, nostr.Filter{IDs: ids, Kinds: a.kinds, Authors: []nostr.PubKey{a.old}})
	if err != nil {
		return nil, fmt.Errorf("read accepted %s relay history: %w", a.name, err)
	}
	local, err := a.local.Events(ctx, a.old)
	if err != nil {
		return nil, fmt.Errorf("read accepted %s local history: %w", a.name, err)
	}
	seen := map[nostr.ID]nostr.Event{}
	for _, ev := range append(relay, local...) {
		if _, ok := accepted[ev.ID]; !ok || !slices.Contains(a.kinds, ev.Kind) {
			continue
		}
		if ev.PubKey != a.old || !ev.CheckID() || !ev.VerifySignature() {
			return nil, errors.New("invalid accepted canonical event")
		}
		if int64(ev.CreatedAt) > s.ActivationBarrier {
			return nil, errors.New("accepted old-key event crosses trust activation barrier")
		}
		seen[ev.ID] = ev
	}
	if err := completeAcceptedHistory(ctx, s, a.old, accepted, seen); err != nil {
		return nil, err
	}

	heads := map[string]nostr.Event{}
	for _, ev := range seen {
		if !a.belongs(ev) {
			continue
		}
		d := tag(ev, "d")
		if d == "" {
			return nil, errors.New("accepted canonical head lacks coordinate")
		}
		key := strconv.Itoa(int(ev.Kind)) + ":" + d
		if prior, ok := heads[key]; !ok || newer(ev, prior) {
			heads[key] = ev
		}
	}
	for key, ev := range heads {
		if err := rejectSupersededSnapshotHead(ctx, s, a.source, local, a.old, ev); err != nil {
			return nil, fmt.Errorf("%s coordinate %s: %w", a.name, key, err)
		}
		if err := a.preflight(ev); err != nil {
			return nil, fmt.Errorf("%s coordinate %s: %w", a.name, key, err)
		}
	}
	return heads, nil
}

func (a canonicalHeadAdapter) Inventory(ctx context.Context) ([]string, error) {
	heads, err := a.heads(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(heads))
	for _, ev := range heads {
		ids = append(ids, ev.ID.Hex())
	}
	slices.Sort(ids)
	return ids, nil
}

func (a canonicalHeadAdapter) newHeads(ctx context.Context, pub nostr.PubKey, old nostr.Event) (*nostr.Event, *nostr.Event, error) {
	f := nostr.Filter{Authors: []nostr.PubKey{pub}, Kinds: []nostr.Kind{old.Kind}, Tags: nostr.TagMap{"d": {tag(old, "d")}}}
	relay, err := a.source.Read(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	local, err := a.local.Events(ctx, pub)
	if err != nil {
		return nil, nil, err
	}
	return latestHead(relay, pub, old.Kind, tag(old, "d")), latestHead(local, pub, old.Kind, tag(old, "d")), nil
}

func latestHead(events []nostr.Event, pub nostr.PubKey, kind nostr.Kind, d string) *nostr.Event {
	var head *nostr.Event
	for _, ev := range events {
		if ev.PubKey != pub || ev.Kind != kind || tag(ev, "d") != d {
			continue
		}
		if head == nil || newer(ev, *head) {
			copy := ev
			head = &copy
		}
	}
	return head
}

func (a canonicalHeadAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	heads, err := a.heads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub == nostr.ZeroPK || pub.Hex() != s.NewPubkey || pub == a.old {
		return nil, errors.New("new canonical signer does not match recovery ledger")
	}
	sink := registryRecoverySink{source: a.source, local: a.local}
	evidence := make([]string, 0, len(heads))
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		relay, local, err := a.newHeads(ctx, pub, old)
		if err != nil {
			return nil, err
		}
		if relay != nil && !equivalent(*relay, old) || local != nil && !equivalent(*local, old) {
			return nil, fmt.Errorf("%s coordinate %s: new-key head differs from accepted state", a.name, key)
		}
		switch {
		case relay != nil && local != nil:
		case relay != nil:
			err = a.local.StoreEvent(ctx, *relay)
		case local != nil:
			err = a.source.Publish(ctx, *local)
		default:
			err = a.publish(ctx, signer, old, sink)
		}
		if err != nil {
			return nil, err
		}
		relay, local, err = a.newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil || !equivalent(*relay, old) || !equivalent(*local, old) {
			return nil, fmt.Errorf("%s coordinate %s: %w", a.name, key, ErrIncomplete)
		}
		evidence = append(evidence, relay.ID.Hex())
	}
	return evidence, nil
}

func (a canonicalHeadAdapter) Verify(ctx context.Context) ([]string, error) {
	heads, err := a.heads(ctx)
	if err != nil {
		return nil, err
	}
	s, err := a.state()
	if err != nil {
		return nil, err
	}
	pub, err := nostr.PubKeyFromHex(s.NewPubkey)
	if err != nil || pub == a.old {
		return nil, errors.New("new canonical identity unavailable")
	}
	h := sha256.New()
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		relay, local, err := a.newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil || !equivalent(*relay, old) || !equivalent(*local, old) {
			return nil, fmt.Errorf("%s coordinate %s: equivalence verification failed: %w", a.name, key, ErrIncomplete)
		}
		_, _ = h.Write([]byte(key + "\x00" + old.Content + "\x00"))
		tags, _ := json.Marshal(old.Tags)
		_, _ = h.Write(tags)
		_, _ = h.Write([]byte("\n"))
	}
	return []string{a.name + ":sha256:" + hex.EncodeToString(h.Sum(nil)), a.name + ":count:" + strconv.Itoa(len(heads))}, nil
}
