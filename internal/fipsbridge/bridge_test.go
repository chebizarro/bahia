package fipsbridge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/stretchr/testify/require"
)

const testPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"

func TestParseEndpointEventExtractsFQDNHealthAndNpub(t *testing.T) {
	pubkey, npub := testIdentity(t)
	ev := signedEndpointEvent(t, pubkey, `{"service":"drydock","route":"review","env":"prod","health":"healthy","capabilities":["llm"]}`, nostr.Tags{
		{"d", "drydock-review.prod.cascadia"},
		{"dns", "drydock-review.prod.cascadia"},
		{"health", "healthy"},
		{"npub", npub},
		{"capability", "code-review"},
	})

	endpoint, err := ParseEndpointEvent(ev)
	require.NoError(t, err)
	require.Equal(t, "drydock-review.prod.cascadia", endpoint.FQDN)
	require.Equal(t, "healthy", endpoint.Health)
	require.Equal(t, npub, endpoint.Npub)
	require.Equal(t, "drydock-review", endpoint.ServiceLabel)
	require.ElementsMatch(t, []string{"llm", "code-review"}, endpoint.Capabilities)
}

func TestParseEndpointEventEncodesHexWorkerPubkeyAsNpub(t *testing.T) {
	pubkey, wantNpub := testIdentity(t)
	ev := signedEndpointEvent(t, pubkey, `{"service":"worker","env":"prod","health":"healthy","worker_pubkey":"`+pubkey+`"}`, nostr.Tags{{"dns", "worker.prod.cascadia"}})

	endpoint, err := ParseEndpointEvent(ev)
	require.NoError(t, err)
	require.Equal(t, wantNpub, endpoint.Npub)
}

func TestBridgeHealthFilteringAddsAndRemovesHostsEntry(t *testing.T) {
	pubkey, npub := testIdentity(t)
	hostsPath := filepath.Join(t.TempDir(), "hosts")
	bridge := newBridgeWithPool(Config{
		BahiaPubkey:          pubkey,
		RelayURLs:            []string{"wss://relay.example.test"},
		HostsPath:            hostsPath,
		ManagedSectionMarker: DefaultManagedSectionMarker,
		HealthFilter:         true,
	}, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	bridge.now = func() time.Time { return time.Now().UTC() }

	healthy := signedEndpointEvent(t, pubkey, `{"service":"drydock","route":"review","env":"prod","health":"healthy"}`, nostr.Tags{
		{"d", "drydock-review.prod.cascadia"},
		{"dns", "drydock-review.prod.cascadia"},
		{"npub", npub},
	})
	require.NoError(t, bridge.HandleEvent(context.Background(), healthy))
	require.Equal(t, npub, bridge.entries["drydock-review"])

	unhealthy := signedEndpointEvent(t, pubkey, `{"service":"drydock","route":"review","env":"prod","health":"unhealthy"}`, nostr.Tags{
		{"d", "drydock-review.prod.cascadia"},
		{"dns", "drydock-review.prod.cascadia"},
		{"npub", npub},
	})
	unhealthy.CreatedAt = healthy.CreatedAt + 1
	require.NoError(t, unhealthy.Sign(testPrivateKey))
	require.NoError(t, bridge.HandleEvent(context.Background(), unhealthy))
	require.NotContains(t, bridge.entries, "drydock-review")
}

func TestBridgeFiltersByCapabilityAndEnvironment(t *testing.T) {
	pubkey, npub := testIdentity(t)
	bridge := newBridgeWithPool(Config{
		BahiaPubkey:          pubkey,
		RelayURLs:            []string{"wss://relay.example.test"},
		HostsPath:            filepath.Join(t.TempDir(), "hosts"),
		ManagedSectionMarker: DefaultManagedSectionMarker,
		HealthFilter:         true,
		CapabilityFilter:     []string{"llm"},
		EnvironmentFilter:    []string{"prod"},
	}, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	wrongEnv := signedEndpointEvent(t, pubkey, `{"service":"drydock","env":"dev","health":"healthy","capabilities":["llm"]}`, nostr.Tags{{"dns", "drydock.dev.cascadia"}, {"npub", npub}})
	require.NoError(t, bridge.HandleEvent(context.Background(), wrongEnv))
	require.Empty(t, bridge.entries)

	matching := signedEndpointEvent(t, pubkey, `{"service":"drydock","env":"prod","health":"healthy","capabilities":["llm"]}`, nostr.Tags{{"dns", "drydock.prod.cascadia"}, {"npub", npub}})
	matching.CreatedAt = wrongEnv.CreatedAt + 1
	require.NoError(t, matching.Sign(testPrivateKey))
	require.NoError(t, bridge.HandleEvent(context.Background(), matching))
	require.Equal(t, npub, bridge.entries["drydock"])
}

func TestServiceLabelFromFQDNStripsZoneSuffix(t *testing.T) {
	require.Equal(t, "drydock-review", ServiceLabelFromFQDN("drydock-review.prod.cascadia.", "prod"))
	require.Equal(t, "embeddings", ServiceLabelFromFQDN("embeddings.mesh.cascadia", ""))
}

func signedEndpointEvent(t *testing.T, pubkey, content string, tags nostr.Tags) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{PubKey: pubkey, CreatedAt: nostr.Now(), Kind: KindDNSEndpointState, Tags: tags, Content: content}
	require.NoError(t, ev.Sign(testPrivateKey))
	return ev
}

func testIdentity(t *testing.T) (string, string) {
	t.Helper()
	pubkey, err := nostr.GetPublicKey(testPrivateKey)
	require.NoError(t, err)
	npub, err := nip19.EncodePublicKey(pubkey)
	require.NoError(t, err)
	return pubkey, npub
}
