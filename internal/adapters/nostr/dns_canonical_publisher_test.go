package nostr

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// --- helpers ---

func newTestPublisher(t *testing.T, opts ...ProjectorOption) (*DNSCanonicalPublisher, *captureProjectionPublisher) {
	t.Helper()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), opts...)
	pub := NewDNSCanonicalPublisher(projector, zap.NewNop())
	return pub, sink
}

func testEndpoint(name string) domain.DNSEndpoint {
	port := 8443
	return domain.DNSEndpoint{
		Family:      domain.DNSEndpointFamilyService,
		Name:        name,
		Environment: "prod",
		Zone:        "prod.cascadia",
		FQDN:        name + ".prod.cascadia",
		Protocol:    "https",
		Address:     "10.0.1.44",
		Port:        &port,
		Runtime:     string(domain.RuntimeTypeDocker),
		Health:      domain.HealthStatusHealthy,
		DriftStatus: domain.DriftStatusInSync,
		Source:      "test",
	}
}

// --- endpoint publish & tombstone ---

func TestDNSCanonicalPublisherPublishesEndpoints(t *testing.T) {
	ctx := context.Background()
	pub, sink := newTestPublisher(t, WithDNSProjectionSource(&fakeDNSProjectionSource{}))

	endpoints := []domain.DNSEndpoint{testEndpoint("api")}
	published, tombstones, err := pub.PublishEndpoints(ctx, endpoints)
	if err != nil {
		t.Fatalf("PublishEndpoints: %v", err)
	}
	if published != 1 || tombstones != 0 {
		t.Fatalf("got published=%d tombstones=%d, want 1/0", published, tombstones)
	}

	ev := assertOneSignedKind(t, sink, KindDNSEndpointState)
	assertTag(t, ev, "d", "endpoint:service:api:prod")
	assertTag(t, ev, "family", "service")
	assertTag(t, ev, "environment", "prod")
	assertTag(t, ev, "health", "healthy")
	assertTag(t, ev, "runtime", "docker")
	assertTag(t, ev, "dns", "api.prod.cascadia")
	assertTag(t, ev, "addr", "10.0.1.44")
	assertTag(t, ev, "proto", "https")
	assertTag(t, ev, "port", "8443")
	assertTag(t, ev, "t", "dns-endpoint")
	assertTag(t, ev, "t", "bahia")
	assertJSONField(t, ev.Content, "coordinate", "endpoint:service:api:prod")
}

func TestDNSCanonicalPublisherTombstonesRemovedEndpoints(t *testing.T) {
	ctx := context.Background()
	pub, sink := newTestPublisher(t, WithDNSProjectionSource(&fakeDNSProjectionSource{}))

	// Publish one endpoint.
	endpoints := []domain.DNSEndpoint{testEndpoint("api")}
	if _, _, err := pub.PublishEndpoints(ctx, endpoints); err != nil {
		t.Fatalf("PublishEndpoints round 1: %v", err)
	}

	// Remove it.
	published, tombstones, err := pub.PublishEndpoints(ctx, nil)
	if err != nil {
		t.Fatalf("PublishEndpoints round 2: %v", err)
	}
	if published != 0 || tombstones != 1 {
		t.Fatalf("got published=%d tombstones=%d, want 0/1", published, tombstones)
	}

	events := sink.byKind(KindDNSEndpointState)
	if len(events) != 2 {
		t.Fatalf("expected live + tombstone, got %d events", len(events))
	}
	tombstone := events[1]
	assertTag(t, tombstone, "d", "endpoint:service:api:prod")
	assertTag(t, tombstone, "deleted", "true")
	assertTag(t, tombstone, "dns", "api.prod.cascadia")
	assertJSONField(t, tombstone.Content, "deleted", true)
	assertJSONField(t, tombstone.Content, "coordinate", "endpoint:service:api:prod")
}

