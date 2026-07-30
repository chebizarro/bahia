package relaysidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

func sidecarTestConfig(t *testing.T) config.NostrConfig {
	t.Helper()
	cfg := config.Defaults().Nostr
	cfg.Sidecar.DataDir = t.TempDir()
	return cfg
}

func TestSidecarAcceptsAndQueriesSignedInteropEvent(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.MaxQueryLimit = 100
	cfg.Sidecar.DataDir = t.TempDir()

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	sk := nostr.Generate()
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      10100,
		Tags:      nostr.Tags{nostr.Tag{"agent", "smoke"}},
		Content:   `{"status":"ready"}`,
	}
	if err := event.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	skipBroadcast, err := server.Relay().AddEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("AddEvent() error: %v", err)
	}
	if skipBroadcast {
		t.Fatalf("AddEvent() skipBroadcast = true")
	}

	var found bool
	for stored := range server.Relay().QueryStored(context.Background(), nostr.Filter{Kinds: []nostr.Kind{10100}}) {
		if stored.ID == event.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stored event %s was not returned by QueryStored", event.ID.Hex())
	}
}

func TestSidecarDecouplesPublisherAcknowledgementFromBroadcast(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.DataDir = t.TempDir()

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer server.store.Close()

	relay := server.Relay()
	if relay.PreventBroadcast == nil || !relay.PreventBroadcast(nil, nostr.Filter{}, nostr.Event{}) {
		t.Fatal("expected synchronous Khatru broadcast to be disabled")
	}
	if relay.OnEventSaved == nil {
		t.Fatal("expected stored events to be broadcast asynchronously")
	}
	if relay.OnEphemeralEvent == nil {
		t.Fatal("expected ephemeral events to be broadcast asynchronously")
	}
}

func TestSidecarRetainsEventsAcrossRestart(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.DataDir = t.TempDir()

	first, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("first New() error: %v", err)
	}
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      nostr.Kind(kinds.SoulFactoryDraft),
		Tags:      nostr.Tags{{"d", "restart-proof-draft"}},
		Content:   `{"agent_id":"restart-proof"}`,
	}
	if err := event.Sign(nostr.Generate()); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	if _, err := first.Relay().AddEvent(context.Background(), event); err != nil {
		t.Fatalf("store event: %v", err)
	}
	if err := first.store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("second New() error: %v", err)
	}
	defer second.store.Close()
	for stored := range second.Relay().QueryStored(context.Background(), nostr.Filter{IDs: []nostr.ID{event.ID}}) {
		if stored.ID == event.ID {
			return
		}
	}
	t.Fatalf("event %s disappeared across relay restart", event.ID.Hex())
}

func TestSidecarAcceptsReadFiltersWithoutKinds(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	if reject, msg := server.Relay().OnRequest(context.Background(), nostr.Filter{}); reject {
		t.Fatalf("expected read filter without kinds to be accepted, got rejection %q", msg)
	}
}

func TestSidecarAcceptsEveryEventKindForReads(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	filter := nostr.Filter{Kinds: []nostr.Kind{0, 1, 5963, 31950, 65535}}
	if reject, msg := server.Relay().OnRequest(context.Background(), filter); reject {
		t.Fatalf("expected arbitrary event kinds to be readable, got rejection %q", msg)
	}
}

func TestSidecarRetainsProtocolValidityChecks(t *testing.T) {
	pol := &policy{now: func() nostr.Timestamp { return 1_000_000 }}
	sk := nostr.Generate()

	valid := nostr.Event{CreatedAt: 1_000_000, Kind: 65535, Content: "valid"}
	if err := valid.Sign(sk); err != nil {
		t.Fatalf("sign valid event: %v", err)
	}
	if reject, msg := pol.acceptEvent(context.Background(), valid); reject {
		t.Fatalf("expected valid event to be accepted, got rejection %q", msg)
	}

	badID := valid
	badID.Content = "modified after signing"
	if reject, _ := pol.acceptEvent(context.Background(), badID); !reject {
		t.Fatalf("expected incorrect event ID to be rejected")
	}

	future := nostr.Event{CreatedAt: 1_000_601, Kind: 65535, Content: "future"}
	if err := future.Sign(sk); err != nil {
		t.Fatalf("sign future event: %v", err)
	}
	if reject, _ := pol.acceptEvent(context.Background(), future); !reject {
		t.Fatalf("expected event more than ten minutes in the future to be rejected")
	}

	past := nostr.Event{CreatedAt: 1_000_000 - nostr.Timestamp((365 * 24 * time.Hour).Seconds()) - 1, Kind: 65535, Content: "past"}
	if err := past.Sign(sk); err != nil {
		t.Fatalf("sign past event: %v", err)
	}
	if reject, _ := pol.acceptEvent(context.Background(), past); !reject {
		t.Fatalf("expected event older than one year to be rejected")
	}
}

