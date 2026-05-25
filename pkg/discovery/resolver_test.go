package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/stretchr/testify/require"
)

func TestResolverParsesAndResolvesEndpointEvent(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := signedEndpointEvent(t, secretKey, "api.prod.example.com", nostr.Now(), map[string]any{
		"addr":  "10.0.0.12",
		"port":  443,
		"proto": "https",
	}, nostr.Tags{
		{"d", "drydock:api:prod"},
		{"dns", "api.prod.example.com"},
		{"environment", "prod"},
		{"zone", "example.com"},
		{"health", "healthy"},
		{"capability", "llm"},
		{"capability", "gpu"},
		{"runtime", "vllm"},
		{"hardware", "a100"},
	})

	require.NoError(t, resolver.applyEvent(event))

	endpoint, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.True(t, ok)
	require.Equal(t, Endpoint{
		FQDN:         "api.prod.example.com",
		Name:         "api",
		Environment:  "prod",
		ZoneName:     "example.com",
		Address:      "10.0.0.12",
		Port:         443,
		Protocol:     "https",
		Health:       "healthy",
		Capabilities: []string{"llm", "gpu"},
		Runtime:      "vllm",
		Hardware:     "a100",
		UpdatedAt:    time.Unix(int64(event.CreatedAt), 0).UTC(),
	}, endpoint)

	endpoint, ok = resolver.Resolve("api", "prod")
	require.True(t, ok)
	require.Equal(t, "api.prod.example.com", endpoint.FQDN)
}

func TestResolverFallsBackToContentFQDNAndLegacyContentFields(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := signedEndpointEvent(t, secretKey, "api.prod.example.com", nostr.Now(), map[string]any{
		"fqdn":     "api.prod.example.com",
		"address":  "10.0.0.12",
		"port":     443,
		"protocol": "https",
	}, nostr.Tags{
		{"d", "drydock:api:prod"},
		{"env", "prod"},
		{"zone", "example.com"},
		{"health", "healthy"},
		{"capability", "llm"},
	})

	require.NoError(t, resolver.applyEvent(event))

	endpoint, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.True(t, ok)
	require.Equal(t, "10.0.0.12", endpoint.Address)
	require.Equal(t, "https", endpoint.Protocol)
	require.Equal(t, []string{"llm"}, endpoint.Capabilities)
}

