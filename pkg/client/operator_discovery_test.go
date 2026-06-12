package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestDiscoverOperatorRelaysPrefersContextVMRelaySet(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	now := nostr.Now()
	transport.events <- signedRelaySetEvent(t, serviceKey, operatorBrowserRelaySet, []string{"wss://browser.example"}, now)
	transport.events <- signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://contextvm.example", "wss://contextvm.example/"}, now)
	close(transport.eose)

	relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{trustedPubkey}, transport)
	if err != nil {
		t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
	}
	if strings.Join(relays, ",") != "wss://contextvm.example" {
		t.Fatalf("relays = %#v, want normalized ContextVM relay set", relays)
	}
	filter := transport.onlyFilter(t)
	if got := filter.Kinds; len(got) != 1 || got[0] != nostr.Kind(kinds.RelaySetDiscovery) {
		t.Fatalf("filter kinds = %#v, want NIP-51 relay set kind", got)
	}
	if got := filter.Authors; len(got) != 1 || got[0].Hex() != trustedPubkey {
		t.Fatalf("filter authors = %#v, want trusted service pubkey", got)
	}
	if got := filter.Tags["d"]; strings.Join(got, ",") != operatorContextVMRelaySet+","+operatorBrowserRelaySet {
		t.Fatalf("filter #d = %#v, want scoped relay-set names", got)
	}
}

func TestDiscoverOperatorRelaysFallsBackToBrowserRelaySet(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	transport.events <- signedRelaySetEvent(t, serviceKey, operatorBrowserRelaySet, []string{"wss://browser.example"}, nostr.Now())
	close(transport.eose)

	relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{trustedPubkey}, transport)
	if err != nil {
		t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
	}
	if strings.Join(relays, ",") != "wss://browser.example" {
		t.Fatalf("relays = %#v, want browser fallback relay set", relays)
	}
}

func TestDiscoverOperatorRelaysUsesLatestParameterizedReplaceableRelaySet(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	now := nostr.Now()
	transport.events <- signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://old.example"}, nostr.Timestamp(int64(now)-1))
	transport.events <- signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://new.example"}, now)
	close(transport.eose)

	relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{trustedPubkey}, transport)
	if err != nil {
		t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
	}
	if strings.Join(relays, ",") != "wss://new.example" {
		t.Fatalf("relays = %#v, want latest replaceable relay set", relays)
	}
}

func TestDiscoverOperatorRelaysUsesLowestEventIDForReplaceableTimestampTie(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	now := nostr.Now()
	first := signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://first.example"}, now)
	second := signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://second.example"}, now)
	transport.events <- first
	transport.events <- second
	close(transport.eose)

	expected := "wss://first.example"
	if second.ID.Hex() < first.ID.Hex() {
		expected = "wss://second.example"
	}
	relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{trustedPubkey}, transport)
	if err != nil {
		t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
	}
	if strings.Join(relays, ",") != expected {
		t.Fatalf("relays = %#v, want lowest event ID relay set %q for equal created_at", relays, expected)
	}
}

func TestDiscoverOperatorRelaysMultipleTrustedServicesUsePurposeThenTrustOrder(t *testing.T) {
	t.Run("ContextVM from later trusted service beats browser from earlier trusted service", func(t *testing.T) {
		firstServiceKey := nostr.Generate().Hex()
		secondServiceKey := nostr.Generate().Hex()
		firstTrustedPubkey := mustPublicKey(t, firstServiceKey)
		secondTrustedPubkey := mustPublicKey(t, secondServiceKey)
		transport := newFakeOperatorTransport()
		now := nostr.Now()
		transport.events <- signedRelaySetEvent(t, firstServiceKey, operatorBrowserRelaySet, []string{"wss://first-browser.example"}, now)
		transport.events <- signedRelaySetEvent(t, secondServiceKey, operatorContextVMRelaySet, []string{"wss://second-contextvm.example"}, now)
		close(transport.eose)

		relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{firstTrustedPubkey, secondTrustedPubkey}, transport)
		if err != nil {
			t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://second-contextvm.example" {
			t.Fatalf("relays = %#v, want ContextVM set before browser fallback across trusted services", relays)
		}
	})

	t.Run("configured trust order beats cross-service latest timestamp for same relay-set purpose", func(t *testing.T) {
		firstServiceKey := nostr.Generate().Hex()
		secondServiceKey := nostr.Generate().Hex()
		firstTrustedPubkey := mustPublicKey(t, firstServiceKey)
		secondTrustedPubkey := mustPublicKey(t, secondServiceKey)
		transport := newFakeOperatorTransport()
		now := nostr.Now()
		transport.events <- signedRelaySetEvent(t, firstServiceKey, operatorContextVMRelaySet, []string{"wss://first-contextvm.example"}, now)
		transport.events <- signedRelaySetEvent(t, secondServiceKey, operatorContextVMRelaySet, []string{"wss://second-newer-contextvm.example"}, nostr.Timestamp(int64(now)+10))
		close(transport.eose)

		relays, err := discoverOperatorRelaysWithTransport(context.Background(), []string{firstTrustedPubkey, secondTrustedPubkey}, transport)
		if err != nil {
			t.Fatalf("discoverOperatorRelaysWithTransport() error = %v", err)
		}
		if strings.Join(relays, ",") != "wss://first-contextvm.example" {
			t.Fatalf("relays = %#v, want first configured trusted service for same relay-set purpose", relays)
		}
	})
}