func TestSidecarRejectsUnsupportedSearchWithoutRestrictingKinds(t *testing.T) {
	pol := &policy{now: nostr.Now}
	filter := nostr.Filter{Kinds: []nostr.Kind{65535}, Search: "not implemented"}
	if reject, msg := pol.acceptFilter(context.Background(), filter); !reject || msg == "" {
		t.Fatalf("expected unsupported search to be rejected with a reason")
	}
}

func TestSidecarDoesNotApplyAuthorRestrictionsToReads(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk)
	cfg.AuthorizedPubkeys = []string{pubkey.Hex()}

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	filter := nostr.Filter{Kinds: []nostr.Kind{5963, 5978, 5979, 5997, 6005, 38390, 38400, 38420, 38421, 38430}, Authors: []nostr.PubKey{pubkey}}
	reject, msg := server.Relay().OnRequest(context.Background(), filter)
	if reject {
		t.Fatalf("expected author-scoped read to be accepted, got rejection %q", msg)
	}

	encryptedFilter := nostr.Filter{Kinds: []nostr.Kind{5980}, Authors: []nostr.PubKey{pubkey}}
	reject, msg = server.Relay().OnRequest(context.Background(), encryptedFilter)
	if reject {
		t.Fatalf("expected encrypted event kind read to be accepted, got rejection %q", msg)
	}
}

func TestSidecarAllowsCanonicalAndLegacyStatusResultKinds(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	canonicalFilter := nostr.Filter{Kinds: []nostr.Kind{
		nostr.Kind(kinds.NIP38Status), nostr.Kind(kinds.AssistantTranscript), nostr.Kind(kinds.CASControlState),
		nostr.Kind(kinds.HeartbeatObservation),
		nostr.Kind(kinds.ContinuityStatus),
		nostr.Kind(kinds.DegradedModeActivation),
		nostr.Kind(kinds.RecoveryProgress),
		nostr.Kind(kinds.ContinuityProfile),
		nostr.Kind(kinds.FailoverPolicy),
		nostr.Kind(kinds.StandbyNodeDefinition),
		nostr.Kind(kinds.ReplicationPolicy),
		nostr.Kind(kinds.RecoveryWorkflow),
	}}
	reject, msg := server.Relay().OnRequest(context.Background(), canonicalFilter)
	if reject {
		t.Fatalf("expected canonical assistant/status/state/audit/continuity kinds to be readable, got rejection %q", msg)
	}

	legacyFilter := nostr.Filter{Kinds: []nostr.Kind{6963, 6978, 6981, 6984, 6997, 7962, 7978, 7979, 7997, 31310, 31311, 38395, 38410, 38422, 38423, 32000, 32003}}
	reject, msg = server.Relay().OnRequest(context.Background(), legacyFilter)
	if reject {
		t.Fatalf("expected every legacy status/result/read-model kind to be readable, got rejection %q", msg)
	}
}

func TestSidecarDoesNotRequireScopedContextVMSubscriptions(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	serviceSK := nostr.Generate()
	servicePubkey := nostr.GetPublicKey(serviceSK)
	operatorSK := nostr.Generate()
	operatorPubkey := nostr.GetPublicKey(operatorSK)
	unknownPubkey := nostr.GetPublicKey(nostr.Generate())
	cfg.PrivateKey = serviceSK.Hex()
	cfg.AuthorizedPubkeys = []string{operatorPubkey.Hex()}

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	allowed := []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}, Authors: []nostr.PubKey{servicePubkey}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}, Authors: []nostr.PubKey{operatorPubkey}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}, Tags: nostr.TagMap{"p": []string{operatorPubkey.Hex()}}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMGiftWrap)}, Tags: nostr.TagMap{"p": []string{servicePubkey.Hex()}}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMEphemeralGiftWrap)}, Tags: nostr.TagMap{"p": []string{operatorPubkey.Hex()}}},
	}
	for _, filter := range allowed {
		reject, msg := server.Relay().OnRequest(context.Background(), filter)
		if reject {
			t.Fatalf("expected scoped ContextVM filter %#v to be readable, got rejection %q", filter, msg)
		}
	}

	blocked := []nostr.Filter{
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMGiftWrap)}},
		{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMEphemeralGiftWrap)}, Tags: nostr.TagMap{"p": []string{unknownPubkey.Hex()}}},
	}
	for _, filter := range blocked {
		reject, msg := server.Relay().OnRequest(context.Background(), filter)
		if reject {
			t.Fatalf("expected unscoped ContextVM filter %#v to be readable, got rejection %q", filter, msg)
		}
	}
}

