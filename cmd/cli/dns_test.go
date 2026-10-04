package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
	"go.uber.org/zap"
)

type cliDNSOperator struct{}

func (cliDNSOperator) ReconcileAll(context.Context) error               { return nil }
func (cliDNSOperator) ReconcileZone(context.Context, string) error      { return nil }
func (cliDNSOperator) RetireZone(context.Context, domain.DNSZone) error { return nil }
func (cliDNSOperator) HasZone(string) bool                              { return true }
func (cliDNSOperator) HasBackend(ref string) bool                       { return ref == "primary" }

type cliDNSCanonical struct{ zones []domain.DNSZone }

func (p *cliDNSCanonical) PublishZone(_ context.Context, zone domain.DNSZone) error {
	p.zones = append(p.zones, zone)
	return nil
}
func (*cliDNSCanonical) PublishZoneTombstone(context.Context, string) error        { return nil }
func (*cliDNSCanonical) PublishPolicy(context.Context, domain.DNSPolicy) error     { return nil }
func (*cliDNSCanonical) PublishPolicyTombstone(context.Context, uuid.UUID) error   { return nil }
func (*cliDNSCanonical) PublishEndpoint(context.Context, domain.DNSEndpoint) error { return nil }
func (*cliDNSCanonical) PublishEndpointTombstone(context.Context, domain.DNSEndpoint) error {
	return nil
}
func (*cliDNSCanonical) PublishBackend(context.Context, domain.DNSBackendState) error { return nil }
func (*cliDNSCanonical) PublishBackendTombstone(context.Context, string) error        { return nil }

func setupDNSIntentPipeline(t *testing.T) (*cliIntentTransport, *cliDNSCanonical, string) {
	t.Helper()
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	operator, daemon := nostr.Generate(), nostr.Generate()
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", operator.Hex())
	t.Setenv("BAHIA_NOSTR_SERVICE_PUBKEY", daemon.Public().Hex())
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "dns.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	events, err := localstore.Open(filepath.Join(t.TempDir(), "intents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	canonical := &cliDNSCanonical{}
	mutations := &service.DNSMutationService{Zones: repository.NewLocalDNSZoneRepository(outbox), Policies: repository.NewLocalDNSPolicyRepository(outbox), Endpoints: repository.NewLocalDNSEndpointRepository(outbox), Backends: repository.NewLocalDNSBackendRepository(outbox), Canonical: canonical, Reconciler: cliDNSOperator{}}
	if err := mutations.Backends.Upsert(context.Background(), &domain.DNSBackendState{Ref: "primary", Type: domain.DNSBackendTypeCoreDNS}); err != nil {
		t.Fatal(err)
	}
	transport := &cliIntentTransport{events: make(chan *nostr.Event, 8)}
	signer := keyer.NewPlainKeySigner(daemon)
	status := controlplane.NewIntentStatusPublisher(func(_ context.Context, event nostr.Event) error { transport.events <- &event; return nil }, signer, zap.NewNop())
	org := uuid.NewString()
	trust := controlplane.NewTrustSet([]string{operator.Public().Hex()}, zap.NewNop())
	processor := controlplane.NewIntentProcessor(trust, events, status, controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"dns": true}}, zap.NewNop())
	processor.RegisterHandler("dns", controlplane.NewDNSIntentHandler(cliDNSOperator{}, canonical, mutations))
	transport.process = func(event nostr.Event) {
		intent, err := controlplane.ParseIntent(&event)
		if err != nil {
			t.Errorf("parse CLI DNS intent: %v", err)
			return
		}
		intent.Actor = event.PubKey.Hex()
		_ = processor.ProcessInProcess(context.Background(), intent)
	}
	previous := newCLIIntentPublisher
	newCLIIntentPublisher = func(cfg client.IntentPublisherConfig) (*client.IntentPublisher, error) {
		cfg.Transport = transport
		return client.NewIntentPublisher(cfg)
	}
	t.Cleanup(func() { newCLIIntentPublisher = previous })
	return transport, canonical, org
}

func TestDNSCLIIntentAcceptedRejectedPendingAndFixture(t *testing.T) {
	transport, canonical, org := setupDNSIntentPipeline(t)
	args := []string{"--org", org, "dns", "zone-create", "--name", "example.test", "--visibility", "internal", "--backend-ref", "primary", "--ttl", "60", "--authoritative"}
	if err := executeIntentCommand(t, args...); err != nil {
		t.Fatal(err)
	}
	if len(canonical.zones) != 1 || canonical.zones[0].Name != "example.test" {
		t.Fatalf("canonical zones = %#v", canonical.zones)
	}
	var fixture struct {
		Intents []struct {
			Domain, Op, Coordinate string
			Content                map[string]interface{}
		} `json:"intents"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "web", "tests", "fixtures", "d70-intent-content.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	intent, err := controlplane.ParseIntent(&transport.published[0])
	if err != nil {
		t.Fatal(err)
	}
	want := fixture.Intents[0]
	if intent.Domain != want.Domain || intent.Op != want.Op || intent.Coordinate != want.Coordinate {
		t.Fatalf("intent envelope = %#v, want %#v", intent, want)
	}
	gotJSON, _ := json.Marshal(intent.Content)
	wantJSON, _ := json.Marshal(want.Content)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("content = %s, want %s", gotJSON, wantJSON)
	}

	rejected := []string{"--org", org, "dns", "zone-create", "--name", "other.test", "--visibility", "internal", "--backend-ref", "missing", "--ttl", "60"}
	var exit *IntentExitError
	if err := executeIntentCommand(t, rejected...); !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("rejection = %v", err)
	}
	transport.process = nil
	pending := []string{"--org", org, "dns", "zone-create", "--name", "pending.test", "--visibility", "internal", "--backend-ref", "primary", "--ttl", "60"}
	if err := executeIntentCommandWithTimeout(t, "15ms", pending...); !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("pending = %v", err)
	}
}

func TestDNSD72CommandsAndRevisionValidation(t *testing.T) {
	for _, name := range []string{"zone-update", "zone-delete", "endpoint-create", "endpoint-update", "endpoint-delete", "backend-create", "backend-update", "backend-delete", "policy-update", "policy-delete"} {
		found := false
		for _, cmd := range dnsCommands().Commands() {
			if cmd.Name() == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing DNS %s command", name)
		}
	}
	for _, content := range []map[string]interface{}{{}, {"expected_updated_at": 3}, {"expected_updated_at": "bad"}} {
		if _, err := dnsRevision(content); err == nil {
			t.Errorf("accepted invalid revision %#v", content)
		}
	}
}

func TestDNSPolicyApplyRejectsInvalidJSONAndPolicy(t *testing.T) {
	for _, tc := range []struct{ content, want string }{{`{"name":`, "read DNS policy"}, {`{"name":"no-rules","rules":[]}`, "rules must not be empty"}, {`{"name":"policy","rules":[],"secret":"not-allowed"}`, "unknown field"}} {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readDNSPolicyFile(path)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("error = %v, want %q", err, tc.want)
		}
	}
}