func TestDiscoverOperatorRelaysRejectsMissingTrustAndUntrustedEvents(t *testing.T) {
	if _, err := discoverOperatorRelaysWithTransport(context.Background(), nil, newFakeOperatorTransport()); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
		t.Fatalf("missing trust error = %v, want deterministic trusted service pubkey failure", err)
	}

	trustedKey := nostr.Generate().Hex()
	untrustedKey := nostr.Generate().Hex()
	transport := newFakeOperatorTransport()
	transport.events <- signedRelaySetEvent(t, untrustedKey, operatorContextVMRelaySet, []string{"wss://untrusted.example"}, nostr.Now())
	close(transport.eose)

	_, err := discoverOperatorRelaysWithTransport(context.Background(), []string{mustPublicKey(t, trustedKey)}, transport)
	if err == nil || !strings.Contains(err.Error(), "no trusted operator relay set events") {
		t.Fatalf("untrusted discovery error = %v, want no trusted relay set failure", err)
	}
}

func TestDiscoverOperatorRelaysRejectsUnsignedOrWrongDTagEvents(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	badSignature := signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://tampered.example"}, nostr.Now())
	badSignature.Content = "tampered"
	transport.events <- badSignature
	transport.events <- signedRelaySetEvent(t, serviceKey, "bahia-service-v1", []string{"wss://service.example"}, nostr.Now())
	close(transport.eose)

	_, err := discoverOperatorRelaysWithTransport(context.Background(), []string{trustedPubkey}, transport)
	if err == nil || !strings.Contains(err.Error(), "no trusted operator relay set events") {
		t.Fatalf("invalid discovery error = %v, want deterministic no trusted event failure", err)
	}
}

func TestDiscoverOperatorRelaysRequiresBootstrapRelaysForRealResolver(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	_, err := DiscoverOperatorRelays(context.Background(), OperatorRelayDiscoveryConfig{TrustedServicePubkeys: []string{mustPublicKey(t, serviceKey)}})
	if err == nil || !strings.Contains(err.Error(), "bootstrap relay") {
		t.Fatalf("DiscoverOperatorRelays() error = %v, want bootstrap relay requirement", err)
	}
}

func TestDiscoverOperatorRelaysReportsDeadlineBeforeEOSE(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := discoverOperatorRelaysWithTransport(ctx, []string{trustedPubkey}, transport)
	if err == nil || !strings.Contains(err.Error(), "transport guard expired before EOSE") {
		t.Fatalf("deadline error = %v, want explicit transport guard expiry before EOSE", err)
	}
}

func TestDiscoverOperatorRelaysTransportGuardDoesNotCompleteDiscovery(t *testing.T) {
	serviceKey := nostr.Generate().Hex()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	transport.events = make(chan *nostr.Event)
	transport.eose = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type discoveryResult struct {
		relays []string
		err    error
	}
	resultCh := make(chan discoveryResult, 1)
	go func() {
		relays, err := discoverOperatorRelaysWithTransport(ctx, []string{trustedPubkey}, transport)
		resultCh <- discoveryResult{relays: relays, err: err}
	}()

	transport.events <- signedRelaySetEvent(t, serviceKey, operatorContextVMRelaySet, []string{"wss://candidate-before-eose.example"}, nostr.Now())
	cancel()

	result := <-resultCh
	if result.err == nil || !strings.Contains(result.err.Error(), "transport guard canceled before EOSE") {
		t.Fatalf("guard result error = %v, want fail-closed cancellation before EOSE", result.err)
	}
	if result.relays != nil {
		t.Fatalf("relays = %#v, want no relays without EOSE", result.relays)
	}
}

func signedRelaySetEvent(t *testing.T, privateKey, dTag string, relays []string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", dTag}, {"title", dTag}}
	for _, relay := range relays {
		tags = append(tags, nostr.Tag{"relay", relay})
	}
	event := &nostr.Event{Kind: nostr.Kind(kinds.RelaySetDiscovery), CreatedAt: createdAt, Tags: tags}
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	if err := event.Sign(secret); err != nil {
		t.Fatalf("sign relay set event: %v", err)
	}
	return event
}

func mustPublicKey(t *testing.T, privateKey string) string {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(privateKey)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	return secret.Public().Hex()
}