func TestSidecarAcceptsContextVMEventsRegardlessOfAuthorOrRecipient(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	serviceSK := nostr.Generate()
	servicePubkey := nostr.GetPublicKey(serviceSK)
	operatorSK := nostr.Generate()
	operatorPubkey := nostr.GetPublicKey(operatorSK)
	unknownPubkey := nostr.GetPublicKey(nostr.Generate())
	cfg.PrivateKey = serviceSK.Hex()
	cfg.AuthorizedPubkeys = []string{operatorPubkey.Hex()}

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	directRequest := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.Kind(kinds.ContextVMMessage), Tags: nostr.Tags{{"p", servicePubkey.Hex()}}, Content: `{}`}
	if err := directRequest.Sign(operatorSK); err != nil {
		t.Fatalf("sign direct request: %v", err)
	}
	if _, err := server.Relay().AddEvent(context.Background(), directRequest); err != nil {
		t.Fatalf("expected authorized direct ContextVM request to be accepted: %v", err)
	}

	directResponse := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.Kind(kinds.ContextVMMessage), Tags: nostr.Tags{{"p", operatorPubkey.Hex()}}, Content: `{}`}
	if err := directResponse.Sign(serviceSK); err != nil {
		t.Fatalf("sign direct response: %v", err)
	}
	if _, err := server.Relay().AddEvent(context.Background(), directResponse); err != nil {
		t.Fatalf("expected service-signed direct ContextVM response to be accepted: %v", err)
	}

	wrapToService := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.Kind(kinds.ContextVMGiftWrap), Tags: nostr.Tags{{"p", servicePubkey.Hex()}}, Content: `encrypted`}
	if err := wrapToService.Sign(nostr.Generate()); err != nil {
		t.Fatalf("sign gift wrap to service: %v", err)
	}
	if _, err := server.Relay().AddEvent(context.Background(), wrapToService); err != nil {
		t.Fatalf("expected gift wrap addressed to service to be accepted despite random wrapper pubkey: %v", err)
	}

	wrapToOperator := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.Kind(kinds.ContextVMEphemeralGiftWrap), Tags: nostr.Tags{{"p", operatorPubkey.Hex()}}, Content: `encrypted`}
	if err := wrapToOperator.Sign(nostr.Generate()); err != nil {
		t.Fatalf("sign gift wrap to operator: %v", err)
	}
	if _, err := server.Relay().AddEvent(context.Background(), wrapToOperator); err != nil {
		t.Fatalf("expected gift wrap addressed to authorized operator to be accepted despite random wrapper pubkey: %v", err)
	}

	wrapToUnknown := nostr.Event{CreatedAt: nostr.Now(), Kind: nostr.Kind(kinds.ContextVMGiftWrap), Tags: nostr.Tags{{"p", unknownPubkey.Hex()}}, Content: `encrypted`}
	if err := wrapToUnknown.Sign(nostr.Generate()); err != nil {
		t.Fatalf("sign gift wrap to unknown: %v", err)
	}
	if _, err := server.Relay().AddEvent(context.Background(), wrapToUnknown); err != nil {
		t.Fatalf("expected gift wrap addressed to unknown pubkey to be accepted: %v", err)
	}
}

func TestSidecarAllowsDiscoveryKinds(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	filter := nostr.Filter{Kinds: []nostr.Kind{10002, 30002, 30004, 30078, 11316, 11317, 11318, 11319, 11320, 31410, 31411, 30360}}
	reject, msg := server.Relay().OnRequest(context.Background(), filter)
	if reject {
		t.Fatalf("expected canonical discovery/SBOM kinds to be readable, got rejection %q", msg)
	}

	legacyFilter := nostr.Filter{Kinds: []nostr.Kind{30079, 31400, 31404, 31974, 31975, 31976, 31977, 31978, 31991, 31999}}
	reject, msg = server.Relay().OnRequest(context.Background(), legacyFilter)
	if reject {
		t.Fatalf("expected legacy discovery/read-model kinds to be readable, got rejection %q", msg)
	}
}

