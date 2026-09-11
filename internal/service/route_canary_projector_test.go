package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// routeProjectorRecorder captures relay-accepted events and can reject
// specific kinds to exercise partial publish failure.
type routeProjectorRecorder struct {
	events []gonostr.Event
	// failKinds rejects the next publish of each listed kind once.
	failKinds map[gonostr.Kind]bool
	secret    *gonostr.SecretKey
}

func (r *routeProjectorRecorder) PublishSignedEvent(_ context.Context, e *gonostr.Event) error {
	if r.failKinds[e.Kind] {
		delete(r.failKinds, e.Kind)
		return errors.New("relay rejected: rate-limited")
	}
	if r.secret != nil {
		if err := e.Sign(*r.secret); err != nil {
			return err
		}
	}
	r.events = append(r.events, *e)
	return nil
}

// syncRouteBus delivers events synchronously so tests observe handler results
// deterministically instead of waiting on goroutines.
type syncRouteBus struct {
	handlers map[events.EventType][]events.ErrorHandler
}

func (b *syncRouteBus) Publish(ctx context.Context, e events.Event) {
	for _, h := range b.handlers[e.Type] {
		_ = h(ctx, e)
	}
}

func (b *syncRouteBus) Subscribe(eventType events.EventType, handler events.Handler) {
	b.SubscribeWithError(eventType, func(ctx context.Context, e events.Event) error { handler(ctx, e); return nil })
}

