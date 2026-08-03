package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/relayadmin"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type fakeRelayAdminClient struct {
	calls []relayAdminCallPayload
}

func (f *fakeRelayAdminClient) SupportedMethods(context.Context, string) ([]string, error) {
	return []string{relayadmin.MethodSupportedMethods, relayadmin.MethodAllowPubkey}, nil
}

func (f *fakeRelayAdminClient) Call(_ context.Context, targetRef, method string, params []any) (*relayadmin.Response, error) {
	f.calls = append(f.calls, relayAdminCallPayload{TargetRef: targetRef, Method: method, Params: params})
	return &relayadmin.Response{Result: json.RawMessage(`{"ok":true}`)}, nil
}

func TestRelaySettingsApplyPublishesCanonicalStateAndAudit(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	signer, err := NewPrivateKeySigner(testServiceKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{}, ProjectionStore: &memoryRelayPolicyProjectionStore{}, Logger: zap.NewNop()})
	h.publisher = publisher
	h.signer = signer
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)

	params := RelayPolicyState{
		BrowserRelays:   []string{"wss://browser.example", "wss://browser.example"},
		ContextVMRelays: []string{"wss://contextvm.example"},
		ServiceRelays:   []string{"wss://service.example"},
		DMRelayLists: []RelayPolicyDMRelayList{{
			Enabled:  true,
			Feature:  config.DMRelayListFeatureNotifications,
			Identity: config.DMRelayListIdentityService,
			Relays:   []string{"wss://dm.example"},
		}},
		RelayAdministration: RelayPolicyAdministration{Enabled: true, Targets: []RelayPolicyAdminTarget{{
			Ref:                  "sidecar",
			RelayURL:             "wss://sidecar.example",
			Authorization:        config.RelayAdministrationBahiaOwned,
			AdministratorPubkeys: []string{requesterPubkey},
		}}},
	}
	raw, _ := json.Marshal(params)
	result, err := h.ApplyPolicy(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, RPC: ContextVMJSONRPCRequest{Params: raw}})
	if err != nil {
		t.Fatalf("ApplyPolicy error: %v", err)
	}
	if result == nil {
		t.Fatalf("missing result")
	}
	if len(publisher.events) != 6 {
		t.Fatalf("published events = %d, want state + three relay sets + dm relay list + audit", len(publisher.events))
	}
	stateEvent := publisher.events[0]
	if stateEvent.Kind != kinds.CASControlState || !hasTag(stateEvent.Tags, "schema", RelaySettingsSchema) || !hasTag(stateEvent.Tags, "d", RelaySettingsDTag) {
		t.Fatalf("unexpected state event: kind=%d tags=%v", stateEvent.Kind, stateEvent.Tags)
	}
	var state RelayPolicyState
	if err := json.Unmarshal([]byte(stateEvent.Content), &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got := strings.Join(state.BrowserRelays, ","); got != "wss://browser.example" {
		t.Fatalf("browser relays = %q", got)
	}
	if publisher.events[1].Kind != kinds.RelaySetDiscovery || !hasTag(publisher.events[1].Tags, "d", "bahia-browser-v1") {
		t.Fatalf("missing browser relay set event: kind=%d tags=%v", publisher.events[1].Kind, publisher.events[1].Tags)
	}
	if publisher.events[2].Kind != kinds.RelaySetDiscovery || !hasTag(publisher.events[2].Tags, "d", "bahia-contextvm-v1") {
		t.Fatalf("missing contextvm relay set event: kind=%d tags=%v", publisher.events[2].Kind, publisher.events[2].Tags)
	}
	if publisher.events[3].Kind != kinds.RelaySetDiscovery || !hasTag(publisher.events[3].Tags, "d", "bahia-service-v1") {
		t.Fatalf("missing service relay set event: kind=%d tags=%v", publisher.events[3].Kind, publisher.events[3].Tags)
	}
	if publisher.events[4].Kind != kinds.NIP51DMRelayList || !hasTag(publisher.events[4].Tags, "relay", "wss://dm.example") {
		t.Fatalf("missing dm relay list event: kind=%d tags=%v", publisher.events[4].Kind, publisher.events[4].Tags)
	}
	if publisher.events[5].Kind != kinds.CASAudit || !hasTag(publisher.events[5].Tags, "type", "relay-settings.updated") {
		t.Fatalf("unexpected audit event: kind=%d tags=%v", publisher.events[5].Kind, publisher.events[5].Tags)
	}
}

