package fipsbridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/stretchr/testify/require"
)

const testPrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"

// Worker identities referenced by endpoint records (hex, as the projector's
// npub tag carries them).
const (
	workerA = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	workerB = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	workerC = "f9308a019258c31049344f85f89d5229b531c845836f99b08601f113bce036f9"
)

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	_, err := LoadConfig([]byte("bridge:\n  bahia_pubky: typo\n"))
	require.ErrorContains(t, err, "field bahia_pubky not found")
}

func TestParseEndpointEventExtractsFQDNHealthAndNpub(t *testing.T) {
	pubkey, _ := testIdentity(t)
	ev := liveEndpoint(t, pubkey, endpointRecord{
		D: "endpoint:llm:review:prod", Service: "drydock", Route: "review", Environment: "prod",
		FQDN: "drydock-review.prod.cascadia", Health: "healthy", Worker: workerA,
		Capabilities: []string{"llm", "code-review"},
	}, nostr.Now())

	endpoint, err := ParseEndpointEvent(ev)
	require.NoError(t, err)
	require.Equal(t, "drydock-review.prod.cascadia", endpoint.FQDN)
	require.Equal(t, "healthy", endpoint.Health)
	require.Equal(t, "prod", endpoint.Environment)
	require.Equal(t, npubOf(t, workerA), endpoint.Npub)
	require.Equal(t, "drydock-review", endpoint.ServiceLabel)
	require.ElementsMatch(t, []string{"llm", "code-review"}, endpoint.Capabilities)
	require.False(t, endpoint.Tombstone, "live records carry deleted=false and must not be tombstones")
}

func TestParseEndpointEventRecognisesProjectorTombstone(t *testing.T) {
	pubkey, _ := testIdentity(t)
	endpoint, err := ParseEndpointEvent(endpointTombstone(t, pubkey, "endpoint:service:api:prod", "api.prod.cascadia", nostr.Now()))
	require.NoError(t, err)
	require.True(t, endpoint.Tombstone)
}

func TestBridgeHealthFilteringAddsAndRemovesHostsEntry(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, func(cfg *Config) { cfg.HealthFilter = true })
	record := endpointRecord{D: "endpoint:service:drydock:prod", Service: "drydock", Route: "review", Environment: "prod", FQDN: "drydock-review.prod.cascadia", Health: "healthy", Worker: workerA}
	now := nostr.Now()

	require.NoError(t, bridge.HandleEvent(context.Background(), liveEndpoint(t, pubkey, record, now)))
	require.Equal(t, npubOf(t, workerA), bridge.entries["drydock-review"])

	record.Health = "unhealthy"
	require.NoError(t, bridge.HandleEvent(context.Background(), liveEndpoint(t, pubkey, record, now+1)))
	require.NotContains(t, bridge.entries, "drydock-review")
}

func TestBridgeSubscriptionFilterScopesToAuthorAndDNSEndpointTopic(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge := newBridgeWithPool(Config{
		BahiaPubkey:       pubkey,
		RelayURLs:         []string{"wss://relay.example.test"},
		CapabilityFilter:  []string{"llm"},
		EnvironmentFilter: []string{"prod"},
	}, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	filter := bridge.subscriptionFilter()
	require.Equal(t, []nostr.Kind{nostr.Kind(kinds.CASControlState)}, filter.Kinds, "must read canonical 30900, not legacy 31976")
	require.Len(t, filter.Authors, 1)
	require.Equal(t, pubkey, filter.Authors[0].Hex())
	require.Equal(t, nostr.TagMap{"t": []string{kinds.DNSEndpointTopic}}, filter.Tags, "only the single-letter t tag goes to the relay")
}

func TestBridgeRejectsNonEndpointControlState(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, nil)
	now := nostr.Now()

	zone := signedStateEvent(t, pubkey, kinds.DNSZoneState, "zone:prod.cascadia", false, `{"name":"prod.cascadia"}`, nostr.Tags{{"t", kinds.DNSZoneTopic}}, now)
	require.ErrorContains(t, bridge.HandleEvent(context.Background(), zone), "not a DNS endpoint record")

	wrongSchema := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:api:prod", Service: "api", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}, now)
	for i, tag := range wrongSchema.Tags {
		if tag[0] == kinds.CASControlStateTagSchema {
			wrongSchema.Tags[i] = nostr.Tag{kinds.CASControlStateTagSchema, "bahia.state.dns-endpoint.v1"}
		}
	}
	require.NoError(t, nostrutil.SignEventWithHexKey(wrongSchema, testPrivateKey))
	require.ErrorContains(t, bridge.HandleEvent(context.Background(), wrongSchema), "unexpected schema")

	legacy := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:api:prod", Service: "api", FQDN: "api.prod.cascadia", Health: "healthy", Worker: workerA}, now)
	legacy.Kind = nostr.Kind(kinds.DNSEndpointState)
	require.NoError(t, nostrutil.SignEventWithHexKey(legacy, testPrivateKey))
	require.ErrorContains(t, bridge.HandleEvent(context.Background(), legacy), "unexpected kind 31976")

	require.Empty(t, bridge.entries)
	require.Empty(t, bridge.latest)
}