func TestSidecarCountIsNotCappedByQueryLimit(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.MaxQueryLimit = 1

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	for i := 0; i < 2; i++ {
		sk := nostr.Generate()
		event := nostr.Event{CreatedAt: nostr.Now(), Kind: 10100, Content: `{}`}
		if err := event.Sign(sk); err != nil {
			t.Fatalf("sign event: %v", err)
		}
		if _, err := server.Relay().AddEvent(context.Background(), event); err != nil {
			t.Fatalf("AddEvent() error: %v", err)
		}
	}

	count, err := server.Relay().Count(context.Background(), nostr.Filter{Kinds: []nostr.Kind{10100}})
	if err != nil {
		t.Fatalf("Count() error: %v", err)
	}
	if count != 2 {
		t.Fatalf("Count() = %d, want 2", count)
	}
}

func TestSidecarServesWebsocketAtRootAndConfiguredPath(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	for _, path := range []string{"/", "/relay"} {
		req, err := http.NewRequest(http.MethodGet, httpServer.URL+path, nil)
		if err != nil {
			t.Fatalf("new request %q: %v", path, err)
		}
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %q: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("status at %q = %d, expected non-404 websocket handling", path, resp.StatusCode)
		}
	}
}

func TestSidecarServesNIP11OnConfiguredPath(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334/relay"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/relay", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/nostr+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /relay: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestSidecarAcceptsArbitrarySignedRequestKind(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	sk := nostr.Generate()
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      5961,
		Tags:      nostr.Tags{nostr.Tag{"t", "task-1"}},
		Content:   `{}`,
	}
	if err := event.Sign(sk); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	if _, err := server.Relay().AddEvent(context.Background(), event); err != nil {
		t.Fatalf("expected arbitrary signed request kind to be accepted: %v", err)
	}
}

func TestSidecarAllowsNIP34Kinds(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	publisherSK := nostr.Generate()
	repositoryEvent := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      nostr.Kind(kinds.NIP34RepositoryAnnouncement),
		Tags: nostr.Tags{
			nostr.Tag{"d", "bahia"},
			nostr.Tag{"name", "Bahia"},
			nostr.Tag{"relays", "wss://relay.example"},
		},
		Content: "",
	}
	if err := repositoryEvent.Sign(publisherSK); err != nil {
		t.Fatalf("sign NIP-34 repository event: %v", err)
	}
	if skipBroadcast, err := server.Relay().AddEvent(context.Background(), repositoryEvent); err != nil || skipBroadcast {
		t.Fatalf("expected sidecar to accept NIP-34 repository event, skipBroadcast=%v err=%v", skipBroadcast, err)
	}

	for _, tc := range []struct {
		name string
		kind int
	}{
		{name: "NIP-22 comment", kind: kinds.NIP22Comment},
		{name: "user grasp list", kind: kinds.NIP34UserGraspList},
		{name: "patch", kind: kinds.NIP34Patch},
		{name: "pull request", kind: kinds.NIP34PullRequest},
		{name: "pull request update", kind: kinds.NIP34PullRequestUpdate},
		{name: "issue", kind: kinds.NIP34Issue},
		{name: "status open", kind: kinds.NIP34StatusOpen},
		{name: "status applied or merged", kind: kinds.NIP34StatusAppliedOrMerged},
		{name: "status closed", kind: kinds.NIP34StatusClosed},
		{name: "status draft", kind: kinds.NIP34StatusDraft},
		{name: "repository announcement", kind: kinds.NIP34RepositoryAnnouncement},
		{name: "repository state", kind: kinds.NIP34RepositoryState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readFilter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(tc.kind)}}
			reject, msg := server.Relay().OnRequest(context.Background(), readFilter)
			if reject {
				t.Fatalf("expected NIP-34 kind %d to be readable, got rejection %q", tc.kind, msg)
			}
		})
	}
}