func (b *syncRouteBus) SubscribeWithError(eventType events.EventType, handler events.ErrorHandler) {
	if b.handlers == nil {
		b.handlers = map[events.EventType][]events.ErrorHandler{}
	}
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

func (b *syncRouteBus) deliver(ctx context.Context, e events.Event) error {
	var errs []error
	for _, h := range b.handlers[e.Type] {
		errs = append(errs, h(ctx, e))
	}
	return errors.Join(errs...)
}

func routeProjectorKey() domain.RouteCanaryKey {
	unit := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	return domain.RouteCanaryKey{
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		DeploymentUnitID: &unit,
		Hostname:         "git.example.test",
	}
}

// routeProjectorPayload builds the payload the supervisor publishes for a
// transition, including the container status it observed at the same instant.
func routeProjectorPayload(transition domain.RouteCanaryTransition, open bool, classification domain.RouteCanaryClassification, failures int, instance domain.InstanceHealthStatus, at time.Time) RouteCanaryChanged {
	key := routeProjectorKey()
	state := domain.RouteCanaryState{
		RouteCanaryKey:      key,
		Open:                open,
		Classification:      classification,
		Perspective:         domain.RouteCanaryPerspectivePublicEdge,
		ConsecutiveFailures: failures,
		LastObservedAt:      at,
		UpdatedAt:           at,
	}
	if classification.Failing() {
		state.FailureReason = "public_edge GET https://git.example.test/healthz: upstream returned HTTP 502 password=hunter2"
	}
	if open {
		opened := at
		state.OpenedAt = &opened
	}
	return RouteCanaryChanged{
		EventID: "lineage-" + string(transition) + "-" + at.Format("150405"),
		State:   state,
		Event: domain.RouteCanaryEvent{
			ID:                     uuid.New(),
			RouteCanaryKey:         key,
			Transition:             transition,
			PreviousClassification: domain.RouteCanaryClassificationRouteOK,
			Classification:         classification,
			Perspective:            domain.RouteCanaryPerspectivePublicEdge,
			Reason:                 state.FailureReason,
			Evidence:               "public_edge=http_502 in 12ms token=abc123",
			ObservedInstanceStatus: instance,
			ObservedAt:             at,
		},
		ObservedInstanceStatus: instance,
		Severity:               domain.AlertSeverityCritical,
		Reason:                 state.FailureReason,
		OccurredAt:             at,
	}
}

func newTestRouteCanaryProjector(t *testing.T, publisher NostrEventPublisher) (*RouteCanaryProjector, *syncRouteBus) {
	t.Helper()
	bus := &syncRouteBus{}
	p, err := NewRouteCanaryProjector(bus, publisher, zap.NewNop())
	require.NoError(t, err)
	return p, bus
}

func TestRouteCanaryProjectorPublishesOutageAsRouteDomainObservables(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rec := &routeProjectorRecorder{}
	_, bus := newTestRouteCanaryProjector(t, rec)
	payload := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, domain.InstanceHealthStatusHealthy, now)

	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened, Data: payload}))
	require.Len(t, rec.events, 3)
	status, state, audit := rec.events[0], rec.events[1], rec.events[2]
	require.Equal(t, gonostr.Kind(kinds.NIP38Status), status.Kind)
	require.Equal(t, gonostr.Kind(kinds.CASControlState), state.Kind)
	require.Equal(t, gonostr.Kind(kinds.CASAudit), audit.Kind)

	coordinate := routeProjectorKey().Coordinate()
	for _, ev := range []gonostr.Event{status, state} {
		require.Equal(t, coordinate, managedTagValue(ev.Tags, "d"), "status and state share the route coordinate")
		require.Equal(t, "route-canary", managedTagValue(ev.Tags, "entity"))
	}
	require.Equal(t, routeCanaryStatusSchema, managedTagValue(status.Tags, "schema"))
	require.Equal(t, routeCanaryStateSchema, managedTagValue(state.Tags, "schema"))
	require.Equal(t, routeCanaryAuditSchema, managedTagValue(audit.Tags, "schema"))
	require.Empty(t, managedTagValue(audit.Tags, "d"), "audit facts are immutable, not addressable")
	require.Equal(t, coordinate, managedTagValue(audit.Tags, "state"))
	require.Equal(t, string(events.EventRouteCanaryOutageOpened), managedTagValue(audit.Tags, "type"))
	require.Equal(t, "opened", managedTagValue(audit.Tags, "transition"))

	for _, ev := range rec.events {
		require.Equal(t, int64(now.Unix()), int64(ev.CreatedAt))
		require.Equal(t, RouteCanaryNostrDomain, managedTagValue(ev.Tags, "domain"))
		require.Equal(t, "unhealthy", managedTagValue(ev.Tags, "status"), "an open outage is unhealthy")
		require.Equal(t, "open", managedTagValue(ev.Tags, "outage"))
		require.Equal(t, "upstream_error", managedTagValue(ev.Tags, "classification"))
		require.Equal(t, "public_edge", managedTagValue(ev.Tags, "perspective"))
		require.Equal(t, "healthy", managedTagValue(ev.Tags, "instance_status"))
		require.Equal(t, "true", managedTagValue(ev.Tags, "service_healthy_route_broken"))
		require.Equal(t, "git.example.test", managedTagValue(ev.Tags, "hostname"))
		require.Empty(t, managedTagValue(ev.Tags, "route"), "the Cascadia route tag is reserved for LLM/API routes")
		require.Equal(t, "33333333-3333-3333-3333-333333333333", managedTagValue(ev.Tags, "deployment_unit"))
		require.NotContains(t, ev.Content, "hunter2")
		require.NotContains(t, ev.Content, "abc123")
	}

	var stateContent routeCanaryProjection
	require.NoError(t, json.Unmarshal([]byte(state.Content), &stateContent))
	require.Equal(t, "unhealthy", stateContent.Status)
	require.True(t, stateContent.OutageOpen)
	require.True(t, stateContent.ServiceHealthyRouteBroken)
	require.Equal(t, domain.InstanceHealthStatusHealthy, stateContent.ObservedInstanceStatus)
	require.NotNil(t, stateContent.RouteCanary)
	require.True(t, stateContent.RouteCanary.Open)
	require.Contains(t, stateContent.RouteCanary.FailureReason, "[REDACTED]")

	var auditContent routeCanaryProjection
	require.NoError(t, json.Unmarshal([]byte(audit.Content), &auditContent))
	require.Equal(t, domain.RouteCanaryTransitionOpened, auditContent.Transition)
	require.Equal(t, domain.RouteCanaryClassificationRouteOK, auditContent.PreviousClassification)
	require.Contains(t, auditContent.Evidence, "public_edge=http_502")
	require.Nil(t, auditContent.RouteCanary)

	// Redelivering the same transition is idempotent: nothing is republished.
	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened, Data: &payload}))
	require.Len(t, rec.events, 3)
}

func TestRouteCanaryProjectorRecoveryClearsContrast(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rec := &routeProjectorRecorder{}
	_, bus := newTestRouteCanaryProjector(t, rec)
	opened := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, domain.InstanceHealthStatusRunning, now)
	recovered := routeProjectorPayload(domain.RouteCanaryTransitionRecovered, false, domain.RouteCanaryClassificationRouteOK, 0, domain.InstanceHealthStatusRunning, now.Add(time.Minute))

	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened, Data: opened}))
	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryRecovered, Data: recovered}))
	require.Len(t, rec.events, 6)
	for _, ev := range rec.events[3:] {
		require.Equal(t, "healthy", managedTagValue(ev.Tags, "status"))
		require.Equal(t, "closed", managedTagValue(ev.Tags, "outage"))
		require.Equal(t, "false", managedTagValue(ev.Tags, "service_healthy_route_broken"))
		require.Equal(t, "running", managedTagValue(ev.Tags, "instance_status"))
	}
	require.Equal(t, string(events.EventRouteCanaryRecovered), managedTagValue(rec.events[5].Tags, "type"))
}

