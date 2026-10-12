package main

import (
	"context"
	"fmt"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relayguard"
)

// serviceRelayGuard checks the relays a service-signing migration publishes
// to against the daemon's relay rules (nostr.controlled_relays, no public
// relay, publish quorum) and returns the guard for the pool, which then
// checks every EVENT frame the same way the daemon's pools do.
func serviceRelayGuard(ctx context.Context, cfg config.NostrConfig, signer nostr.Signer, relays []string) (*relayguard.Guard, error) {
	if err := cfg.ValidateControlPlaneRelays("bahia-migrate target relays:", config.ControlPlaneRelays{Service: relays}); err != nil {
		return nil, err
	}
	pubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("service pubkey: %w", err)
	}
	return relayguard.New(pubkey, cfg.ControlledRelaySet())
}
