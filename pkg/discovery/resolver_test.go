package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

func TestResolverSubscribesToCanonicalDNSEndpointState(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	filter := resolver.subscriptionFilter()

	author, err := nostrutil.PubKeyFromHex(pubkey)
	require.NoError(t, err)
	require.Equal(t, []nostr.Kind{nostr.Kind(kinds.CASControlState)}, filter.Kinds, "must read canonical 30900, not legacy 31976")
	require.Equal(t, []nostr.PubKey{author}, filter.Authors)
	require.Equal(t, nostr.TagMap{"t": []string{kinds.DNSEndpointTopic}}, filter.Tags)
	require.Zero(t, filter.Since, "the first subscription must backfill everything")
}

func TestResolverAppliesProducerShapedLiveEndpoints(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	createdAt := resolverTestBase()
	port := 8443
	llm := domain.DNSEndpoint{
		Family: domain.DNSEndpointFamilyLLM, Name: "chat", Environment: "prod", Zone: "llm.example.com",
		FQDN: "chat.llm.example.com", Coordinate: "llm:chat:prod", Protocol: "https", Address: "10.0.0.12",
		Port: &port, Runtime: "vllm", Hardware: "a100", Capabilities: []string{"llm", "gpu"}, Health: domain.HealthStatusHealthy,
	}
	// Service endpoints resolved from an observed host carry no port or protocol.
	service := domain.DNSEndpoint{
		Family: domain.DNSEndpointFamilyService, Name: "api", Environment: "prod", Zone: "svc.example.com",
		FQDN: "api.svc.example.com", Coordinate: "service:api:prod", Address: "10.0.0.13", Runtime: "docker",
		Health: domain.HealthStatusHealthy,
	}

	llmEvent := liveEndpointEvent(t, secretKey, llm, createdAt)
	require.NoError(t, resolver.applyEvent(llmEvent))
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, service, createdAt)))

	endpoint, ok := resolver.ResolveByFQDN("chat.llm.example.com")
	require.True(t, ok)
	require.Equal(t, Endpoint{
		FQDN:         "chat.llm.example.com",
		Name:         "chat",
		Environment:  "prod",
		ZoneName:     "llm.example.com",
		Address:      "10.0.0.12",
		Port:         8443,
		Protocol:     "https",
		Health:       "healthy",
		Capabilities: []string{"llm", "gpu"},
		Runtime:      "vllm",
		Hardware:     "a100",
		UpdatedAt:    time.Unix(int64(llmEvent.CreatedAt), 0).UTC(),
	}, endpoint)

	endpoint, ok = resolver.Resolve("api", "prod")
	require.True(t, ok)
	require.Equal(t, "api.svc.example.com", endpoint.FQDN)
	require.Equal(t, "svc.example.com", endpoint.ZoneName)
	require.Equal(t, "10.0.0.13", endpoint.Address)
	require.Zero(t, endpoint.Port)
	require.Empty(t, endpoint.Protocol)

	gpu := resolver.FindByCapability("gpu")
	require.Len(t, gpu, 1)
	require.Equal(t, "chat.llm.example.com", gpu[0].FQDN)
	require.Len(t, resolver.Endpoints(), 2)
}

func TestResolverTreatsDeletedFalseAsLive(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), resolverTestBase())
	require.Equal(t, "false", firstTagValue(event.Tags, kinds.CASControlStateTagDeleted), "producer stamps deleted=false on live records")

	require.NoError(t, resolver.applyEvent(event))

	endpoint, ok := resolver.ResolveByFQDN("api.svc.example.com")
	require.True(t, ok, "a deleted tag with value false is not a tombstone")
	require.Equal(t, "10.0.0.10", endpoint.Address)
}

func TestResolverTombstoneRemovesEndpointAndStaleLiveCannotResurrect(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, api, base)))
	require.NoError(t, resolver.applyEvent(endpointTombstoneEvent(t, secretKey, api.Coordinate, api.FQDN, base+10)))

	_, ok := resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok)
	require.Empty(t, resolver.Endpoints())

	// A stale live record replayed from another relay must not bring it back.
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.11"), base+5)))
	_, ok = resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok)

	// A genuinely newer live record re-creates the endpoint.
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.12"), base+20)))
	endpoint, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok)
	require.Equal(t, "10.0.0.12", endpoint.Address)
}

