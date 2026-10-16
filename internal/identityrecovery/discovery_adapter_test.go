package identityrecovery

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestDiscoveryRecoveryRegeneratesFromLiveConfiguration(t *testing.T) {
	oldSigner := signer(t)
	old := signedRegistryHead(t, oldSigner, kinds.SystemDiscovery, nostr.Tags{{"d", adapter.SystemDiscoveryDTag}, {"schema", adapter.SystemDiscoverySchema}, {"name", adapter.SystemDiscoveryName}}, `{"schema":"bahia.system-discovery.v1","registries":{},"control_plane":{},"features":{"old":true}}`, 100)
	base, relay, local, newSigner := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	cfg := config.Defaults()
	cfg.Nostr.BrowserRelays = []string{"wss://browser.example"}
	a := &DiscoveryRecoveryAdapter{OldPubkey: old.PubKey, Source: relay, Local: local, Ledger: base.Ledger, Config: cfg, MCPTransportEnabled: true}
	ctx := context.Background()
	if _, err := a.Inventory(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reassert(ctx, newSigner); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 1 || local.writes != 1 {
		t.Fatalf("writes=%d/%d", relay.publishes, local.writes)
	}
	newHead := relay.events[len(relay.events)-1]
	if newHead.Content == old.Content {
		t.Fatal("stale old-key content copied instead of regenerated")
	}
	var body map[string]any
	if json.Unmarshal([]byte(newHead.Content), &body) != nil {
		t.Fatal("new discovery JSON invalid")
	}
	if body["schema"] != adapter.SystemDiscoverySchema {
		t.Fatal("new discovery schema invalid")
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 1 {
		t.Fatalf("verify: %v %v", proof, err)
	}
	if _, err := a.Reassert(ctx, newSigner); err != nil || relay.publishes != 1 {
		t.Fatalf("idempotence: %v", err)
	}
	wrong := signedRegistryHead(t, newSigner, kinds.ContextVMServerAnnouncement, nostr.Tags{{"d", "other"}, {"schema", adapter.SystemDiscoverySchema}, {"name", adapter.SystemDiscoveryName}}, newHead.Content, int64(newHead.CreatedAt)+100)
	relay.events = append(relay.events, wrong)
	local.events = append(local.events, wrong)
	if _, err := a.Verify(ctx); err == nil {
		t.Fatal("newer wrong-d 11316 head was hidden")
	}
}