func TestBridgeFiltersByCapabilityAndEnvironment(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, func(cfg *Config) {
		cfg.CapabilityFilter = []string{"llm"}
		cfg.EnvironmentFilter = []string{"prod"}
	})
	now := nostr.Now()

	wrongEnv := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:drydock:dev", Service: "drydock", Environment: "dev", FQDN: "drydock.dev.cascadia", Health: "healthy", Worker: workerA, Capabilities: []string{"llm"}}, now)
	require.NoError(t, bridge.HandleEvent(context.Background(), wrongEnv))
	require.Empty(t, bridge.entries)

	matching := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:drydock:prod", Service: "drydock", Environment: "prod", FQDN: "drydock.prod.cascadia", Health: "healthy", Worker: workerA, Capabilities: []string{"llm"}}, now+1)
	require.NoError(t, bridge.HandleEvent(context.Background(), matching))
	require.Equal(t, npubOf(t, workerA), bridge.entries["drydock"])
}

func TestServiceLabelFromFQDNStripsZoneSuffix(t *testing.T) {
	require.Equal(t, "drydock-review", ServiceLabelFromFQDN("drydock-review.prod.cascadia.", "prod"))
	require.Equal(t, "embeddings", ServiceLabelFromFQDN("embeddings.mesh.cascadia", ""))
}

func TestBridgeSuppressesRedeliveredDuplicate(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, func(cfg *Config) { cfg.HealthFilter = false })

	ev := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:service:drydock:prod", Service: "drydock", Environment: "prod", FQDN: "drydock.prod.cascadia", Health: "healthy", Worker: workerA}, nostr.Now())
	require.NoError(t, bridge.HandleEvent(context.Background(), ev))
	require.Equal(t, npubOf(t, workerA), bridge.entries["drydock"])
	require.Len(t, bridge.latest, 1)

	require.NoError(t, bridge.HandleEvent(context.Background(), ev))
	require.Len(t, bridge.latest, 1, "re-delivery must not grow latest")
	require.Equal(t, npubOf(t, workerA), bridge.entries["drydock"])
}

func TestBridgeTieBreakSameCreatedAtLowestEventIDWins(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, func(cfg *Config) { cfg.HealthFilter = false })

	ts := nostr.Now()
	record := endpointRecord{D: "endpoint:service:drydock:prod", Service: "drydock", Environment: "prod", FQDN: "drydock.prod.cascadia", Health: "healthy", Worker: workerA}
	evA := liveEndpoint(t, pubkey, record, ts)
	record.Worker = workerB
	evB := liveEndpoint(t, pubkey, record, ts)

	first, second := evA, evB
	if nostrutil.EventIDHex(evA) < nostrutil.EventIDHex(evB) {
		first, second = evB, evA
	}
	winner := second
	coordinate := strconv.Itoa(kinds.CASControlState) + ":" + pubkey + ":" + record.D

	require.NoError(t, bridge.HandleEvent(context.Background(), first))
	require.NoError(t, bridge.HandleEvent(context.Background(), second))
	require.Len(t, bridge.latest, 1)
	require.Equal(t, nostrutil.EventIDHex(winner), bridge.latest[coordinate].EventID, "lowest event ID must win")

	require.NoError(t, bridge.HandleEvent(context.Background(), first))
	require.Equal(t, nostrutil.EventIDHex(winner), bridge.latest[coordinate].EventID, "loser must not displace winner")
}

func TestBridgeLatestDoesNotGrowWithRedeliveriesOfSameCoordinate(t *testing.T) {
	pubkey, _ := testIdentity(t)
	bridge, _ := newTestBridge(t, pubkey, func(cfg *Config) { cfg.HealthFilter = false })
	now := nostr.Now()

	for i := 0; i < 10; i++ {
		ev := liveEndpoint(t, pubkey, endpointRecord{D: "endpoint:llm:default:prod", Service: "drydock", Route: "default", Environment: "prod", FQDN: "drydock-default.prod.cascadia", Health: "healthy", Worker: workerA}, now+nostr.Timestamp(i))
		require.NoError(t, bridge.HandleEvent(context.Background(), ev))
		require.Equal(t, npubOf(t, workerA), bridge.entries["drydock-default"])
	}
	require.Len(t, bridge.latest, 1, "latest must not grow across redeliveries")
}

// endpointRecord is the subset of domain.DNSEndpoint the tests vary.
type endpointRecord struct {
	D            string
	Service      string
	Route        string
	Environment  string
	FQDN         string
	Health       string
	Worker       string
	Capabilities []string
}