func TestDNSCanonicalPublisherEndpointFIPSTags(t *testing.T) {
	ctx := context.Background()
	pub, sink := newTestPublisher(t, WithDNSProjectionSource(&fakeDNSProjectionSource{}))

	port := 8000
	workerPubkey := "npub1workerpubkey"
	endpoints := []domain.DNSEndpoint{{
		Family:       domain.DNSEndpointFamilyWorker,
		Name:         "t7920-l40s",
		Zone:         "edge.cascadia",
		FQDN:         "t7920-l40s.edge.cascadia",
		Protocol:     "http",
		Address:      "10.0.1.45",
		Port:         &port,
		WorkerPubkey: workerPubkey,
		Health:       domain.HealthStatusHealthy,
		DriftStatus:  domain.DriftStatusInSync,
		Source:       "test",
	}}
	if _, _, err := pub.PublishEndpoints(ctx, endpoints); err != nil {
		t.Fatalf("PublishEndpoints: %v", err)
	}

	ev := assertOneSignedKind(t, sink, KindDNSEndpointState)
	assertTag(t, ev, "d", "endpoint:worker:t7920-l40s")
	assertTag(t, ev, "worker", workerPubkey)
	assertTag(t, ev, "npub", workerPubkey)
	assertTag(t, ev, "mesh", "fips")
}

// --- zone publish & tombstone ---

func TestDNSCanonicalPublisherPublishesZone(t *testing.T) {
	ctx := context.Background()
	pub, sink := newTestPublisher(t, WithDNSZoneProjectionSource(&fakeDNSZoneProjectionSource{}))

	zone := domain.DNSZone{
		Name:       "prod.cascadia",
		Visibility: domain.ZoneVisibilityInternal,
		BackendRef: "fs-primary",
		TTL:        60,
	}
	if err := pub.PublishZone(ctx, zone); err != nil {
		t.Fatalf("PublishZone: %v", err)
	}

	ev := assertOneSignedKind(t, sink, KindDNSZoneState)
	assertTag(t, ev, "d", "zone:prod.cascadia")
	assertTag(t, ev, "zone", "prod.cascadia")
	assertTag(t, ev, "backend", "fs-primary")
	assertTag(t, ev, "visibility", "internal")
	assertTag(t, ev, "t", "bahia")
	assertJSONField(t, ev.Content, "name", "prod.cascadia")
	assertJSONField(t, ev.Content, "visibility", "internal")
	assertJSONField(t, ev.Content, "backend_ref", "fs-primary")
	assertJSONField(t, ev.Content, "ttl", float64(60))
	assertJSONField(t, ev.Content, "deleted", false)
}

func TestDNSCanonicalPublisherTombstonesZone(t *testing.T) {
	ctx := context.Background()
	pub, sink := newTestPublisher(t, WithDNSZoneProjectionSource(&fakeDNSZoneProjectionSource{}))

	if err := pub.PublishZone(ctx, domain.DNSZone{Name: "prod.cascadia", BackendRef: "fs"}); err != nil {
		t.Fatalf("PublishZone: %v", err)
	}
	if err := pub.PublishZoneTombstone(ctx, "prod.cascadia"); err != nil {
		t.Fatalf("PublishZoneTombstone: %v", err)
	}

	events := sink.byKind(KindDNSZoneState)
	if len(events) != 2 {
		t.Fatalf("expected zone + tombstone, got %d", len(events))
	}
	assertTag(t, events[1], "deleted", "true")
	assertJSONField(t, events[1].Content, "deleted", true)
}

// --- backend publish ---