func TestResolverNIP09DeletionRemovesEndpoint(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, api, base)))
	_, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok, "endpoint should exist before deletion")

	// A kind-5 deletion targeting the coordinate via "a" tag.
	deletion := makeKind5Deletion(t, secretKey, api.Coordinate, pubkey, base+10)
	require.NoError(t, resolver.applyEvent(deletion))
	_, ok = resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok, "endpoint should be removed after NIP-09 deletion")

	// A stale live record replayed from another relay must not resurrect it.
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.11"), base+5)))
	_, ok = resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok, "stale live event must not resurrect a deleted endpoint")

	// A genuinely newer live record re-creates the endpoint.
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.13"), base+20)))
	ep, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok)
	require.Equal(t, "10.0.0.13", ep.Address)
}

func TestResolverNIP09DeletionIgnoredForWrongKind(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, api, base)))

	// A kind-5 deletion that targets a different kind should be rejected.
	deletion := &nostr.Event{
		CreatedAt: base + 10,
		Kind:      nostr.KindDeletion,
		Tags: nostr.Tags{
			{"k", "31976"}, // wrong kind
			{"a", strconv.Itoa(kinds.CASControlState) + ":" + pubkey + ":" + api.Coordinate},
		},
	}
	signResolverEvent(t, deletion, secretKey)
	err := resolver.applyEvent(deletion)
	require.Error(t, err, "kind-5 targeting wrong kind should be rejected")

	_, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok, "endpoint should still exist")
}

func TestResolverNIP09DeletionIgnoredFromUntrustedAuthor(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	forgerKey, _ := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, api, base)))
	_, ok := resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok, "endpoint should exist before deletion attempt")

	// A kind-5 deletion from a different author must be rejected even if it
	// targets the right coordinate and kind — NIP-09 only honours deletions
	// from the event's own author.
	deletion := makeKind5Deletion(t, forgerKey, api.Coordinate, pubkey, base+10)
	err := resolver.applyEvent(deletion)
	require.Error(t, err, "kind-5 from untrusted author must be rejected")
	require.Contains(t, err.Error(), "unexpected author")

	_, ok = resolver.ResolveByFQDN(api.FQDN)
	require.True(t, ok, "endpoint must survive a forged deletion")
}

func TestResolverDeletionFilterIncludesKind5(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	filter := resolver.deletionFilter()
	require.Equal(t, []nostr.Kind{nostr.KindDeletion}, filter.Kinds)
	require.Equal(t, nostr.TagMap{"k": []string{strconv.Itoa(kinds.CASControlState)}}, filter.Tags)
}

func TestResolverIgnoresStaleEvents(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.20"), base+10)))
	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), base)))

	endpoint, ok := resolver.ResolveByFQDN("api.svc.example.com")
	require.True(t, ok)
	require.Equal(t, "10.0.0.20", endpoint.Address)
}

func TestResolverBreaksCreatedAtTiesByLowestEventID(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	createdAt := resolverTestBase()
	first := liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.31"), createdAt)
	second := liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.32"), createdAt)
	winner := "10.0.0.31"
	if nostrutil.EventIDHex(second) < nostrutil.EventIDHex(first) {
		winner = "10.0.0.32"
	}

	for _, order := range [][]*nostr.Event{{first, second}, {second, first}} {
		resolver := New([]string{"wss://relay.example.test"}, pubkey)
		for _, event := range order {
			require.NoError(t, resolver.applyEvent(event))
		}
		endpoint, ok := resolver.ResolveByFQDN("api.svc.example.com")
		require.True(t, ok)
		require.Equal(t, winner, endpoint.Address, "equal created_at must resolve to the lowest event id regardless of arrival order")
	}
}

func TestResolverIgnoresForgedAuthor(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	forgerKey, _ := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()

	require.ErrorContains(t, resolver.applyEvent(liveEndpointEvent(t, forgerKey, apiEndpoint("10.6.6.6"), base)), "unexpected author")
	require.Empty(t, resolver.Endpoints())

	require.NoError(t, resolver.applyEvent(liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), base)))
	require.ErrorContains(t, resolver.applyEvent(liveEndpointEvent(t, forgerKey, apiEndpoint("10.6.6.6"), base+10)), "unexpected author")
	require.ErrorContains(t, resolver.applyEvent(endpointTombstoneEvent(t, forgerKey, "service:api:prod", "api.svc.example.com", base+10)), "unexpected author")

	endpoint, ok := resolver.ResolveByFQDN("api.svc.example.com")
	require.True(t, ok)
	require.Equal(t, "10.0.0.10", endpoint.Address)
}

