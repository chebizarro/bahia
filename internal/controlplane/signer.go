package controlplane

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
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

// SignNostrEvent signs a canonical fiatjaf.com/nostr event with the configured
// control-plane signer.
func SignNostrEvent(ctx context.Context, signer canonicalnostr.Signer, ev *canonicalnostr.Event) error {
	if ev == nil {
		return fmt.Errorf("nostr event is nil")
	}
	if signer == nil {
		return fmt.Errorf("control-plane signer is not configured")
	}
	return signer.SignEvent(ctx, ev)
}

// SignGoNostrEvent is retained as a source-compatible wrapper for older
// control-plane call sites; the event type is now the canonical fiatjaf module.
func SignGoNostrEvent(ctx context.Context, signer canonicalnostr.Signer, ev *canonicalnostr.Event) error {
	return SignNostrEvent(ctx, signer, ev)
}