// liveEndpoint mirrors projector.publishDNSEndpoint: domain.DNSEndpoint JSON
// content plus dnsEndpointTags, wrapped in the controlStateEnvelope tags.
func liveEndpoint(t *testing.T, pubkey string, record endpointRecord, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	family, name := "service", record.Service
	tags := nostr.Tags{{"family", family}, {"health", record.Health}, {"dns", record.FQDN}, {"addr", "fd00::1"}, {"t", kinds.DNSEndpointTopic}, {"t", "bahia"}}
	if record.Environment != "" {
		tags = append(tags, nostr.Tag{"environment", record.Environment})
	}
	if record.Worker != "" {
		tags = append(tags, nostr.Tag{"npub", record.Worker}, nostr.Tag{"mesh", "fips"})
	}
	if record.Service != "" {
		tags = append(tags, nostr.Tag{"service", record.Service})
	}
	if record.Route != "" {
		tags = append(tags, nostr.Tag{"route", record.Route})
	}
	for _, capability := range record.Capabilities {
		tags = append(tags, nostr.Tag{"capability", capability})
	}
	content, err := json.Marshal(map[string]any{
		"family": family, "name": name, "environment": record.Environment, "fqdn": record.FQDN,
		"coordinate": record.D, "health": record.Health, "worker_pubkey": record.Worker, "capabilities": record.Capabilities,
	})
	require.NoError(t, err)
	return signedStateEvent(t, pubkey, kinds.DNSEndpointState, record.D, false, string(content), tags, createdAt)
}

// endpointTombstone mirrors projector.publishDNSEndpointTombstone.
func endpointTombstone(t *testing.T, pubkey, d, fqdn string, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	content, err := json.Marshal(map[string]any{"deleted": true, "coordinate": d, "fqdn": fqdn})
	require.NoError(t, err)
	return signedStateEvent(t, pubkey, kinds.DNSEndpointState, d, true, string(content), nostr.Tags{{"t", kinds.DNSEndpointTopic}, {"t", "bahia"}, {"dns", fqdn}}, createdAt)
}

// signedStateEvent mirrors controlStateEnvelope for a DNS legacy kind.
func signedStateEvent(t *testing.T, pubkey string, legacyKind int, d string, deleted bool, content string, extra nostr.Tags, createdAt nostr.Timestamp) *nostr.Event {
	t.Helper()
	pubkeyValue, err := nostrutil.PubKeyFromHex(pubkey)
	require.NoError(t, err)
	tags := nostr.Tags{
		{kinds.CASControlStateTagD, d},
		{kinds.CASControlStateTagDomain, kinds.DNSDomain},
		{kinds.CASControlStateTagSchema, kinds.CASControlStateSchema},
		{kinds.CASControlStateTagLegacyKind, strconv.Itoa(legacyKind)},
		{kinds.CASControlStateTagDeleted, strconv.FormatBool(deleted)},
	}
	ev := &nostr.Event{PubKey: pubkeyValue, CreatedAt: createdAt, Kind: nostr.Kind(kinds.CASControlState), Tags: append(tags, extra...), Content: content}
	require.NoError(t, nostrutil.SignEventWithHexKey(ev, testPrivateKey))
	return ev
}

type recordingWriter struct {
	writes chan map[string]string
}

func (w recordingWriter) Write(_ context.Context, entries map[string]string) error {
	w.writes <- maps.Clone(entries)
	return nil
}

// next returns the next hosts write. The timer only bounds a hung test; it is
// not used to order anything.
func (w recordingWriter) next(t *testing.T) map[string]string {
	t.Helper()
	select {
	case entries := <-w.writes:
		return entries
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for hosts write")
		return nil
	}
}

func (w recordingWriter) requireNoPending(t *testing.T) {
	t.Helper()
	select {
	case entries := <-w.writes:
		t.Fatalf("unexpected extra hosts write: %v", entries)
	default:
	}
}

// newTestBridge returns a bridge with a recording writer and no relay pool. It
// has not caught up, so direct HandleEvent calls only update state; the
// relay-driven tests in bridge_store_test.go cover the live phase.
func newTestBridge(t *testing.T, pubkey string, configure func(*Config)) (*Bridge, recordingWriter) {
	t.Helper()
	cfg := Config{
		BahiaPubkey:          pubkey,
		RelayURLs:            []string{"wss://relay.example.test"},
		HostsPath:            filepath.Join(t.TempDir(), "hosts"),
		ManagedSectionMarker: DefaultManagedSectionMarker,
		HealthFilter:         true,
	}
	if configure != nil {
		configure(&cfg)
	}
	bridge := newBridgeWithPool(cfg, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	writer := recordingWriter{writes: make(chan map[string]string, 32)}
	bridge.writer = writer
	return bridge, writer
}

func npubOf(t *testing.T, hex string) string {
	t.Helper()
	npub, err := nostrutil.EncodeNpubFromHex(hex)
	require.NoError(t, err)
	return npub
}

func testIdentity(t *testing.T) (string, string) {
	t.Helper()
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(testPrivateKey)
	require.NoError(t, err)
	npub, err := nostrutil.EncodeNpubFromHex(pubkey)
	require.NoError(t, err)
	return pubkey, npub
}
