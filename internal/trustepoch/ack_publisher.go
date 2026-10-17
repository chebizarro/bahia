package trustepoch

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// AckPublisher binds an enforcing consumer's signer to its own durable read
// and publish path. A verified receipt for the exact head makes retries and
// restarts idempotent; a receipt for another head never does.
type AckPublisher struct {
	ConsumerID   string
	Authority    string
	OperatorRoot string
	Existing     func(context.Context, nostr.Filter) ([]nostr.Event, error)
	Sign         func(context.Context, *nostr.Event) error
	Publish      func(context.Context, nostr.Event) error
}

func (p AckPublisher) Acknowledge(ctx context.Context, resolved Resolution) error {
	if resolved.Record == nil || !resolved.Record.Activated {
		return nil
	}
	if p.Existing == nil || p.Sign == nil || p.Publish == nil || !validHex64(p.Authority) ||
		!validHex64(p.OperatorRoot) || p.Authority == p.OperatorRoot {
		return fmt.Errorf("trust epoch acknowledgment publisher is not configured")
	}
	switch p.ConsumerID {
	case "daemon":
		if p.Authority != resolved.Pubkey {
			return fmt.Errorf("daemon acknowledgment signer differs from activated identity")
		}
	case "sidecar", "dns":
		if p.Authority == resolved.Pubkey || p.Authority == resolved.Record.OldPubkey {
			return fmt.Errorf("component acknowledgment signer is not independent")
		}
	default:
		return fmt.Errorf("consumer has no production acknowledgment publisher")
	}
	pubkey, err := nostr.PubKeyFromHex(p.Authority)
	if err != nil {
		return err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey}, Tags: nostr.TagMap{"d": {"trust-epoch-ack:" + resolved.ServiceID + ":" + p.ConsumerID}}}
	existing, err := p.Existing(ctx, filter)
	if err != nil {
		return err
	}
	for _, ev := range existing {
		if p.ConsumerID == "daemon" && VerifyAcknowledgment(ev, resolved, "daemon") == nil {
			return nil
		}
		if p.ConsumerID != "daemon" && VerifyAcknowledgmentAuthority(ev, resolved, p.ConsumerID, p.Authority, false) == nil {
			return nil
		}
	}
	ack, err := NewAcknowledgment(resolved, p.ConsumerID, nostr.Now())
	if err != nil {
		return err
	}
	if err := p.Sign(ctx, &ack); err != nil {
		return fmt.Errorf("sign trust epoch acknowledgment: %w", err)
	}
	if err := VerifyAcknowledgmentAuthority(ack, resolved, p.ConsumerID, p.Authority, false); err != nil {
		return err
	}
	return p.Publish(ctx, ack)
}