func TestRelaySettingsApplyDoesNotMutateRuntimeConfig(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	signer, _ := NewPrivateKeySigner(testServiceKey)
	cfg := &config.Config{Nostr: config.NostrConfig{
		Relays:          []string{"wss://initial-service.example"},
		ServiceRelays:   []string{"wss://initial-service.example"},
		BrowserRelays:   []string{"wss://initial-browser.example"},
		ContextVMRelays: []string{"wss://initial-contextvm.example"},
	}}
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: cfg, ProjectionStore: &memoryRelayPolicyProjectionStore{}, Logger: zap.NewNop()})
	h.publisher = publisher
	h.signer = signer
	params := RelayPolicyState{
		BrowserRelays:   []string{"wss://updated-browser.example"},
		ContextVMRelays: []string{"wss://updated-contextvm.example"},
		ServiceRelays:   []string{"wss://updated-service.example"},
	}
	raw, _ := json.Marshal(params)
	result, err := h.ApplyPolicy(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, RPC: ContextVMJSONRPCRequest{Params: raw}})
	if err != nil {
		t.Fatalf("ApplyPolicy error: %v", err)
	}
	if result == nil || len(publisher.events) != 5 {
		t.Fatalf("ApplyPolicy result=%#v published events=%d, want accepted publish without config mutation", result, len(publisher.events))
	}
	if got := strings.Join(cfg.Nostr.BrowserRelays, ","); got != "wss://initial-browser.example" {
		t.Fatalf("BrowserRelays mutated to %q", got)
	}
	if got := strings.Join(cfg.Nostr.ContextVMRelays, ","); got != "wss://initial-contextvm.example" {
		t.Fatalf("ContextVMRelays mutated to %q", got)
	}
	if got := strings.Join(cfg.Nostr.ServiceRelays, ","); got != "wss://initial-service.example" {
		t.Fatalf("ServiceRelays mutated to %q", got)
	}
	if got := strings.Join(cfg.Nostr.Relays, ","); got != "wss://initial-service.example" {
		t.Fatalf("Relays mutated to %q", got)
	}
}

func TestRelaySettingsApplyRequiresDurableProjectionStore(t *testing.T) {
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{}, Logger: zap.NewNop()})
	_, err := h.ApplyPolicy(context.Background(), ContextVMRequest{
		Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)},
		RPC:   ContextVMJSONRPCRequest{Params: []byte(`{"browser_relays":["wss://browser.example"]}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "durable relay policy projection is unavailable") {
		t.Fatalf("error = %v, want durable projection unavailable", err)
	}
}

func TestRelaySettingsRejectsInvalidPolicyAndDoesNotPublish(t *testing.T) {
	cases := []struct {
		name   string
		policy string
	}{
		{name: "invalid relay scheme", policy: `{"browser_relays":["https://not-a-relay.example"]}`},
		{name: "empty relay topology", policy: `{"browser_relays":[],"contextvm_relays":[],"service_relays":[]}`},
		{name: "invalid trusted monitor pubkey", policy: `{"browser_relays":["wss://browser.example"],"trusted_relay_monitor_pubkeys":["not-hex"]}`},
		{name: "invalid relay admin pubkey", policy: `{"browser_relays":["wss://browser.example"],"relay_administration":{"enabled":true,"targets":[{"ref":"sidecar","relay_url":"wss://sidecar.example","authorization":"bahia-owned","administrator_pubkeys":["not-hex"]}]}}`},
		{name: "external plaintext relay admin relay url", policy: `{"browser_relays":["wss://browser.example"],"relay_administration":{"enabled":true,"targets":[{"ref":"sidecar","relay_url":"ws://relay.example.com","authorization":"bahia_owned","administrator_pubkeys":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}]}}`},
		{name: "external plaintext relay admin http url", policy: `{"browser_relays":["wss://browser.example"],"relay_administration":{"enabled":true,"targets":[{"ref":"sidecar","relay_url":"wss://relay.example.com","http_url":"http://relay.example.com","authorization":"bahia_owned","administrator_pubkeys":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			signer, _ := NewPrivateKeySigner(testServiceKey)
			h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{}, ProjectionStore: &memoryRelayPolicyProjectionStore{}, Logger: zap.NewNop()})
			h.publisher = publisher
			h.signer = signer
			_, err := h.ApplyPolicy(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, RPC: ContextVMJSONRPCRequest{Params: []byte(tc.policy)}})
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if len(publisher.events) != 0 {
				t.Fatalf("published invalid policy events: %d", len(publisher.events))
			}
		})
	}
}