func TestResolverRejectsTamperedEvent(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), resolverTestBase())
	event.Content = `{"address":"tampered"}`

	require.Error(t, resolver.applyEvent(event))
	require.Empty(t, resolver.Endpoints())
}

func TestResolverSkipsNonEndpointStateAndLegacyKind(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	content := `{"name":"api","fqdn":"api.svc.example.com","address":"10.0.0.10"}`
	tags := nostr.Tags{{"t", kinds.DNSEndpointTopic}, {"dns", "api.svc.example.com"}, {"addr", "10.0.0.10"}}

	zone := signedEnvelopeEvent(t, secretKey, kinds.DNSZoneState, "svc.example.com", false, content, tags, base)
	require.ErrorIs(t, resolver.applyEvent(zone), errNotDNSEndpoint)

	wrongSchema := signedEnvelopeEvent(t, secretKey, kinds.DNSEndpointState, "service:api:prod", false, content, tags, base)
	setTag(wrongSchema, kinds.CASControlStateTagSchema, "bahia.cp-state.v0")
	signResolverEvent(t, wrongSchema, secretKey)
	require.ErrorIs(t, resolver.applyEvent(wrongSchema), errNotDNSEndpoint)

	legacy := signedEnvelopeEvent(t, secretKey, kinds.DNSEndpointState, "service:api:prod", false, content, tags, base)
	legacy.Kind = nostr.Kind(kinds.DNSEndpointState)
	signResolverEvent(t, legacy, secretKey)
	require.ErrorContains(t, resolver.applyEvent(legacy), "unexpected kind 31976")

	require.Empty(t, resolver.Endpoints())
}

// TestResolverBackfillsThenGoesLiveAndResumesFromCursor drives the
// subscription with unbuffered channels: each send completes only once the
// resolver has received the event, and Ready orders the EOSE transition.
func TestResolverBackfillsThenGoesLiveAndResumesFromCursor(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := resolverTestBase()
	api := apiEndpoint("10.0.0.10")
	chat := apiEndpoint("10.0.0.20")
	chat.Name, chat.FQDN, chat.Coordinate = "chat", "chat.svc.example.com", "service:chat:prod"

	events := make(chan *nostr.Event)
	eose := make(chan struct{})
	pool := &fakeRelayPool{subs: []*nostradapter.MergedSubscription{{Events: events, EndOfStoredEvents: eose}}}
	done := make(chan error, 1)
	go func() { done <- resolver.subscribeUntilClosed(context.Background(), pool) }()

	// Backfill, newest first as relays usually return it.
	events <- liveEndpointEvent(t, secretKey, chat, base+10)
	events <- liveEndpointEvent(t, secretKey, api, base)
	select {
	case <-resolver.Ready():
		t.Fatal("resolver reported ready before EOSE")
	default:
	}
	close(eose)
	waitReady(t, resolver)

	// Live: the api endpoint is removed after catch-up.
	events <- endpointTombstoneEvent(t, secretKey, api.Coordinate, api.FQDN, base+20)
	close(events)
	require.ErrorContains(t, waitDone(t, done), "subscription event stream closed")

	_, ok := resolver.ResolveByFQDN(api.FQDN)
	require.False(t, ok)
	endpoint, ok := resolver.ResolveByFQDN(chat.FQDN)
	require.True(t, ok)
	require.Equal(t, "10.0.0.20", endpoint.Address)

	// Reconnect resumes from the newest seen created_at with an overlap.
	resumed := make(chan *nostr.Event)
	close(resumed)
	pool.push(&nostradapter.MergedSubscription{Events: resumed})
	require.Error(t, resolver.subscribeUntilClosed(context.Background(), pool))

	filters := pool.subscribedFilters()
	require.Len(t, filters, 2)
	require.Zero(t, filters[0][0].Since)
	require.Equal(t, base+20-nostr.Timestamp(resolverSinceOverlap/time.Second), filters[1][0].Since)
}

