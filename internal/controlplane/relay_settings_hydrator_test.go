package controlplane

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

func TestRelaySettingsHydratorFilterIsScopedToCanonicalState(t *testing.T) {
	servicePubkey, _ := nostr.GetPublicKey(testServiceKey)
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{ServicePubkey: servicePubkey, Logger: zap.NewNop()})

	filter := h.filter()
	if len(filter.Kinds) != 1 || filter.Kinds[0] != kinds.CASControlState {
		t.Fatalf("unexpected filter kinds: %#v", filter.Kinds)
	}
	if len(filter.Authors) != 1 || filter.Authors[0] != servicePubkey {
		t.Fatalf("unexpected filter authors: %#v", filter.Authors)
	}
	if got := filter.Tags["d"]; len(got) != 1 || got[0] != RelaySettingsDTag {
		t.Fatalf("unexpected d tag filter: %#v", got)
	}
	if got := filter.Tags["domain"]; len(got) != 1 || got[0] != RelaySettingsDomain {
		t.Fatalf("unexpected domain tag filter: %#v", got)
	}
	if got := filter.Tags["schema"]; len(got) != 1 || got[0] != RelaySettingsSchema {
		t.Fatalf("unexpected schema tag filter: %#v", got)
	}
}

func TestRelaySettingsHydratorStoresLatestValidReplaceableStateWithoutMutatingConfig(t *testing.T) {
	servicePubkey, _ := nostr.GetPublicKey(testServiceKey)
	cfg := &config.Config{Nostr: config.NostrConfig{BrowserRelays: []string{"wss://old.example"}}}
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		ServicePubkey: servicePubkey,
		Logger:        zap.NewNop(),
		Now:           func() time.Time { return time.Unix(2_000, 0).UTC() },
	})

	newer := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0).UTC(), RelayPolicyState{
		Schema:          RelaySettingsSchema,
		BrowserRelays:   []string{"wss://browser.example", "wss://browser.example"},
		ContextVMRelays: []string{"wss://contextvm.example"},
		ServiceRelays:   []string{"wss://service.example"},
	})
	older := signedRelaySettingsStateEvent(t, time.Unix(900, 0).UTC(), RelayPolicyState{
		Schema:        RelaySettingsSchema,
		BrowserRelays: []string{"wss://older.example"},
	})

	if !h.handleEvent(context.Background(), newer) {
		t.Fatalf("expected newer relay settings event to apply")
	}
	if got := cfg.Nostr.BrowserRelays; len(got) != 1 || got[0] != "wss://old.example" {
		t.Fatalf("hydrator must not mutate shared runtime config: %#v", got)
	}
	snapshot, ok := h.Snapshot()
	if !ok {
		t.Fatalf("expected hydrated snapshot")
	}
	if got := snapshot.BrowserRelays; len(got) != 1 || got[0] != "wss://browser.example" {
		t.Fatalf("browser relays not snapshotted: %#v", got)
	}
	if got := snapshot.ContextVMRelays; len(got) != 1 || got[0] != "wss://contextvm.example" {
		t.Fatalf("contextvm relays not snapshotted: %#v", got)
	}
	if got := snapshot.ServiceRelays; len(got) != 1 || got[0] != "wss://service.example" {
		t.Fatalf("service relays not snapshotted: %#v", got)
	}

	snapshot.BrowserRelays[0] = "wss://caller-mutated.example"
	again, ok := h.Snapshot()
	if !ok || len(again.BrowserRelays) != 1 || again.BrowserRelays[0] != "wss://browser.example" {
		t.Fatalf("snapshot must be cloned for callers: %#v", again.BrowserRelays)
	}

	if h.handleEvent(context.Background(), older) {
		t.Fatalf("older replaceable event should not apply")
	}
	afterOlder, ok := h.Snapshot()
	if !ok || len(afterOlder.BrowserRelays) != 1 || afterOlder.BrowserRelays[0] != "wss://browser.example" {
		t.Fatalf("older event replaced current snapshot: %#v", afterOlder.BrowserRelays)
	}
	if h.handleEvent(context.Background(), newer) {
		t.Fatalf("duplicate event should not apply")
	}
}

func TestRelaySettingsHydratorTieBreaksEqualTimestampsByLowestEventID(t *testing.T) {
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{Logger: zap.NewNop()})
	if !h.shouldApply(&nostr.Event{ID: "bbbb", CreatedAt: nostr.Timestamp(100)}) {
		t.Fatalf("first event should apply")
	}
	if !h.shouldApply(&nostr.Event{ID: "aaaa", CreatedAt: nostr.Timestamp(100)}) {
		t.Fatalf("lower event id should win same-created_at replaceable tie")
	}
	if h.shouldApply(&nostr.Event{ID: "cccc", CreatedAt: nostr.Timestamp(100)}) {
		t.Fatalf("higher event id should not replace same-created_at winner")
	}
}

func TestRelaySettingsHydratorRejectsWrongAuthorOrSchema(t *testing.T) {
	servicePubkey, _ := nostr.GetPublicKey(testServiceKey)
	otherPubkey, _ := nostr.GetPublicKey(testRequesterKey)
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		ServicePubkey: servicePubkey,
		Logger:        zap.NewNop(),
		Now:           func() time.Time { return time.Unix(2_000, 0).UTC() },
	})

	wrongAuthor := signedRelaySettingsStateEventWithKey(t, testRequesterKey, time.Unix(1_000, 0).UTC(), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://browser.example"}})
	if wrongAuthor.PubKey != otherPubkey {
		t.Fatalf("test event author mismatch: %s", wrongAuthor.PubKey)
	}
	if h.handleEvent(context.Background(), wrongAuthor) {
		t.Fatalf("wrong service author should not apply")
	}

	wrongSchema := signedRelaySettingsStateEvent(t, time.Unix(1_001, 0).UTC(), RelayPolicyState{Schema: "wrong", BrowserRelays: []string{"wss://browser.example"}})
	if h.handleEvent(context.Background(), wrongSchema) {
		t.Fatalf("wrong content schema should not apply")
	}
}

