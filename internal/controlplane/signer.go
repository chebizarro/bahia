package controlplane

import (
	"context"
	"fmt"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// NewPrivateKeySigner builds a local-key signer for operator tools and tests.
// Empty keys return nil. The daemon builds its service identity in
// internal/app/service_keyer.go instead.
func NewPrivateKeySigner(privateKeyHex string) (casnostr.Signer, error) {
	if strings.TrimSpace(privateKeyHex) == "" {
		return nil, nil
	}
	signer, err := nostrutil.NewLocalKeyer(privateKeyHex)
	if err != nil {
		return nil, err
	}
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
