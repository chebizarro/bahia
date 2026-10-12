package main

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// `bahia-migrate nostr --relays wss://relay.damus.io` and f74a-import sign
// cp-state with the service key; their targets obey the daemon's relay rules.
func TestServiceRelayGuardRefusesPublicAndUncontrolledTargets(t *testing.T) {
	secret := nostr.Generate()
	signer, err := nostrutil.NewLocalKeyer(secret.Hex())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.NostrConfig{
		PrivateKey:       secret.Hex(),
		PublishEnabled:   true,
		PublishQuorum:    1,
		ControlledRelays: []string{"wss://bahia.sharegap.net/relay", "wss://relay.sharegap.net"},
	}
	for relay, want := range map[string]string{
		"wss://relay.damus.io":       "public relay",
		"wss://uncontrolled.example": "not in nostr.controlled_relays",
	} {
		if _, err := serviceRelayGuard(t.Context(), cfg, signer, []string{"wss://relay.sharegap.net", relay}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("target %s: err = %v, want %q", relay, err, want)
		}
	}
	guard, err := serviceRelayGuard(t.Context(), cfg, signer, []string{"wss://bahia.sharegap.net/relay", "wss://relay.sharegap.net"})
	if err != nil || guard == nil {
		t.Fatalf("controlled targets: guard=%v err=%v", guard != nil, err)
	}
	if guard.Check("wss://relay.damus.io", nostr.Event{PubKey: secret.Public(), Kind: 30900}) == nil {
		t.Fatal("the migration pool's guard must refuse cp-state to a public relay")
	}
}