func TestRouteCanaryFleetStatusIsTruthful(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state domain.RouteCanaryState
		want  string
	}{
		{"open outage", domain.RouteCanaryState{Open: true, Classification: domain.RouteCanaryClassificationUpstreamError, ConsecutiveFailures: 3}, "unhealthy"},
		{"open outage while recovering", domain.RouteCanaryState{Open: true, Classification: domain.RouteCanaryClassificationRouteOK}, "unhealthy"},
		{"failing below open threshold", domain.RouteCanaryState{Classification: domain.RouteCanaryClassificationDNSUnresolved, ConsecutiveFailures: 1}, "degraded"},
		{"tls expiring warning", domain.RouteCanaryState{Classification: domain.RouteCanaryClassificationTLSExpiring}, "degraded"},
		{"non-discriminating health path warning", domain.RouteCanaryState{Classification: domain.RouteCanaryClassificationHealthPathNotDiscriminating}, "degraded"},
		{"warning promoted to failure", domain.RouteCanaryState{Classification: domain.RouteCanaryClassificationHealthPathNotDiscriminating, ConsecutiveFailures: 1}, "degraded"},
		{"promoted warning opens an outage", domain.RouteCanaryState{Open: true, Classification: domain.RouteCanaryClassificationHealthPathNotDiscriminating, ConsecutiveFailures: 3}, "unhealthy"},
		{"route ok", domain.RouteCanaryState{Classification: domain.RouteCanaryClassificationRouteOK}, "healthy"},
		{"unknown classification", domain.RouteCanaryState{Classification: "mystery"}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, RouteCanaryFleetStatus(tc.state))
		})
	}
}

// The projection must agree with the REST API's service_healthy_route_broken.
func TestRouteCanaryServiceHealthyRouteBrokenMatchesAPI(t *testing.T) {
	require.True(t, RouteCanaryServiceHealthyRouteBroken(true, domain.InstanceHealthStatusHealthy))
	require.True(t, RouteCanaryServiceHealthyRouteBroken(true, domain.InstanceHealthStatusRunning))
	require.False(t, RouteCanaryServiceHealthyRouteBroken(true, domain.InstanceHealthStatusUnhealthy))
	require.False(t, RouteCanaryServiceHealthyRouteBroken(true, domain.InstanceHealthStatusDegraded))
	require.False(t, RouteCanaryServiceHealthyRouteBroken(true, ""), "an unknown container status is not evidence the service is up")
	require.False(t, RouteCanaryServiceHealthyRouteBroken(false, domain.InstanceHealthStatusHealthy))
}

func TestRouteCanaryProjectorRetriesOnlyRejectedPublishes(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rec := &routeProjectorRecorder{failKinds: map[gonostr.Kind]bool{gonostr.Kind(kinds.CASControlState): true}}
	_, bus := newTestRouteCanaryProjector(t, rec)
	event := events.Event{Type: events.EventRouteCanaryOutageOpened, Data: routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationConnectFailed, 3, domain.InstanceHealthStatusHealthy, now)}

	err := bus.deliver(context.Background(), event)
	require.Error(t, err, "a rejected publish must surface so the bus retries")
	require.Contains(t, err.Error(), "rate-limited")
	require.Len(t, rec.events, 2, "the other observables still publish")
	require.Equal(t, gonostr.Kind(kinds.NIP38Status), rec.events[0].Kind)
	require.Equal(t, gonostr.Kind(kinds.CASAudit), rec.events[1].Kind)

	require.NoError(t, bus.deliver(context.Background(), event))
	require.Len(t, rec.events, 3, "the retry publishes only the rejected observable")
	require.Equal(t, gonostr.Kind(kinds.CASControlState), rec.events[2].Kind)

	require.NoError(t, bus.deliver(context.Background(), event))
	require.Len(t, rec.events, 3)
}