func TestResolverAppliesNewestReplaceableEvent(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
	newer := signedEndpointEvent(t, secretKey, "api.prod.example.com", base+10, map[string]any{
		"addr": "10.0.0.20", "port": 8443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	older := signedEndpointEvent(t, secretKey, "api.prod.example.com", base, map[string]any{
		"addr": "10.0.0.10", "port": 443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))

	require.NoError(t, resolver.applyEvent(newer))
	require.NoError(t, resolver.applyEvent(older))

	endpoint, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.True(t, ok)
	require.Equal(t, "10.0.0.20", endpoint.Address)
	require.Equal(t, 8443, endpoint.Port)
}

func TestResolverHandlesTombstones(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
	endpoint := signedEndpointEvent(t, secretKey, "api.prod.example.com", base, map[string]any{
		"addr": "10.0.0.10", "port": 443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	tombstone := signedEndpointEvent(t, secretKey, "api.prod.example.com", base+1, map[string]any{
		"deleted": true,
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "unknown", "llm"))
	olderLiveEvent := signedEndpointEvent(t, secretKey, "api.prod.example.com", base, map[string]any{
		"addr": "10.0.0.11", "port": 443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))

	require.NoError(t, resolver.applyEvent(endpoint))
	require.NoError(t, resolver.applyEvent(tombstone))
	require.NoError(t, resolver.applyEvent(olderLiveEvent))

	_, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.False(t, ok)
	require.Empty(t, resolver.Endpoints())
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
}

func TestResolverConsumesEOSEAndLiveEventsWithoutRefreshTicker(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)
	resolver := New([]string{"wss://relay.example.test"}, pubkey)

	events := make(chan *nostr.Event, 1)
	events <- signedEndpointEvent(t, secretKey, "api.prod.example.com", nostr.Now(), map[string]any{
		"addr": "10.0.0.30", "port": 443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	close(events)
	eose := make(chan struct{})
	close(eose)
	relayEOSE := make(chan nostradapter.RelayEOSE, 1)
	relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://relay.example.test", SubscriptionID: "sub-1"}
	close(relayEOSE)
	closed := make(chan nostradapter.RelayClosed)
	close(closed)

	_, err = resolver.consume(context.Background(), &fakeRelayPool{}, &nostradapter.MergedSubscription{
		Events:            events,
		EndOfStoredEvents: eose,
		RelayEOSE:         relayEOSE,
		Closed:            closed,
	}, map[string]struct{}{})

	require.ErrorContains(t, err, "subscription event stream closed")
	endpoint, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.True(t, ok)
	require.Equal(t, "10.0.0.30", endpoint.Address)
}

func TestResolverRetriesAfterAuthRequiredClosed(t *testing.T) {
	resolver := New([]string{"wss://relay.example.test"}, "author")
	pool := &fakeRelayPool{}
	closed := make(chan nostradapter.RelayClosed, 1)
	closed <- nostradapter.RelayClosed{RelayURL: "wss://relay.example.test", SubscriptionID: "sub-1", Reason: "auth-required: restricted"}
	close(closed)

	retry, err := resolver.consume(context.Background(), pool, &nostradapter.MergedSubscription{Closed: closed}, map[string]struct{}{})

	require.NoError(t, err)
	require.True(t, retry)
	require.Equal(t, []string{"auth:wss://relay.example.test"}, pool.calls)
}

func TestResolverFindsByCapability(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	createdAt := nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
	require.NoError(t, resolver.applyEvent(signedEndpointEvent(t, secretKey, "llm.prod.example.com", createdAt, map[string]any{
		"addr": "10.0.0.21", "port": 443, "proto": "https",
	}, endpointTags("drydock:llm:prod", "llm.prod.example.com", "prod", "example.com", "healthy", "llm", "gpu"))))
	require.NoError(t, resolver.applyEvent(signedEndpointEvent(t, secretKey, "speech.prod.example.com", createdAt, map[string]any{
		"addr": "10.0.0.22", "port": 443, "proto": "https",
	}, endpointTags("drydock:speech:prod", "speech.prod.example.com", "prod", "example.com", "healthy", "speech"))))

	gpuEndpoints := resolver.FindByCapability("gpu")
	require.Len(t, gpuEndpoints, 1)
	require.Equal(t, "llm.prod.example.com", gpuEndpoints[0].FQDN)

	allEndpoints := resolver.Endpoints()
	require.Len(t, allEndpoints, 2)
}

func TestResolverRejectsInvalidEvent(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := signedEndpointEvent(t, secretKey, "api.prod.example.com", nostr.Now(), map[string]any{
		"addr": "10.0.0.10", "port": 443, "proto": "https",
	}, endpointTags("drydock:api:prod", "api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	event.Content = `{"address":"tampered","port":443,"protocol":"https"}`

	require.Error(t, resolver.applyEvent(event))
	_, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.False(t, ok)
}

func signedEndpointEvent(t *testing.T, secretKey string, fqdn string, createdAt nostr.Timestamp, content map[string]any, tags nostr.Tags) *nostr.Event {
	t.Helper()
	if tags.GetD() == "" {
		tags = append(tags, nostr.Tag{"d", fqdn})
	}
	body, err := json.Marshal(content)
	require.NoError(t, err)
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)
	event := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: createdAt,
		Kind:      KindDNSEndpointState,
		Tags:      tags,
		Content:   string(body),
	}
	require.NoError(t, event.Sign(secretKey))
	return event
}

type fakeRelayPool struct {
	calls []string
	infos map[string]*nip11.RelayInformationDocument
}

func (p *fakeRelayPool) Connect(context.Context) {
	p.calls = append(p.calls, "connect")
}

func (p *fakeRelayPool) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostradapter.MergedSubscription, error) {
	p.calls = append(p.calls, "subscribe")
	return nil, fmt.Errorf("no fake subscription configured")
}

func (p *fakeRelayPool) FetchAllRelayInfo(context.Context) map[string]*nip11.RelayInformationDocument {
	p.calls = append(p.calls, "fetch_info")
	return p.infos
}

func (p *fakeRelayPool) AuthenticateRelay(_ context.Context, relayURL string) error {
	p.calls = append(p.calls, "auth:"+relayURL)
	return nil
}

func (p *fakeRelayPool) Close() {
	p.calls = append(p.calls, "close")
}

func endpointTags(coordinate, fqdn, environment, zone, health string, capabilities ...string) nostr.Tags {
	tags := nostr.Tags{
		{"d", coordinate},
		{"dns", fqdn},
		{"environment", environment},
		{"zone", zone},
		{"health", health},
	}
	for _, capability := range capabilities {
		tags = append(tags, nostr.Tag{"capability", capability})
	}
	return tags
}
