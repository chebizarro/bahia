package client

import (
	"context"
	"slices"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// TestPrepareOperatorAttemptSendsRequestsTooLargeToStoreAsEphemeralWraps: a
// NIP-59 request whose 1059 wrap would exceed what Bahia's relay stores is sent
// as a 21059 wrap of the same rumor, so it is relayed instead of refused. Small
// requests keep the stored 1059 wrap, and the reply filter covers both kinds.
func TestPrepareOperatorAttemptSendsRequestsTooLargeToStoreAsEphemeralWraps(t *testing.T) {
	ctx := context.Background()
	operatorSecret, serviceSecret := nostr.Generate(), nostr.Generate()
	operatorKeyer := keyer.NewPlainKeySigner(operatorSecret)
	serviceKeyer := keyer.NewPlainKeySigner(serviceSecret)
	servicePubkey := serviceSecret.Public().Hex()
	c := &ContextVMRequestClient{
		signer: operatorKeyer, cipher: operatorKeyer, pubkey: operatorSecret.Public().Hex(),
		servicePubkey: servicePubkey, encrypted: true,
	}
	for _, tc := range []struct {
		name  string
		value int
		want  nostr.Kind
	}{
		{name: "fits a stored wrap", value: 1000, want: nostr.Kind(controlplane.KindContextVMGiftWrap)},
		// ~40 KB rumor: its seal encrypts to a wrap of ~76 KB content.
		{name: "too large to store", value: 40_000, want: nostr.Kind(controlplane.KindContextVMEphemeralWrap)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inner := &nostr.Event{
				Kind:      nostr.Kind(controlplane.KindContextVMMessage),
				CreatedAt: nostr.Now(),
				Tags:      nostr.Tags{{"method", "secrets/set"}, {"p", servicePubkey}},
				Content:   `{"jsonrpc":"2.0","id":"1","method":"secrets/set","params":{"value":"` + strings.Repeat("x", tc.value) + `"}}`,
			}
			if err := controlplane.SignGoNostrEvent(ctx, operatorKeyer, inner); err != nil {
				t.Fatal(err)
			}
			outer, filters, outerIDs, err := c.prepareOperatorAttempt(ctx, inner, nil)
			if err != nil {
				t.Fatalf("prepareOperatorAttempt: %v", err)
			}
			if outer.Kind != tc.want {
				t.Fatalf("outer kind = %d (content %d bytes), want %d", outer.Kind, len(outer.Content), tc.want)
			}
			if outer.Kind == nostr.Kind(controlplane.KindContextVMGiftWrap) && len(outer.Content) > maxStoredGiftWrapContentBytes {
				t.Fatalf("stored wrap content %d bytes exceeds %d", len(outer.Content), maxStoredGiftWrapContentBytes)
			}
			rumor, err := cascontextvm.UnwrapNIP59(ctx, serviceKeyer, outer)
			if err != nil || rumor.ID != inner.ID {
				t.Fatalf("service unwrap: rumor=%v err=%v", rumor, err)
			}
			if len(outerIDs) != 1 || outerIDs[0] != outer.ID.Hex() {
				t.Fatalf("outer ids = %v", outerIDs)
			}
			if !slices.Contains(filters[0].Kinds, nostr.Kind(controlplane.KindContextVMEphemeralWrap)) || !slices.Contains(filters[0].Kinds, nostr.Kind(controlplane.KindContextVMGiftWrap)) {
				t.Fatalf("reply filter kinds = %v, want both wrap kinds", filters[0].Kinds)
			}
		})
	}
}
