package discovery

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

func TestResolverParsesAndResolvesEndpointEvent(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	event := signedEndpointEvent(t, secretKey, "api.prod.example.com", nostr.Now(), map[string]any{
		"address":  "10.0.0.12",
		"port":     443,
		"protocol": "https",
	}, nostr.Tags{
		{"d", "api.prod.example.com"},
		{"env", "prod"},
		{"zone", "example.com"},
		{"health", "healthy"},
		{"cap", "llm"},
		{"cap", "gpu"},
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

func TestResolverAppliesNewestReplaceableEvent(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	base := nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
	newer := signedEndpointEvent(t, secretKey, "api.prod.example.com", base+10, map[string]any{
		"address":  "10.0.0.20",
		"port":     8443,
		"protocol": "https",
	}, endpointTags("api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	older := signedEndpointEvent(t, secretKey, "api.prod.example.com", base, map[string]any{
		"address":  "10.0.0.10",
		"port":     443,
		"protocol": "https",
	}, endpointTags("api.prod.example.com", "prod", "example.com", "healthy", "llm"))

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
		"address":  "10.0.0.10",
		"port":     443,
		"protocol": "https",
	}, endpointTags("api.prod.example.com", "prod", "example.com", "healthy", "llm"))
	tombstone := signedEndpointEvent(t, secretKey, "api.prod.example.com", base+1, map[string]any{
		"deleted": true,
	}, endpointTags("api.prod.example.com", "prod", "example.com", "unknown", "llm"))
	olderLiveEvent := signedEndpointEvent(t, secretKey, "api.prod.example.com", base, map[string]any{
		"address":  "10.0.0.11",
		"port":     443,
		"protocol": "https",
	}, endpointTags("api.prod.example.com", "prod", "example.com", "healthy", "llm"))

	require.NoError(t, resolver.applyEvent(endpoint))
	require.NoError(t, resolver.applyEvent(tombstone))
	require.NoError(t, resolver.applyEvent(olderLiveEvent))

	_, ok := resolver.ResolveByFQDN("api.prod.example.com")
	require.False(t, ok)
	require.Empty(t, resolver.Endpoints())
}

func TestResolverFindsByCapability(t *testing.T) {
	secretKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(secretKey)
	require.NoError(t, err)

	resolver := New([]string{"wss://relay.example.test"}, pubkey)
	createdAt := nostr.Timestamp(time.Now().Add(-time.Hour).Unix())
	require.NoError(t, resolver.applyEvent(signedEndpointEvent(t, secretKey, "llm.prod.example.com", createdAt, map[string]any{
		"address": "10.0.0.21", "port": 443, "protocol": "https",
	}, endpointTags("llm.prod.example.com", "prod", "example.com", "healthy", "llm", "gpu"))))
	require.NoError(t, resolver.applyEvent(signedEndpointEvent(t, secretKey, "speech.prod.example.com", createdAt, map[string]any{
		"address": "10.0.0.22", "port": 443, "protocol": "https",
	}, endpointTags("speech.prod.example.com", "prod", "example.com", "healthy", "speech"))))

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
		"address": "10.0.0.10", "port": 443, "protocol": "https",
	}, endpointTags("api.prod.example.com", "prod", "example.com", "healthy", "llm"))
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

func endpointTags(fqdn, environment, zone, health string, capabilities ...string) nostr.Tags {
	tags := nostr.Tags{
		{"d", fqdn},
		{"env", environment},
		{"zone", zone},
		{"health", health},
	}
	for _, capability := range capabilities {
		tags = append(tags, nostr.Tag{"cap", capability})
	}
	return tags
}