func TestResolverDoesNotAdvanceCursorBeforeEOSE(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	events := make(chan *nostr.Event)
	pool := &fakeRelayPool{subs: []*nostradapter.MergedSubscription{{Events: events, EndOfStoredEvents: make(chan struct{})}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- resolver.subscribeUntilClosed(ctx, pool) }()

	// The send completes once the resolver holds the event; it is applied
	// before the resolver selects again, so cancelling here interrupts the
	// backfill after the event but before any EOSE.
	events <- liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.10"), resolverTestBase()+10)
	cancel()
	require.ErrorIs(t, waitDone(t, done), context.Canceled)

	select {
	case <-resolver.Ready():
		t.Fatal("resolver reported ready without EOSE")
	default:
	}
	require.Zero(t, resolver.subscriptionFilter().Since, "an interrupted backfill must be redone in full")
	_, ok := resolver.ResolveByFQDN("api.svc.example.com")
	require.True(t, ok)
}

func TestResolverPreparesRelayMetadataBeforeConnecting(t *testing.T) {
	pool := &fakeRelayPool{
		infos: map[string]*nip11.RelayInformationDocument{
			"wss://relay.example.test": {Name: "test", SupportedNIPs: []any{float64(1), float64(11)}},
			"wss://down.example.test":  nil,
		},
	}
	resolver := New([]string{"wss://relay.example.test", "wss://down.example.test"}, "author")

	resolver.prepareRelays(context.Background(), pool)

	require.Equal(t, []string{"fetch_info", "connect"}, pool.calls)
	metadata := resolver.RelayMetadata()
	require.Equal(t, "metadata-ok", metadata["wss://relay.example.test"].Status)
	require.Equal(t, []int{1, 11}, metadata["wss://relay.example.test"].SupportedNIPs)
	require.Equal(t, "metadata-unavailable", metadata["wss://down.example.test"].Status)
}

func TestResolverNIP11MetadataIsAdvisoryForMissingMalformedAndLimitingRelays(t *testing.T) {
	pool := &fakeRelayPool{
		infos: map[string]*nip11.RelayInformationDocument{
			"wss://malformed.example.test": {Name: "malformed", SupportedNIPs: []any{float64(1.5), map[string]any{"bad": true}, float64(11)}},
			"wss://limited.example.test": {
				Name:          "limited",
				SupportedNIPs: []any{float64(1), float64(11), float64(42)},
				Limitation: &nip11.RelayLimitationDocument{
					AuthRequired:     true,
					PaymentRequired:  true,
					RestrictedWrites: true,
					MaxLimit:         25,
				},
			},
		},
	}
	resolver := New([]string{
		"wss://missing.example.test",
		"wss://malformed.example.test",
		"wss://limited.example.test",
	}, "author")

	resolver.prepareRelays(context.Background(), pool)

	require.Equal(t, []string{"fetch_info", "connect"}, pool.calls)
	metadata := resolver.RelayMetadata()
	require.Len(t, metadata, 3)
	require.Equal(t, "metadata-unavailable", metadata["wss://missing.example.test"].Status)
	require.Equal(t, "metadata-malformed", metadata["wss://malformed.example.test"].Status)
	require.Contains(t, metadata["wss://malformed.example.test"].Error, "supported_nips")
	require.Equal(t, []int{11}, metadata["wss://malformed.example.test"].SupportedNIPs)

	limited := metadata["wss://limited.example.test"]
	require.Equal(t, "metadata-limited", limited.Status)
	require.Equal(t, []int{1, 11, 42}, limited.SupportedNIPs)
	require.True(t, limited.Limitations.AuthRequired)
	require.True(t, limited.Limitations.PaymentRequired)
	require.True(t, limited.Limitations.RestrictedWrites)
	require.Equal(t, 25, limited.Limitations.MaxLimit)
	require.ElementsMatch(t, []string{"auth-required", "payment-required", "restricted-writes", "max-limit:25"}, limited.Warnings)
}

// The pool answers "auth-required:" itself (AUTH, then reissue on that
// relay), so a CLOSED reaching the resolver is only logged: it neither
// authenticates nor tears the subscription down.
func TestResolverLeavesAuthRequiredClosedToThePool(t *testing.T) {
	resolver := New([]string{"wss://relay.example.test"}, "author")
	closed := make(chan nostradapter.RelayClosed, 1)
	closed <- nostradapter.RelayClosed{RelayURL: "wss://relay.example.test", SubscriptionID: "sub-1", Reason: "auth-required: restricted", Terminal: true}
	close(closed)
	events := make(chan *nostr.Event)
	close(events)

	err := resolver.consume(context.Background(), &nostradapter.MergedSubscription{Events: events, Closed: closed})

	require.ErrorContains(t, err, "subscription event stream closed")
}

func resolverTestBase() nostr.Timestamp {
	return nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
}

func apiEndpoint(address string) domain.DNSEndpoint {
	return domain.DNSEndpoint{
		Family: domain.DNSEndpointFamilyService, Name: "api", Environment: "prod", Zone: "svc.example.com",
		FQDN: "api.svc.example.com", Coordinate: "service:api:prod", Address: address, Health: domain.HealthStatusHealthy,
	}
}

// makeKind5Deletion creates a signed NIP-09 kind-5 deletion event targeting
// a kind-30900 coordinate via an "a" tag.
func makeKind5Deletion(t *testing.T, secretKey, coordinate, pubkey string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	event := &nostr.Event{
		CreatedAt: createdAt,
		Kind:      nostr.KindDeletion,
		Tags: nostr.Tags{
			{"k", strconv.Itoa(kinds.CASControlState)},
			{"a", strconv.Itoa(kinds.CASControlState) + ":" + pubkey + ":" + coordinate},
		},
	}
	signResolverEvent(t, event, secretKey)
	return event
}

func generatedResolverKeyPair(t *testing.T) (string, string) {
	t.Helper()
	secretKey := nostrutil.GeneratePrivateKeyHex()
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(secretKey)
	require.NoError(t, err)
	return secretKey, pubkey
}

// liveEndpointEvent mirrors projector.publishDNSEndpoint: domain.DNSEndpoint
// JSON content plus dnsEndpointTags, wrapped in controlStateEnvelope tags.
func liveEndpointEvent(t *testing.T, secretKey string, endpoint domain.DNSEndpoint, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"family", string(endpoint.Family)}, {"health", string(endpoint.Health)}, {"dns", endpoint.FQDN}, {"addr", endpoint.Address}, {"t", kinds.DNSEndpointTopic}, {"t", "bahia"}}
	if endpoint.Environment != "" {
		tags = append(tags, nostr.Tag{"environment", endpoint.Environment})
	}
	if endpoint.Runtime != "" {
		tags = append(tags, nostr.Tag{"runtime", endpoint.Runtime})
	}
	if endpoint.Protocol != "" {
		tags = append(tags, nostr.Tag{"proto", endpoint.Protocol})
	}
	if endpoint.Port != nil {
		tags = append(tags, nostr.Tag{"port", strconv.Itoa(*endpoint.Port)})
	}
	switch endpoint.Family {
	case domain.DNSEndpointFamilyService:
		tags = append(tags, nostr.Tag{"service", endpoint.Name})
	case domain.DNSEndpointFamilyLLM:
		tags = append(tags, nostr.Tag{"route", endpoint.Name})
	}
	for _, capability := range endpoint.Capabilities {
		tags = append(tags, nostr.Tag{"capability", capability})
	}
	content, err := json.Marshal(endpoint)
	require.NoError(t, err)
	return signedEnvelopeEvent(t, secretKey, kinds.DNSEndpointState, endpoint.Coordinate, false, string(content), tags, createdAt)
}

