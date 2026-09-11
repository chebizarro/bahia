package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

func TestNostrFleetHealthProjectsCanonicalObservables(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	provider := Setup(Config{}, zap.NewNop())
	provider.now = func() time.Time { return now }
	provider.ObserveSubscriptionStart()

	for _, tc := range []struct {
		kind   int
		d      string
		domain string
		status string
	}{
		{kinds.NIP38Status, "runtime:a", "runtime", "healthy"},
		{kinds.AssistantTranscript, "agent:a", "agent", "active"},
		{kinds.SoulFactoryRuntimeCapability, "agent:b", "agent", "degraded"},
		{kinds.CASControlState, "service:a", "service", "failed"},
		{kinds.CASAudit, "audit:a", "control_plane", "succeeded"},
	} {
		provider.ObserveNostrEvent(context.Background(), &gonostr.Event{
			Kind: gonostr.Kind(tc.kind), CreatedAt: gonostr.Timestamp(100),
			Tags: gonostr.Tags{{"d", tc.d}, {"domain", tc.domain}, {"status", tc.status}},
		})
	}
	provider.ObserveEOSE()

	snapshot := provider.nostrFleetHealth.snapshot(now)
	if !snapshot.SubscriptionActive || !snapshot.CaughtUp {
		t.Fatalf("subscription state = active:%v caught-up:%v", snapshot.SubscriptionActive, snapshot.CaughtUp)
	}
	for key, want := range map[string]int{
		"runtime:healthy": 1, "agent:healthy": 1, "agent:degraded": 1,
		"service:unhealthy": 1, "control_plane:healthy": 1,
	} {
		if got := snapshot.Entities[key]; got != want {
			t.Fatalf("entities[%q] = %d, want %d", key, got, want)
		}
	}
	if len(snapshot.HeartbeatLagSeconds) != 1 {
		t.Fatalf("heartbeat pubkeys = %d, want 1", len(snapshot.HeartbeatLagSeconds))
	}
}

func TestNostrFleetHealthKeepsLatestReplaceableAndSeparatesRelayFailure(t *testing.T) {
	now := time.Unix(300, 0).UTC()
	projector := newNostrFleetHealthProjector(func() time.Time { return now })
	newer := &gonostr.Event{Kind: gonostr.Kind(kinds.NIP38Status), CreatedAt: 200, Tags: gonostr.Tags{{"d", "runtime:a"}, {"status", "healthy"}}}
	older := &gonostr.Event{Kind: gonostr.Kind(kinds.NIP38Status), CreatedAt: 100, Tags: gonostr.Tags{{"d", "runtime:a"}, {"status", "failed"}}}
	projector.observeSubscriptionStart()
	projector.observeEvent(context.Background(), newer)
	projector.observeEvent(context.Background(), older)
	projector.observeRelayClosed()
	projector.observeSubscriptionEnd()

	snapshot := projector.snapshot(now)
	if snapshot.Entities["runtime:healthy"] != 1 || snapshot.Entities["runtime:unhealthy"] != 0 {
		t.Fatalf("stale replaceable event changed subject state: %#v", snapshot.Entities)
	}
	if snapshot.SubscriptionActive || snapshot.CaughtUp || snapshot.RelayClosedTotal != 1 {
		t.Fatalf("relay failure not isolated from subject state: %#v", snapshot)
	}
}

func TestNostrFleetHealthMetricsUseBoundedLabelsAndNoRawContent(t *testing.T) {
	provider := Setup(Config{}, zap.NewNop())
	provider.now = func() time.Time { return time.Unix(200, 0).UTC() }
	provider.ObserveNostrEvent(context.Background(), &gonostr.Event{
		Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: 100,
		Tags:    gonostr.Tags{{"d", "service:a"}, {"domain", "attacker-domain"}, {"status", "attacker-status"}},
		Content: `{"secret":"must-not-be-exported"}`,
	})
	provider.ObserveNostrEvent(context.Background(), &gonostr.Event{
		Kind: gonostr.Kind(kinds.AssistantTranscript), CreatedAt: 100,
		Tags: gonostr.Tags{{"d", "private-agent-coordinate"}, {"status", "active"}},
	})
	recorder := httptest.NewRecorder()
	provider.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, forbidden := range []string{"attacker-domain", "attacker-status", "private-agent-coordinate", "must-not-be-exported", "secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics leaked unbounded/raw value %q", forbidden)
		}
	}
	if !strings.Contains(body, `bahia_fleet_health_nostr_entities{domain="service",status="unknown"} 1`) {
		t.Fatalf("missing bounded fallback metric:\n%s", body)
	}
}

