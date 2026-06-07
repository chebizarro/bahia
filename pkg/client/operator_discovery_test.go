package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func TestDiscoverOperatorRelaysPrefersContextVMRelaySet(t *testing.T) {
	serviceKey := nostr.GeneratePrivateKey()
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
	if got := filter.Kinds; len(got) != 1 || got[0] != kinds.RelaySetDiscovery {
		t.Fatalf("filter kinds = %#v, want NIP-51 relay set kind", got)
	}
	if got := filter.Authors; len(got) != 1 || got[0] != trustedPubkey {
		t.Fatalf("filter authors = %#v, want trusted service pubkey", got)
	}
	if got := filter.Tags["d"]; strings.Join(got, ",") != operatorContextVMRelaySet+","+operatorBrowserRelaySet {
		t.Fatalf("filter #d = %#v, want scoped relay-set names", got)
	}
}

func TestDiscoverOperatorRelaysFallsBackToBrowserRelaySet(t *testing.T) {
	serviceKey := nostr.GeneratePrivateKey()
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
	serviceKey := nostr.GeneratePrivateKey()
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

func TestDiscoverOperatorRelaysRejectsMissingTrustAndUntrustedEvents(t *testing.T) {
	if _, err := discoverOperatorRelaysWithTransport(context.Background(), nil, newFakeOperatorTransport()); err == nil || !strings.Contains(err.Error(), "trusted service pubkey") {
		t.Fatalf("missing trust error = %v, want deterministic trusted service pubkey failure", err)
	}

	trustedKey := nostr.GeneratePrivateKey()
	untrustedKey := nostr.GeneratePrivateKey()
	transport := newFakeOperatorTransport()
	transport.events <- signedRelaySetEvent(t, untrustedKey, operatorContextVMRelaySet, []string{"wss://untrusted.example"}, nostr.Now())
	close(transport.eose)

	_, err := discoverOperatorRelaysWithTransport(context.Background(), []string{mustPublicKey(t, trustedKey)}, transport)
	if err == nil || !strings.Contains(err.Error(), "no trusted operator relay set events") {
		t.Fatalf("untrusted discovery error = %v, want no trusted relay set failure", err)
	}
}

func TestDiscoverOperatorRelaysRejectsUnsignedOrWrongDTagEvents(t *testing.T) {
	serviceKey := nostr.GeneratePrivateKey()
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
	serviceKey := nostr.GeneratePrivateKey()
	_, err := DiscoverOperatorRelays(context.Background(), OperatorRelayDiscoveryConfig{TrustedServicePubkeys: []string{mustPublicKey(t, serviceKey)}})
	if err == nil || !strings.Contains(err.Error(), "bootstrap relay") {
		t.Fatalf("DiscoverOperatorRelays() error = %v, want bootstrap relay requirement", err)
	}
}

func TestDiscoverOperatorRelaysReportsDeadlineBeforeEOSE(t *testing.T) {
	serviceKey := nostr.GeneratePrivateKey()
	trustedPubkey := mustPublicKey(t, serviceKey)
	transport := newFakeOperatorTransport()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := discoverOperatorRelaysWithTransport(ctx, []string{trustedPubkey}, transport)
	if err == nil || !strings.Contains(err.Error(), "timed out before EOSE") {
		t.Fatalf("deadline error = %v, want explicit timeout before EOSE", err)
	}
}

func signedRelaySetEvent(t *testing.T, privateKey, dTag string, relays []string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", dTag}, {"title", dTag}}
	for _, relay := range relays {
		tags = append(tags, nostr.Tag{"relay", relay})
	}
	event := &nostr.Event{Kind: kinds.RelaySetDiscovery, CreatedAt: createdAt, Tags: tags}
	if err := event.Sign(privateKey); err != nil {
		t.Fatalf("sign relay set event: %v", err)
	}
	return event
}

func mustPublicKey(t *testing.T, privateKey string) string {
	t.Helper()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	return pubkey
}