// endpointTombstoneEvent mirrors projector.publishDNSEndpointTombstone.
func endpointTombstoneEvent(t *testing.T, secretKey, coordinate, fqdn string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{"deleted": true, "coordinate": coordinate, "fqdn": fqdn, "updated_at": time.Unix(int64(createdAt), 0).UTC().Format(time.RFC3339Nano)})
	require.NoError(t, err)
	tags := nostr.Tags{{"t", kinds.DNSEndpointTopic}, {"t", "bahia"}, {"dns", fqdn}}
	return signedEnvelopeEvent(t, secretKey, kinds.DNSEndpointState, coordinate, true, string(content), tags, createdAt)
}

// signedEnvelopeEvent mirrors controlStateEnvelope for a DNS compatibility kind.
func signedEnvelopeEvent(t *testing.T, secretKey string, legacyKind int, d string, deleted bool, content string, extra nostr.Tags, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	tags := nostr.Tags{
		{kinds.CASControlStateTagD, d},
		{kinds.CASControlStateTagDomain, kinds.DNSDomain},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
		{kinds.CASControlStateTagLegacyKind, strconv.Itoa(legacyKind)},
		{kinds.CASControlStateTagDeleted, strconv.FormatBool(deleted)},
	}
	event := &nostr.Event{CreatedAt: createdAt, Kind: nostr.Kind(kinds.CASControlState), Tags: append(tags, extra...), Content: content}
	signResolverEvent(t, event, secretKey)
	return event
}

