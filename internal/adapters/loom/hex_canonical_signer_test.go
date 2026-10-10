package loom

import (
	"context"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// HexKeyCanonicalSigner adapts a test hex private key to CanonicalSigner.
type HexKeyCanonicalSigner struct {
	PrivateKey string
}

func (s HexKeyCanonicalSigner) Sign(_ context.Context, event *nostr.Event) error {
	if strings.TrimSpace(s.PrivateKey) == "" {
		return fmt.Errorf("canonical Loom projection signer is not configured")
	}
	return nostrutil.SignEventWithHexKey(event, s.PrivateKey)
}

// testKeyer is the local-mode service Keyer for a test key.
func testKeyer(privateKeyHex string) nostr.Keyer {
	signer, err := nostrutil.NewLocalKeyer(privateKeyHex)
	if err != nil {
		panic(err)
	}
	return signer
}
