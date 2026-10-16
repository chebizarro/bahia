package identityrecovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
)

// QuarantinedOldSigner deliberately does not implement nostr.Signer or Keyer.
// Its only signing operation is an inventory-scoped NIP-09 e-tag deletion.
type QuarantinedOldSigner struct {
	keyer   nostr.Keyer
	author  nostr.PubKey
	targets map[nostr.ID]nostr.Kind
}

func NewQuarantinedOldSigner(ctx context.Context, keyer nostr.Keyer, author nostr.PubKey, observed []InventoryEvent) (*QuarantinedOldSigner, error) {
	if keyer == nil || author == nostr.ZeroPK {
		return nil, errors.New("old signer identity required")
	}
	pub, err := keyer.GetPublicKey(ctx)
	if err != nil || pub != author {
		return nil, errors.New("old signer does not match quarantined identity")
	}
	targets := map[nostr.ID]nostr.Kind{}
	for _, e := range observed {
		id, err := nostr.IDFromHex(e.ID)
		if err != nil || id.Hex() != e.ID {
			return nil, errors.New("invalid deletion inventory")
		}
		targets[id] = nostr.Kind(e.Kind)
	}
	return &QuarantinedOldSigner{keyer: keyer, author: author, targets: targets}, nil
}
func (q *QuarantinedOldSigner) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	return q.keyer.Decrypt(ctx, ciphertext, sender)
}
func (q *QuarantinedOldSigner) SignDeletion(ctx context.Context, ev *nostr.Event) error {
	if ev == nil || ev.Kind != nostr.KindDeletion || ev.PubKey != q.author || ev.Content != "" || len(ev.Tags) == 0 {
		return errors.New("old signer permits only scoped kind-5 deletion")
	}
	seen := map[nostr.ID]struct{}{}
	kinds := map[nostr.Kind]struct{}{}
	declared := map[nostr.Kind]struct{}{}
	for _, tag := range ev.Tags {
		if len(tag) != 2 || strings.Contains(strings.ToLower(tag[1]), "nsec1") {
			return errors.New("unsafe deletion tag")
		}
		switch tag[0] {
		case "e":
			id, err := nostr.IDFromHex(tag[1])
			if err != nil || id.Hex() != tag[1] {
				return errors.New("invalid deletion e-tag")
			}
			kind, ok := q.targets[id]
			if !ok {
				return errors.New("deletion e-tag outside observed inventory")
			}
			if _, dup := seen[id]; dup {
				return errors.New("duplicate deletion e-tag")
			}
			seen[id] = struct{}{}
			kinds[kind] = struct{}{}
		case "k":
			value, err := strconv.Atoi(tag[1])
			if err != nil || strconv.Itoa(value) != tag[1] {
				return errors.New("invalid deletion k-tag")
			}
			declared[nostr.Kind(value)] = struct{}{}
		default:
			return fmt.Errorf("old signer refuses tag %s", tag[0])
		}
	}
	if len(seen) == 0 || len(seen) > 500 || len(kinds) != len(declared) {
		return errors.New("invalid deletion scope")
	}
	for k := range kinds {
		if _, ok := declared[k]; !ok {
			return errors.New("deletion kind mismatch")
		}
	}
	if err := q.keyer.SignEvent(ctx, ev); err != nil {
		return errors.New("old signer refused deletion")
	}
	if ev.PubKey != q.author || !ev.CheckID() || !ev.VerifySignature() {
		return errors.New("old signer returned invalid deletion")
	}
	return nil
}
func (e *Engine) deleteAndPurge(ctx context.Context, s *State) ([]string, error) {
	if e.Purge == nil {
		return nil, fmt.Errorf("purge: %w", ErrAdapterNotRegistered)
	}
	if e.OldSigner == nil {
		return nil, errors.New("quarantined old signer required")
	}
	keyer, err := e.OldSigner(ctx)
	if err != nil {
		return nil, errors.New("old signer unavailable")
	}
	q, err := NewQuarantinedOldSigner(ctx, keyer, e.Config.OldPubkey, s.Observed)
	if err != nil {
		return nil, err
	}
	{
		for start := len(s.DeletionIDs) * 500; start < len(s.Observed); start += 500 {
			end := min(start+500, len(s.Observed))
			batch := s.Observed[start:end]
			tags := make(nostr.Tags, 0, len(batch)+8)
			kinds := map[int]struct{}{}
			for _, item := range batch {
				tags = append(tags, nostr.Tag{"e", item.ID})
				kinds[item.Kind] = struct{}{}
			}
			kindNumbers := make([]int, 0, len(kinds))
			for k := range kinds {
				kindNumbers = append(kindNumbers, k)
			}
			slices.Sort(kindNumbers)
			for _, k := range kindNumbers {
				tags = append(tags, nostr.Tag{"k", strconv.Itoa(k)})
			}
			ev := nostr.Event{Kind: nostr.KindDeletion, PubKey: e.Config.OldPubkey, CreatedAt: e.nextTimestamp(s), Tags: tags}
			if err = q.SignDeletion(ctx, &ev); err != nil {
				return nil, err
			}
			s.Records[ev.ID.Hex()] = ev
			s.DeletionIDs = append(s.DeletionIDs, ev.ID.Hex())
			s.Pending = append(s.Pending, ev.ID.Hex())
			if err = e.Ledger.Save(*s); err != nil {
				return nil, err
			}
		}
	}
	if err = e.publishPending(ctx, s); err != nil {
		return nil, err
	}
	ids := make([]nostr.ID, 0, len(s.Observed))
	for _, item := range s.Observed {
		id, _ := nostr.IDFromHex(item.ID)
		ids = append(ids, id)
	}
	receipts, err := e.Purge.Purge(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(receipts) == 0 {
		return nil, fmt.Errorf("purge receipts: %w", ErrIncomplete)
	}
	return append(append([]string{}, s.DeletionIDs...), receipts...), nil
}