func signResolverEvent(t *testing.T, event *nostr.Event, secretKey string) {
	t.Helper()
	pubkeyHex, err := nostrutil.PublicKeyHexFromPrivateKeyHex(secretKey)
	require.NoError(t, err)
	event.PubKey, err = nostrutil.PubKeyFromHex(pubkeyHex)
	require.NoError(t, err)
	require.NoError(t, nostrutil.SignEventWithHexKey(event, secretKey))
}

func setTag(event *nostr.Event, key, value string) {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == key {
			tag[1] = value
		}
	}
}

// waitReady and waitDone bound a hung test; they do not order anything.
func waitReady(t *testing.T, resolver *Resolver) {
	t.Helper()
	select {
	case <-resolver.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for resolver EOSE")
	}
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for subscription to end")
		return nil
	}
}

type fakeRelayPool struct {
	mu      sync.Mutex
	calls   []string
	infos   map[string]*nip11.RelayInformationDocument
	subs    []*nostradapter.MergedSubscription
	filters [][]nostr.Filter
}

func (p *fakeRelayPool) push(sub *nostradapter.MergedSubscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs = append(p.subs, sub)
}

func (p *fakeRelayPool) subscribedFilters() [][]nostr.Filter {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]nostr.Filter(nil), p.filters...)
}

func (p *fakeRelayPool) Connect(context.Context) {
	p.calls = append(p.calls, "connect")
}

func (p *fakeRelayPool) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (*nostradapter.MergedSubscription, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "subscribe")
	p.filters = append(p.filters, filters)
	if len(p.subs) == 0 {
		return nil, errors.New("no fake subscription configured")
	}
	sub := p.subs[0]
	p.subs = p.subs[1:]
	return sub, nil
}

func (p *fakeRelayPool) FetchAllRelayInfo(context.Context) map[string]*nip11.RelayInformationDocument {
	p.calls = append(p.calls, "fetch_info")
	return p.infos
}

func (p *fakeRelayPool) Close() {
	p.calls = append(p.calls, "close")
}

// TestResolverEventStreamCloseDoesNotMarkReady proves that when the event
// stream closes (relay disconnect / timeout) without an EndOfStoredEvents
// signal, the resolver does NOT mark itself as ready. This verifies the
// fix from.30: only a real EOSE counts as completion, not channel close
// or context timeout. (.40 item 4,.63 verification)
func TestResolverEventStreamCloseDoesNotMarkReady(t *testing.T) {
	secretKey, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	events := make(chan *nostr.Event, 1)
	events <- liveEndpointEvent(t, secretKey, apiEndpoint("10.0.0.20"), resolverTestBase()+5)
	close(events) // simulate relay disconnect — no EOSE

	err := resolver.consume(context.Background(), &nostradapter.MergedSubscription{
		Events:            events,
		EndOfStoredEvents: make(chan struct{}), // never closed
	})

	require.ErrorContains(t, err, "subscription event stream closed")

	// The resolver must NOT be ready: EOSE never fired.
	select {
	case <-resolver.Ready():
		t.Fatal("resolver reported ready on event stream close without EOSE — " +
			"only EndOfStoredEvents must trigger readiness")
	default:
		// correct: not ready
	}

	// The resume cursor must not have advanced (a partial backfill is redone
	// in full on the next subscription cycle).
	require.Zero(t, resolver.subscriptionFilter().Since,
		"cursor advanced without EOSE — partial backfill must be redoable")
}

// TestResolverContextCancelDoesNotMarkReady proves that cancelling the
// context during a backfill (before EOSE) does not mark the resolver ready.
func TestResolverContextCancelDoesNotMarkReady(t *testing.T) {
	_, pubkey := generatedResolverKeyPair(t)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancel — no events, no EOSE

	err := resolver.consume(ctx, &nostradapter.MergedSubscription{
		Events:            make(chan *nostr.Event),
		EndOfStoredEvents: make(chan struct{}),
	})

	require.ErrorIs(t, err, context.Canceled)

	select {
	case <-resolver.Ready():
		t.Fatal("resolver reported ready after context cancellation without EOSE")
	default:
		// correct: not ready
	}
}
