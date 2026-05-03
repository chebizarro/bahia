package controlplane

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	gonostr "github.com/nbd-wtf/go-nostr"
)

// NewPrivateKeySigner builds the canonical signer used by control-plane signing
// paths from a legacy hex private key. Empty keys return nil so callers can gate
// startup on signer availability instead of checking raw key strings.
func NewPrivateKeySigner(privateKeyHex string) (canonicalnostr.Signer, error) {
	privateKeyHex = strings.TrimSpace(privateKeyHex)
	if privateKeyHex == "" {
		return nil, nil
	}

	decoded, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode nostr private key: %w", err)
	}
	if len(decoded) != 32 {
		return nil, fmt.Errorf("nostr private key must be 32 bytes, got %d", len(decoded))
	}

	var secret [32]byte
	copy(secret[:], decoded)
	signer := keyer.NewPlainKeySigner(secret)
	return signer, nil
}

// SignGoNostrEvent adapts Bahia's current go-nostr event values to the
// canonical signer contract. It signs via fiatjaf.com/nostr.Signer and copies
// the signed event fields back into the go-nostr event used by the rest of the
// control plane.
func SignGoNostrEvent(ctx context.Context, signer canonicalnostr.Signer, ev *gonostr.Event) error {
	if ev == nil {
		return fmt.Errorf("nostr event is nil")
	}
	if signer == nil {
		return fmt.Errorf("control-plane signer is not configured")
	}

	canonicalEvent := toCanonicalEvent(ev)
	if err := signer.SignEvent(ctx, &canonicalEvent); err != nil {
		return err
	}
	copyCanonicalEvent(ev, canonicalEvent)
	return nil
}

func toCanonicalEvent(ev *gonostr.Event) canonicalnostr.Event {
	return canonicalnostr.Event{
		CreatedAt: canonicalnostr.Timestamp(ev.CreatedAt),
		Kind:      canonicalnostr.Kind(ev.Kind),
		Tags:      toCanonicalTags(ev.Tags),
		Content:   ev.Content,
	}
}

func copyCanonicalEvent(dst *gonostr.Event, src canonicalnostr.Event) {
	dst.ID = src.ID.Hex()
	dst.PubKey = src.PubKey.Hex()
	dst.CreatedAt = gonostr.Timestamp(src.CreatedAt)
	dst.Kind = int(src.Kind)
	dst.Tags = toGoNostrTags(src.Tags)
	dst.Content = src.Content
	dst.Sig = canonicalnostr.HexEncodeToString(src.Sig[:])
}

func toCanonicalTags(tags gonostr.Tags) canonicalnostr.Tags {
	converted := make(canonicalnostr.Tags, 0, len(tags))
	for _, tag := range tags {
		converted = append(converted, canonicalnostr.Tag(append([]string(nil), tag...)))
	}
	return converted
}

func toGoNostrTags(tags canonicalnostr.Tags) gonostr.Tags {
	converted := make(gonostr.Tags, 0, len(tags))
	for _, tag := range tags {
		converted = append(converted, gonostr.Tag(append([]string(nil), tag...)))
	}
	return converted
}