func TestNostrFleetHealthEmptyTimestampsRenderAsZero(t *testing.T) {
	provider := Setup(Config{}, zap.NewNop())
	recorder := httptest.NewRecorder()
	provider.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "bahia_fleet_health_projector_last_event_timestamp_seconds 0") {
		t.Fatalf("zero timestamp did not render as zero:\n%s", recorder.Body.String())
	}
}

// Route outages are a distinct fleet-health domain: one entity per managed-route
// coordinate, counted by the Prometheus gauge separately from the runtime
// container behind the route.
func TestNostrFleetHealthCountsRouteOutagesAsDistinctDomain(t *testing.T) {
	provider := Setup(Config{}, zap.NewNop())
	now := time.Unix(300, 0).UTC()
	provider.now = func() time.Time { return now }
	observe := func(kind int, createdAt int64, tags gonostr.Tags) {
		t.Helper()
		provider.ObserveNostrEvent(context.Background(), &gonostr.Event{Kind: gonostr.Kind(kind), CreatedAt: gonostr.Timestamp(createdAt), Tags: tags})
	}
	broken := "route:svc:env:none:git.example.test"
	// Status and state observables for one route share a coordinate, so they are
	// one entity rather than two.
	observe(kinds.NIP38Status, 100, gonostr.Tags{{"d", broken}, {"domain", "route"}, {"status", "unhealthy"}})
	observe(kinds.CASControlState, 100, gonostr.Tags{{"d", broken}, {"domain", "route"}, {"status", "unhealthy"}})
	observe(kinds.NIP38Status, 100, gonostr.Tags{{"d", "route:svc:env:none:expiring.example.test"}, {"domain", "route"}, {"status", "degraded"}})
	observe(kinds.CASControlState, 100, gonostr.Tags{{"d", "route:svc:env:none:ok.example.test"}, {"domain", "route"}, {"status", "healthy"}})
	// The container behind the broken route is healthy; it stays in its own domain.
	observe(kinds.NIP38Status, 100, gonostr.Tags{{"d", "runtime:instance:svc"}, {"domain", "runtime"}, {"status", "healthy"}})
	// A route audit fact is lineage, not an additional route.
	observe(kinds.CASAudit, 100, gonostr.Tags{{"domain", "route"}, {"type", "route.canary_outage_opened"}, {"state", broken}, {"status", "unhealthy"}})

	snapshot := provider.nostrFleetHealth.snapshot(now)
	for key, want := range map[string]int{
		"route:unhealthy": 1, "route:degraded": 1, "route:healthy": 1, "route:unknown": 0,
		"runtime:healthy": 1, "control_plane:unhealthy": 0, "control_plane:unknown": 0,
	} {
		if got := snapshot.Entities[key]; got != want {
			t.Fatalf("entities[%q] = %d, want %d (all: %#v)", key, got, want, snapshot.Entities)
		}
	}
	if snapshot.ProjectionErrors != 0 {
		t.Fatalf("route audit lineage counted as projection error: %d", snapshot.ProjectionErrors)
	}

	recorder := httptest.NewRecorder()
	provider.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 1`,
		`bahia_fleet_health_nostr_entities{domain="route",status="degraded"} 1`,
		`bahia_fleet_health_nostr_entities{domain="route",status="healthy"} 1`,
		`bahia_fleet_health_nostr_entities{domain="route",status="unknown"} 0`,
		`bahia_fleet_health_nostr_entities{domain="runtime",status="healthy"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in metrics:\n%s", want, body)
		}
	}
	if strings.Contains(body, "git.example.test") {
		t.Fatalf("route coordinate leaked into metric labels:\n%s", body)
	}

	// Recovery replaces the route's state; it does not add a second entity.
	observe(kinds.NIP38Status, 200, gonostr.Tags{{"d", broken}, {"domain", "route"}, {"status", "healthy"}})
	snapshot = provider.nostrFleetHealth.snapshot(now)
	if snapshot.Entities["route:unhealthy"] != 0 || snapshot.Entities["route:healthy"] != 2 {
		t.Fatalf("recovered route not reflected: %#v", snapshot.Entities)
	}
}

func TestNostrFleetHealthRejectsRouteObservableWithoutCoordinate(t *testing.T) {
	projector := newNostrFleetHealthProjector(func() time.Time { return time.Unix(300, 0).UTC() })
	projector.observeEvent(context.Background(), &gonostr.Event{
		Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: 100,
		Tags: gonostr.Tags{{"domain", "route"}, {"status", "unhealthy"}},
	})
	snapshot := projector.snapshot(time.Unix(300, 0).UTC())
	if snapshot.Entities["route:unhealthy"] != 0 {
		t.Fatalf("unattributable route observable counted as a route: %#v", snapshot.Entities)
	}
	if snapshot.ProjectionErrors != 1 {
		t.Fatalf("projection errors = %d, want 1", snapshot.ProjectionErrors)
	}
}