func TestDNSCanonicalPublisherPublishesBackend(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	pub, sink := newTestPublisher(t, WithDNSBackendProjectionSource(&fakeDNSBackendProjectionSource{}))

	backend := domain.DNSBackendState{
		Ref:       "fs-primary",
		Type:      domain.DNSBackendTypeFilesystem,
		Health:    domain.HealthStatusHealthy,
		ZoneRefs:  []string{"prod.cascadia"},
		UpdatedAt: now,
	}
	if err := pub.PublishBackend(ctx, backend); err != nil {
		t.Fatalf("PublishBackend: %v", err)
	}

	ev := assertOneSignedKind(t, sink, KindDNSBackendState)
	assertTag(t, ev, "d", "dnsbackend:fs-primary")
	assertTag(t, ev, "backend", "fs-primary")
	assertTag(t, ev, "type", "filesystem")
	assertTag(t, ev, "health", "healthy")
	assertTag(t, ev, "t", "bahia")
	assertJSONField(t, ev.Content, "ref", "fs-primary")
	assertJSONField(t, ev.Content, "type", "filesystem")
	assertJSONField(t, ev.Content, "health", "healthy")
	assertJSONField(t, ev.Content, "deleted", false)
}

// --- policy publish ---

func TestDNSCanonicalPublisherPublishesPolicy(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	policyID := uuid.New()
	zoneID := uuid.New()
	ttl := 120
	pub, sink := newTestPublisher(t, WithDNSPolicyProjectionSource(&fakeDNSPolicyProjectionSource{}))

	policy := domain.DNSPolicy{
		ID:     policyID,
		Name:   "latency-aware",
		ZoneID: &zoneID,
		Rules: []domain.DNSPolicyRule{{
			Match:  domain.DNSPolicyMatch{Environment: "prod"},
			Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityInternal, TTLOverride: &ttl},
		}},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := pub.PublishPolicy(ctx, policy); err != nil {
		t.Fatalf("PublishPolicy: %v", err)
	}

	ev := assertOneSignedKind(t, sink, KindDNSPolicyState)
	assertTag(t, ev, "d", "dnspolicy:"+policyID.String())
	assertTag(t, ev, "policy", policyID.String())
	assertTag(t, ev, "t", "bahia")
	assertJSONField(t, ev.Content, "id", policyID.String())
	assertJSONField(t, ev.Content, "name", "latency-aware")
	assertJSONField(t, ev.Content, "enabled", true)
	assertJSONField(t, ev.Content, "deleted", false)
}

// --- warm-start hydration ---

func TestDNSCanonicalPublisherHydrateFromStoreTombstonesOnRestart(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryNostrEventRepo()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop(), WithDNSProjectionSource(&fakeDNSProjectionSource{}))

	// First run: publish one endpoint via the canonical publisher.
	pub1 := NewDNSCanonicalPublisher(projector, zap.NewNop())
	endpoints := []domain.DNSEndpoint{testEndpoint("api")}
	if _, _, err := pub1.PublishEndpoints(ctx, endpoints); err != nil {
		t.Fatalf("first run PublishEndpoints: %v", err)
	}
	if len(sink.byKind(KindDNSEndpointState)) != 1 {
		t.Fatal("expected one published endpoint event")
	}

	// Simulate restart: new projector, new publisher, hydrate from store.
	projector2 := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop(), WithDNSProjectionSource(&fakeDNSProjectionSource{}))
	pub2 := NewDNSCanonicalPublisher(projector2, zap.NewNop())
	if err := pub2.HydrateFromStore(ctx); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}

	// Now publish with removed endpoint → should tombstone.
	published, tombstones, err := pub2.PublishEndpoints(ctx, nil)
	if err != nil {
		t.Fatalf("second run PublishEndpoints: %v", err)
	}
	if published != 0 || tombstones != 1 {
		t.Fatalf("got published=%d tombstones=%d, want 0/1", published, tombstones)
	}

	events := sink.byKind(KindDNSEndpointState)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (live + tombstone), got %d", len(events))
	}
	last := events[len(events)-1]
	assertTag(t, last, "deleted", "true")
	assertJSONField(t, last.Content, "deleted", true)
	assertJSONField(t, last.Content, "coordinate", "endpoint:service:api:prod")
}
