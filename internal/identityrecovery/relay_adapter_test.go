package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func relayRecoveryFixture(t *testing.T) (*RelayRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer, nostr.Event) {
	t.Helper()
	oldKey := signer(t)
	url := "wss://control.example"
	state := controlplane.RelayPolicyState{Schema: controlplane.RelaySettingsSchema, BrowserRelays: []string{url}, ContextVMRelays: []string{url}, ServiceRelays: []string{url}}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	settingsTags := nostr.Tags{{"d", controlplane.RelaySettingsDTag}, {"domain", controlplane.RelaySettingsDomain}, {"entity", "relay-policy"}, {"schema", controlplane.RelaySettingsSchema}, {"t", kinds.RelaySettingsTopic}, {"status", "applied"}, {"p", pub(t, oldKey).Hex()}}
	settings := signedRegistryHead(t, oldKey, kinds.CASControlState, settingsTags, string(body), 100)
	setTags, setContent, err := adapter.RelaySetRecoveryRecord(adapter.ServiceRelaySetDTag, []string{url})
	if err != nil {
		t.Fatal(err)
	}
	set := signedRegistryHead(t, oldKey, kinds.RelaySetDiscovery, setTags, setContent, 101)
	base, relay, local, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{settings, set})
	a := &RelayRecoveryAdapter{OldPubkey: settings.PubKey, Source: relay, Local: local, Ledger: base.Ledger, Static: config.NostrConfig{ControlledRelays: []string{url}, ServiceRelays: []string{url}, BrowserRelays: []string{url}, ContextVMRelays: []string{url}}}
	return a, relay, local, newKey, settings
}

func TestRelayRecoveryRoundTripIdempotenceAndEquivalence(t *testing.T) {
	a, relay, local, key, settings := relayRecoveryFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != 2 || !slices.Contains(ids, settings.ID.Hex()) {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || local.writes != 2 {
		t.Fatalf("writes relay=%d local=%d", relay.publishes, local.writes)
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 2 {
		t.Fatalf("verify %v: %v", proof, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || local.writes != 2 {
		t.Fatal("idempotent rerun wrote heads")
	}
	bad := signedRegistryHead(t, key, kinds.CASControlState, settings.Tags, `{"schema":"changed"}`, time.Now().Unix()+100)
	relay.events = append(relay.events, bad)
	local.events = append(local.events, bad)
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent verify: %v", err)
	}
	if _, err := a.Reassert(ctx, key); err == nil || relay.publishes != 2 {
		t.Fatalf("divergent reassert: %v", err)
	}
}

func TestRelayRecoveryRefusesUnreproducibleOrIncompleteHead(t *testing.T) {
	a, relay, _, key, _ := relayRecoveryFixture(t)
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil {
		t.Fatal("incomplete relay read accepted as inventory")
	}
	if _, err := a.Reassert(context.Background(), key); err == nil || relay.publishes != 0 {
		t.Fatalf("incomplete relay read published: %v", err)
	}
}
