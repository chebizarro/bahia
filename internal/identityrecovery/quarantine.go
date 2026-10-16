package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/deletionguard"
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
	if ev == nil {
		return errors.New("deletion required")
	}
	if err := deletionguard.Check(*ev, q.author, q.targets, nil); err != nil {
		return err
	}
	if err := q.keyer.SignEvent(ctx, ev); err != nil {
		return errors.New("old signer refused deletion")
	}
	return deletionguard.CheckSigned(*ev, q.author, q.targets, nil)
}
func (e *Engine) deleteAndPurge(ctx context.Context, s *State) ([]string, error) {
	if err := e.requireNotificationReencryption(ctx, *s, nil); err != nil {
		return nil, err
	}
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
	if err := validatePurgeEvidence(receipts); err != nil {
		return nil, fmt.Errorf("purge receipts: %w", err)
	}
	evidence := append([]string{"deletion-complete"}, s.DeletionIDs...)
	if slices.Contains(e.RequiredDomains, "notification-channels") {
		proof, err := e.Domains["notification-channels"].Verify(ctx)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, proof...)
	}
	for _, id := range s.DeletionIDs {
		for _, relay := range e.Config.ControlledRelays {
			sum := sha256.Sum256([]byte(relay))
			evidence = append(evidence, "delete-ok:"+hex.EncodeToString(sum[:])+":"+id)
		}
	}
	return append(evidence, receipts...), nil
}