func TestSidecarAllowsSoulFactoryInteropKinds(t *testing.T) {
	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.MaxQueryLimit = 100

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	publisherSK := nostr.Generate()
	for _, tc := range []struct {
		name string
		kind int
	}{
		{name: "template", kind: kinds.SoulFactoryTemplate},
		{name: "agent soul", kind: kinds.SoulFactoryAgentSoul},
		{name: "draft", kind: kinds.SoulFactoryDraft},
		{name: "provisioning request", kind: kinds.SoulFactoryProvisioningRequest},
		{name: "provisioning status", kind: kinds.SoulFactoryProvisioningStatus},
		{name: "provisioning result", kind: kinds.SoulFactoryProvisioningResult},
		{name: "action", kind: kinds.SoulFactoryAction},
		{name: "legacy action result", kind: kinds.SoulFactoryActionLegacyResult},
		{name: "runtime capability", kind: kinds.SoulFactoryRuntimeCapability},
		{name: "runtime control", kind: kinds.SoulFactoryRuntimeControl},
		{name: "runtime result", kind: kinds.SoulFactoryRuntimeResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readFilter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(tc.kind)}}
			reject, msg := server.Relay().OnRequest(context.Background(), readFilter)
			if reject {
				t.Fatalf("expected SoulFactory kind %d to be readable, got rejection %q", tc.kind, msg)
			}

			event := nostr.Event{
				CreatedAt: nostr.Now(),
				Kind:      nostr.Kind(tc.kind),
				Tags: nostr.Tags{
					nostr.Tag{"d", tc.name},
					nostr.Tag{"agent-id", "scout"},
				},
				Content: `{"schema":"soulfactory-test/v1"}`,
			}
			if err := event.Sign(publisherSK); err != nil {
				t.Fatalf("sign SoulFactory event: %v", err)
			}
			if skipBroadcast, err := server.Relay().AddEvent(context.Background(), event); err != nil || skipBroadcast {
				t.Fatalf("expected sidecar to accept SoulFactory kind %d, skipBroadcast=%v err=%v", tc.kind, skipBroadcast, err)
			}
		})
	}
}

func TestSidecarAllowsNIP23LongFormFromServicePubkey(t *testing.T) {
	serviceSK := nostr.Generate()
	servicePubkey := nostr.GetPublicKey(serviceSK)
	unauthorizedSK := nostr.Generate()

	cfg := sidecarTestConfig(t)
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.PrivateKey = serviceSK.Hex()

	server, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	for _, tc := range []struct {
		name string
		kind int
		dtag string
	}{
		{name: "long-form content", kind: kinds.LongFormContent, dtag: "getting-started"},
		{name: "long-form draft", kind: kinds.LongFormDraft, dtag: "getting-started-draft"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Service pubkey can write NIP-23 long-form content and draft events.
			docEvent := nostr.Event{
				CreatedAt: nostr.Now(),
				Kind:      nostr.Kind(tc.kind),
				Tags: nostr.Tags{
					nostr.Tag{"d", tc.dtag},
					nostr.Tag{"title", "Getting Started"},
					nostr.Tag{"t", "bahia-docs"},
					nostr.Tag{"t", "guide"},
				},
				Content: "# Getting Started\n\nWelcome to Bahia.",
			}
			if err := docEvent.Sign(serviceSK); err != nil {
				t.Fatalf("sign doc event: %v", err)
			}
			skipBroadcast, err := server.Relay().AddEvent(context.Background(), docEvent)
			if err != nil {
				t.Fatalf("AddEvent() error for kind %d: %v", tc.kind, err)
			}
			if skipBroadcast {
				t.Fatalf("AddEvent() rejected kind %d from service pubkey", tc.kind)
			}

			// Relay admission is independent of author identity.
			badDocEvent := nostr.Event{
				CreatedAt: nostr.Now(),
				Kind:      nostr.Kind(tc.kind),
				Tags:      nostr.Tags{nostr.Tag{"d", "rogue-doc"}, nostr.Tag{"t", "bahia-docs"}},
				Content:   "# Rogue",
			}
			if err := badDocEvent.Sign(unauthorizedSK); err != nil {
				t.Fatalf("sign bad doc event: %v", err)
			}
			if _, err := server.Relay().AddEvent(context.Background(), badDocEvent); err != nil {
				t.Fatalf("expected kind %d from arbitrary pubkey to be accepted: %v", tc.kind, err)
			}

			// NIP-23 long-form content and draft kinds are readable.
			readFilter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(tc.kind)}}
			reject, msg := server.Relay().OnRequest(context.Background(), readFilter)
			if reject {
				t.Fatalf("expected kind %d to be readable, got rejection %q", tc.kind, msg)
			}

			// Verify the stored event is queryable.
			var found bool
			for stored := range server.Relay().QueryStored(context.Background(), nostr.Filter{
				Kinds:   []nostr.Kind{nostr.Kind(tc.kind)},
				Authors: []nostr.PubKey{servicePubkey},
			}) {
				if stored.ID == docEvent.ID {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("kind %d event not found in sidecar store", tc.kind)
			}
		})
	}
}
