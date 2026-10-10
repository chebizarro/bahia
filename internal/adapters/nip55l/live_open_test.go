//go:build nip55llive

package nip55l_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nip55l"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/servicesigner"
)

// TestLiveServiceSignerOpen opens the service identity the way Bahia does
// at startup (nostr.signer.method=nip55l) against the provisioned daemon.
func TestLiveServiceSignerOpen(t *testing.T) {
	f := nip55l.LiveProvision(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := config.NostrConfig{PublicKey: f.Service.Hex(), Signer: config.NostrSignerConfig{
		Method: config.NostrSignerNIP55L,
		NIP55L: config.NostrSignerNIP55LConfig{AppID: f.AppID, BusAddress: f.BusAddress},
	}}
	signer, err := servicesigner.Open(ctx, cfg, servicesigner.Options{})
	if err != nil {
		t.Fatalf("servicesigner.Open: %v", err)
	}
	defer closeKeyer(signer)
	if _, ok := signer.(servicesigner.BinaryCipher); !ok {
		t.Error("nip55l service signer does not implement servicesigner.BinaryCipher")
	}
	evt := &nostr.Event{Kind: 1, Content: "servicesigner.Open live"}
	if err := signer.SignEvent(ctx, evt); err != nil || evt.PubKey != f.Service || !evt.VerifySignature() {
		t.Fatalf("SignEvent = %v (pubkey %s)", err, evt.PubKey.Hex())
	}

	// The daemon's active identity is the decoy; it is stored too.
	cfg.PublicKey = f.Decoy.Hex()
	decoy, err := servicesigner.Open(ctx, cfg, servicesigner.Options{})
	if err != nil {
		t.Fatalf("servicesigner.Open as the decoy: %v", err)
	}
	closeKeyer(decoy)

	cfg.PublicKey = nostr.Generate().Public().Hex()
	if s, err := servicesigner.Open(ctx, cfg, servicesigner.Options{}); err == nil {
		closeKeyer(s)
		t.Fatal("Open accepted a pubkey the signer does not hold")
	} else if !errors.Is(err, nip55l.ErrIdentityNotFound) {
		t.Fatalf("Open(unknown pubkey) = %v, want nip55l.ErrIdentityNotFound", err)
	}
}

func closeKeyer(k nostr.Keyer) {
	if c, ok := k.(io.Closer); ok {
		_ = c.Close()
	}
}
