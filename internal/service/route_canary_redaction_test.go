package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
)

func TestRouteCanaryProjectorRedactsInternalLANAddressesOnlyOnRelay(t *testing.T) {
	rec := &routeProjectorRecorder{}
	_, bus := newTestRouteCanaryProjector(t, rec)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	payload := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationConnectFailed, 3, domain.InstanceHealthStatusHealthy, now)
	const reason = "internal_lan GET https://git.example.test/healthz: no HTTP response: dial tcp 192.168.40.10:443: connection refused"
	const want = "internal_lan GET https://git.example.test/healthz: no HTTP response: dial tcp [REDACTED_ADDRESS]: connection refused"
	payload.State.Perspective = domain.RouteCanaryPerspectiveInternalLAN
	payload.Event.Perspective = domain.RouteCanaryPerspectiveInternalLAN
	payload.State.FailureReason = reason
	payload.Event.Reason = reason
	payload.Reason = reason
	payload.Event.Evidence = reason + "; lookup origin.internal:443 on [fd00::53]:53: timeout token=canary-secret"
	before, err := json.Marshal(payload)
	require.NoError(t, err)

	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened, Data: &payload}))
	require.Len(t, rec.events, 3)
	for _, event := range rec.events {
		wire, err := json.Marshal(event)
		require.NoError(t, err)
		for _, private := range []string{"192.168.40.10", "origin.internal:443", "fd00::53", "canary-secret"} {
			require.NotContains(t, string(wire), private, "content and tags of kind %d", event.Kind)
		}
		var content routeCanaryProjection
		require.NoError(t, json.Unmarshal([]byte(event.Content), &content))
		require.Equal(t, want, content.Reason)
		require.Equal(t, domain.RouteCanaryPerspectiveInternalLAN, content.Perspective)
		require.Equal(t, domain.RouteCanaryClassificationConnectFailed, content.Classification)
		if content.RouteCanary != nil {
			require.Equal(t, want, content.RouteCanary.FailureReason)
		}
		if content.Evidence != "" {
			require.Contains(t, content.Evidence, "connection refused")
			require.Contains(t, content.Evidence, "timeout")
		}
	}
	// The state/event objects used by persistence and REST retain operator detail.
	after, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	require.Contains(t, payload.State.FailureReason, "192.168.40.10:443")
}