func TestRelaySettingsHydratorAcceptsTopologyEmptyCanonicalState(t *testing.T) {
	servicePubkey, _ := nostr.GetPublicKey(testServiceKey)
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		ServicePubkey: servicePubkey,
		Logger:        zap.NewNop(),
		Now:           func() time.Time { return time.Unix(2_000, 0).UTC() },
	})
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0).UTC(), RelayPolicyState{
		Schema: RelaySettingsSchema,
		DMRelayLists: []RelayPolicyDMRelayList{{
			Enabled:  true,
			Feature:  config.DMRelayListFeatureNotifications,
			Identity: config.DMRelayListIdentityService,
			Relays:   []string{"wss://dm.example"},
		}},
	})

	if !h.handleEvent(context.Background(), event) {
		t.Fatalf("expected DM-only canonical state to hydrate")
	}
	snapshot, ok := h.Snapshot()
	if !ok {
		t.Fatalf("expected hydrated snapshot")
	}
	if len(snapshot.BrowserRelays)+len(snapshot.ContextVMRelays)+len(snapshot.ServiceRelays) != 0 {
		t.Fatalf("expected no core relay topology in snapshot: %#v", snapshot)
	}
	if len(snapshot.DMRelayLists) != 1 || len(snapshot.DMRelayLists[0].Relays) != 1 || snapshot.DMRelayLists[0].Relays[0] != "wss://dm.example" {
		t.Fatalf("DM-only canonical state not preserved: %#v", snapshot.DMRelayLists)
	}
}

func TestRelaySettingsHydratorProcessesEventAndEOSEDeterministically(t *testing.T) {
	servicePubkey, _ := nostr.GetPublicKey(testServiceKey)
	event := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0).UTC(), RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://browser.example"}})
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
		Pool:          nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
		ServicePubkey: servicePubkey,
		Logger:        zap.NewNop(),
		Now:           func() time.Time { return time.Unix(2_000, 0).UTC() },
	})

	original := relaySettingsSubscribeAllWithEOSE
	relaySettingsSubscribeAllWithEOSE = func(_ *nostradapter.RelayPool, ctx context.Context, filters []nostr.Filter) (*nostradapter.MergedSubscription, error) {
		if len(filters) != 1 || filters[0].Tags["d"][0] != RelaySettingsDTag {
			t.Fatalf("unexpected subscription filters: %#v", filters)
		}
		return scriptedRelaySettingsMergedSubscription(ctx, []*nostr.Event{event}, true), nil
	}
	t.Cleanup(func() { relaySettingsSubscribeAllWithEOSE = original })

	if err := h.subscribe(context.Background()); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if !h.IsCaughtUp() {
		t.Fatalf("expected EOSE to mark hydrator caught up")
	}
	snapshot, ok := h.Snapshot()
	if !ok || len(snapshot.BrowserRelays) != 1 || snapshot.BrowserRelays[0] != "wss://browser.example" {
		t.Fatalf("event was not snapshotted: %#v", snapshot.BrowserRelays)
	}
}

func TestRelaySettingsHydratorHandlesClosedAuthWithoutPolling(t *testing.T) {
	h := NewRelaySettingsHydrator(RelaySettingsHydratorConfig{Logger: zap.NewNop()})
	if h.handleRelayClosed(context.Background(), nostradapter.RelayClosed{RelayURL: "wss://relay.example", Reason: "auth-required: sign in"}, map[string]struct{}{}) {
		t.Fatalf("nil relay pool cannot satisfy AUTH and should not report resubscribe success")
	}
}

func signedRelaySettingsStateEvent(t *testing.T, createdAt time.Time, state RelayPolicyState) *nostr.Event {
	t.Helper()
	return signedRelaySettingsStateEventWithKey(t, testServiceKey, createdAt, state)
}

func signedRelaySettingsStateEventWithKey(t *testing.T, privateKey string, createdAt time.Time, state RelayPolicyState) *nostr.Event {
	t.Helper()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	event := &nostr.Event{
		Kind:      kinds.CASControlState,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags: nostr.Tags{
			{"d", RelaySettingsDTag},
			{"domain", RelaySettingsDomain},
			{"schema", RelaySettingsSchema},
		},
		Content: string(content),
	}
	if err := SignGoNostrEvent(context.Background(), signer, event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	return event
}

func scriptedRelaySettingsMergedSubscription(ctx context.Context, events []*nostr.Event, eose bool) *nostradapter.MergedSubscription {
	eventCh := make(chan *nostr.Event)
	eoseCh := make(chan struct{})
	relayEOSE := make(chan nostradapter.RelayEOSE, 1)
	closed := make(chan nostradapter.RelayClosed)
	go func() {
		defer close(relayEOSE)
		defer close(closed)
		for _, event := range events {
			select {
			case eventCh <- event:
			case <-ctx.Done():
				close(eventCh)
				return
			}
		}
		if eose {
			relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://relay.example", SubscriptionID: "relay-settings"}
			close(eoseCh)
			select {
			case <-ctx.Done():
			case eventCh <- nil:
			}
		} else {
			close(eoseCh)
		}
		close(eventCh)
	}()
	return &nostradapter.MergedSubscription{
		Events:            eventCh,
		EndOfStoredEvents: eoseCh,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
	}
}
