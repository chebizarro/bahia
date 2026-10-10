package main

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/relaysidecar"
	"go.uber.org/zap"
)

// TestReloadWithRealSidecarSharesTheEventStore: a SIGHUP prepares the
// replacement runtime while the active one still holds the bbolt event store
// under the same data_dir. The replacement must open it without waiting on
// the file lock its own process holds, and take over once the old one stops.
func TestReloadWithRealSidecarSharesTheEventStore(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.ListenAddr = "127.0.0.1:0"
	cfg.Nostr.Sidecar.DataDir = t.TempDir()
	cfg.Nostr.PrivateKey = nostr.Generate().Hex()
	supervisor := &runtimeSupervisor{
		rootCtx:    t.Context(),
		logger:     zap.NewNop(),
		factory:    newSidecar,
		openSigner: openServiceSigner,
	}
	if err := supervisor.replace(cfg); err != nil {
		t.Fatalf("initial runtime: %v", err)
	}
	signer := supervisor.signer
	for range 2 {
		if err := supervisor.replace(cfg); err != nil {
			t.Fatalf("replace() error = %v, want the replacement to share the open event store", err)
		}
	}
	if supervisor.signer != signer {
		t.Fatal("an unchanged nostr.signer must keep the open service signer")
	}
	if err := supervisor.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	// Every handle is closed now, so a fresh open takes the file lock.
	fresh, err := openServiceSigner(t.Context(), cfg.Nostr)
	if err != nil {
		t.Fatalf("open service signer: %v", err)
	}
	defer fresh.Close()
	reopened, err := relaysidecar.New(t.Context(), cfg.Nostr, fresh.keyer, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen after stop: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened runtime: %v", err)
	}
}