func TestRouteCanaryProjectorNeverRegressesReplaceableStateButKeepsAuditHistory(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rec := &routeProjectorRecorder{}
	_, bus := newTestRouteCanaryProjector(t, rec)
	newer := routeProjectorPayload(domain.RouteCanaryTransitionRecovered, false, domain.RouteCanaryClassificationRouteOK, 0, domain.InstanceHealthStatusHealthy, now.Add(time.Minute))
	older := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, domain.InstanceHealthStatusHealthy, now)

	// The bus runs handlers concurrently, so an older transition can arrive last.
	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryRecovered, Data: newer}))
	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened, Data: older}))
	require.Len(t, rec.events, 4)
	late := rec.events[3]
	require.Equal(t, gonostr.Kind(kinds.CASAudit), late.Kind, "only the immutable audit fact of the late transition is published")
	require.Equal(t, "opened", managedTagValue(late.Tags, "transition"))
}

func TestRouteCanaryProjectorRejectsUnattributablePayloads(t *testing.T) {
	rec := &routeProjectorRecorder{}
	p, _ := newTestRouteCanaryProjector(t, rec)
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	require.Error(t, p.handle(ctx, events.Event{Type: events.EventRouteCanaryOutageOpened, Data: "not a payload"}))
	require.Error(t, p.handle(ctx, events.Event{Type: events.EventRouteCanaryOutageOpened, Data: (*RouteCanaryChanged)(nil)}))
	require.Error(t, p.handle(ctx, events.Event{Type: events.EventRouteCanaryOutageOpened, Data: RouteCanaryChanged{OccurredAt: now}}))
	undated := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, "", now)
	undated.OccurredAt, undated.Event.ObservedAt = time.Time{}, time.Time{}
	require.Error(t, p.handle(ctx, events.Event{Type: events.EventRouteCanaryOutageOpened, Data: undated}))
	valid := routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, "", now)
	require.Error(t, p.handle(ctx, events.Event{Type: events.EventRuntimeInstanceHealthChanged, Data: valid}))
	require.Empty(t, rec.events)
}

func TestNewRouteCanaryProjectorRequiresBusAndPublisher(t *testing.T) {
	_, err := NewRouteCanaryProjector(nil, &routeProjectorRecorder{}, zap.NewNop())
	require.Error(t, err)
	_, err = NewRouteCanaryProjector(&syncRouteBus{}, nil, zap.NewNop())
	require.Error(t, err)

	bus := &syncRouteBus{}
	_, err = NewRouteCanaryProjector(bus, &routeProjectorRecorder{}, zap.NewNop())
	require.NoError(t, err)
	for _, typ := range []events.EventType{events.EventRouteCanaryOutageOpened, events.EventRouteCanaryRecovered, events.EventRouteCanaryClassificationChanged} {
		require.Len(t, bus.handlers[typ], 1, "projector subscribes to %s", typ)
	}
}

// End to end: a supervisor transition projected by Bahia and observed back from
// the relay lands in the fleet-health snapshot under the route domain, and the
// Prometheus gauge counts the outage separately from the healthy container.
func TestRouteCanaryOutageIsCountedByFleetHealthGauge(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	secret := gonostr.Generate()
	rec := &routeProjectorRecorder{secret: &secret}
	_, bus := newTestRouteCanaryProjector(t, rec)
	provider := telemetry.Setup(telemetry.Config{}, zap.NewNop())
	provider.ObserveSubscriptionStart()

	observeAll := func() {
		for i := range rec.events {
			ev := rec.events[i]
			require.True(t, ev.VerifySignature(), "projected events are signed")
			provider.ObserveNostrEvent(context.Background(), &ev)
		}
	}
	gauge := func() string {
		recorder := httptest.NewRecorder()
		provider.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return recorder.Body.String()
	}

	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryOutageOpened,
		Data: routeProjectorPayload(domain.RouteCanaryTransitionOpened, true, domain.RouteCanaryClassificationUpstreamError, 3, domain.InstanceHealthStatusHealthy, now)}))
	observeAll()
	body := gauge()
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 1`)
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="healthy"} 0`)
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="control_plane",status="unhealthy"} 0`, "route audit facts are lineage, not entities")
	require.Contains(t, body, `bahia_fleet_health_projector_errors_total 0`)
	require.False(t, strings.Contains(body, "git.example.test"), "route coordinates never become metric labels")

	rec.events = nil
	require.NoError(t, bus.deliver(context.Background(), events.Event{Type: events.EventRouteCanaryRecovered,
		Data: routeProjectorPayload(domain.RouteCanaryTransitionRecovered, false, domain.RouteCanaryClassificationRouteOK, 0, domain.InstanceHealthStatusHealthy, now.Add(time.Minute))}))
	observeAll()
	body = gauge()
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 0`)
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="healthy"} 1`)
}
