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