func TestRelaySettingsGetPolicyReturnsUnavailableWithoutInferringConfigDefaults(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	signer, _ := NewPrivateKeySigner(testServiceKey)
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{
		Config: &config.Config{Nostr: config.NostrConfig{BrowserRelays: []string{"wss://config-default.example"}}},
		Logger: zap.NewNop(),
	})
	h.publisher = publisher
	h.signer = signer
	result, err := h.GetPolicy(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}})
	if err != nil {
		t.Fatalf("GetPolicy error: %v", err)
	}
	response := result.(map[string]any)
	if response["status"] != "unavailable" || response["state"] != nil || response["canonical_policy"] != nil {
		t.Fatalf("absence collapsed into policy state: %#v", response)
	}
	view := response["server_projection"].(RelayPolicyProjectionView)
	if view.Availability != "unavailable" || view.Freshness != "unavailable" {
		t.Fatalf("unexpected unavailable projection: %#v", view)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("read-only get published events: %d", len(publisher.events))
	}
}

func TestRelaySettingsGetPolicyReturnsDurableProjectionProvenance(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	now := time.Unix(2_000, 0).UTC()
	state := RelayPolicyState{Schema: RelaySettingsSchema, BrowserRelays: []string{"wss://durable.example"}}
	store := &memoryRelayPolicyProjectionStore{projection: testProjection(t, servicePubkey, "event-head", time.Unix(1_900, 0), state)}
	store.projection.LastSyncAt = now.Add(-time.Minute)
	store.projection.SourceRelay = "wss://secondary.example/path?ignored=value"
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{
		ProjectionStore: store,
		ServicePubkey:   servicePubkey,
		Logger:          zap.NewNop(),
		Now:             func() time.Time { return now },
	})

	result, err := h.GetPolicy(context.Background(), ContextVMRequest{})
	if err != nil {
		t.Fatalf("GetPolicy error: %v", err)
	}
	response := result.(map[string]any)
	if response["status"] != "ok" {
		t.Fatalf("status = %#v", response["status"])
	}
	policy := response["canonical_policy"].(*RelayPolicyState)
	if strings.Join(policy.BrowserRelays, ",") != "wss://durable.example" {
		t.Fatalf("unexpected canonical policy: %#v", policy)
	}
	view := response["server_projection"].(RelayPolicyProjectionView)
	if view.Availability != "available" || view.Freshness != "fresh" || view.Hash == "" || view.EventID != strings.Repeat("a", 64) {
		t.Fatalf("unexpected projection provenance: %#v", view)
	}
	if view.SourceRelay != "wss://secondary.example/path" {
		t.Fatalf("source relay was not sanitized: %q", view.SourceRelay)
	}
}

func TestRelayAdminCallRequiresConfiguredAuthorizedTarget(t *testing.T) {
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{}, AdminClient: &fakeRelayAdminClient{}, Logger: zap.NewNop()})
	raw := []byte(`{"target_ref":"public","method":"supportedmethods"}`)
	_, err := h.CallRelayAdmin(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, RPC: ContextVMJSONRPCRequest{Params: raw}})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v, want configured target rejection", err)
	}
}

func TestRelayAdminCallUsesConfiguredNIP86Client(t *testing.T) {
	publisher := &mockEncryptedPublisher{}
	signer, _ := NewPrivateKeySigner(testServiceKey)
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	admin := &fakeRelayAdminClient{}
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{Nostr: config.NostrConfig{RelayAdministration: config.RelayAdministrationConfig{Enabled: true, Targets: []config.RelayAdministrationTarget{{
		Ref:                  "sidecar",
		RelayURL:             "wss://sidecar.example",
		Authorization:        config.RelayAdministrationBahiaAuthorized,
		AdministratorPubkeys: []string{requesterPubkey},
	}}}}}, AdminClient: admin, Logger: zap.NewNop()})
	h.publisher = publisher
	h.signer = signer
	raw := []byte(`{"target_ref":"sidecar","method":"allowpubkey","params":["` + requesterPubkey + `","operator requested"]}`)
	_, err := h.CallRelayAdmin(context.Background(), ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}, RPC: ContextVMJSONRPCRequest{Params: raw}})
	if err != nil {
		t.Fatalf("CallRelayAdmin error: %v", err)
	}
	if len(admin.calls) != 1 || admin.calls[0].Method != relayadmin.MethodAllowPubkey {
		t.Fatalf("admin calls = %#v", admin.calls)
	}
	if len(publisher.events) != 1 || publisher.events[0].Kind != kinds.CASAudit {
		t.Fatalf("admin audit events = %#v", publisher.events)
	}
}
