package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/adapters/telemetry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// recordingRouteBus is the synchronous in-test bus that also keeps every
// published event, so a test can assert what the gate and the supervisor
// announced and what the projector made of it without waiting on goroutines.
type recordingRouteBus struct {
	syncRouteBus
	published []events.Event
	// publishCtxErr is ctx.Err() observed at each Publish call.
	publishCtxErr []error
}

func (b *recordingRouteBus) Publish(ctx context.Context, e events.Event) {
	b.published = append(b.published, e)
	b.publishCtxErr = append(b.publishCtxErr, ctx.Err())
	b.syncRouteBus.Publish(ctx, e)
}

func (b *recordingRouteBus) ofType(typ events.EventType) []events.Event {
	var out []events.Event
	for _, e := range b.published {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// routeProjectionHarness wires the real projector and fleet-health telemetry to
// a recording bus: exactly the path a production transition takes to Nostr and
// to bahia_fleet_health_nostr_entities.
type routeProjectionHarness struct {
	t        *testing.T
	bus      *recordingRouteBus
	relay    *routeProjectorRecorder
	provider *telemetry.Provider
	observed int
}

func newRouteProjectionHarness(t *testing.T) *routeProjectionHarness {
	t.Helper()
	secret := gonostr.Generate()
	h := &routeProjectionHarness{
		t:        t,
		bus:      &recordingRouteBus{},
		relay:    &routeProjectorRecorder{secret: &secret},
		provider: telemetry.Setup(telemetry.Config{}, zap.NewNop()),
	}
	_, err := NewRouteCanaryProjector(h.bus, h.relay, zap.NewNop())
	require.NoError(t, err)
	h.provider.ObserveSubscriptionStart()
	return h
}

// observe feeds relay-accepted events not yet seen to fleet-health telemetry,
// as the relay echo would, and returns them.
func (h *routeProjectionHarness) observe() []gonostr.Event {
	fresh := append([]gonostr.Event(nil), h.relay.events[h.observed:]...)
	for i := range fresh {
		require.True(h.t, fresh[i].VerifySignature())
		h.provider.ObserveNostrEvent(context.Background(), &fresh[i])
	}
	h.observed = len(h.relay.events)
	return fresh
}

func (h *routeProjectionHarness) metrics() string {
	recorder := httptest.NewRecorder()
	h.provider.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

// replaceableStatus returns the status/outage tags of the 30315 and 30900
// observables in a batch, which must agree.
func replaceableStatus(t *testing.T, batch []gonostr.Event) (status, outage string) {
	t.Helper()
	var seen int
	for _, ev := range batch {
		if ev.Kind != gonostr.Kind(kinds.NIP38Status) && ev.Kind != gonostr.Kind(kinds.CASControlState) {
			continue
		}
		seen++
		if status == "" {
			status, outage = managedTagValue(ev.Tags, "status"), managedTagValue(ev.Tags, "outage")
			continue
		}
		require.Equal(t, status, managedTagValue(ev.Tags, "status"))
		require.Equal(t, outage, managedTagValue(ev.Tags, "outage"))
	}
	require.Equal(t, 2, seen, "a transition projects both 30315 status and 30900 state")
	return status, outage
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func newPublishingTestGate(t *testing.T, prober RouteProber, applier RouteApplier, repo RouteCanaryRepository, health RouteInstanceHealthSource, bus events.Publisher, at time.Time) *RouteCanaryGate {
	t.Helper()
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	require.NoError(t, err)
	gate, err := NewRouteCanaryGate(applier, evaluator, repo, health, bus, RouteCanaryGateConfig{
		Timeout:       50 * time.Millisecond,
		RetryInterval: 5 * time.Millisecond,
	}, nil)
	require.NoError(t, err)
	gate.now = fixedClock(at)
	return gate
}

// TestGateRecoveryClearsSupervisorOutageInNostrProjection reproduces the
// review's failure sequence end to end: the supervisor opens an outage and it
// is projected; an operator deploys a fix and the gate passes; later sweeps see
// a closed route and report nothing. Before the gate published transitions,
// Nostr and the fleet-health gauge stayed stuck on the outage forever and no
// route.canary_recovered was ever announced.
func TestGateRecoveryClearsSupervisorOutageInNostrProjection(t *testing.T) {
	t0 := time.Now().UTC().Truncate(time.Second)
	h := newRouteProjectionHarness(t)
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	repo := newMemoryRouteCanaryRepo()
	health := staticHealthSource{status: domain.InstanceHealthStatusHealthy, ok: true}
	supervisor := newTestSupervisor(t, prober, repo, health, h.bus)
	supervisor.now = fixedClock(t0)
	ctx := context.Background()
	key := domain.RouteCanaryKeyForPlan(testRoutePlan())

	// 1. The supervisor sees the failure below its threshold (degraded), then
	// opens the outage (failure threshold 2), and both are projected.
	supervisor.EvaluateOnce(ctx)
	status, outage := replaceableStatus(t, h.observe())
	require.Equal(t, "degraded", status)
	require.Equal(t, "closed", outage)
	supervisor.now = fixedClock(t0.Add(30 * time.Second))
	supervisor.EvaluateOnce(ctx)
	require.Len(t, h.bus.ofType(events.EventRouteCanaryOutageOpened), 1)
	status, outage = replaceableStatus(t, h.observe())
	require.Equal(t, "unhealthy", status)
	require.Equal(t, "open", outage)
	require.Contains(t, h.metrics(), `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 1`)

	// 2. A fix is deployed and the post-deploy gate passes.
	prober.byPerspective[domain.RouteCanaryPerspectivePublicEdge] = healthyObservation()
	applier := &stubRouteApplier{}
	gate := newPublishingTestGate(t, prober, applier, repo, health, h.bus, t0.Add(time.Minute))
	require.NoError(t, gate.Apply(ctx, testRoutePlan()))
	require.Zero(t, applier.compensated)

	state, err := repo.GetState(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.False(t, state.Open, "the gate recovered the outage durably")

	recovered := h.bus.ofType(events.EventRouteCanaryRecovered)
	require.Len(t, recovered, 1, "the gate announces route.canary_recovered")
	payload, ok := recovered[0].Data.(RouteCanaryChanged)
	require.True(t, ok, "payload type %T", recovered[0].Data)
	require.Equal(t, domain.AlertSeverityInfo, payload.Severity)
	require.Equal(t, domain.RouteCanaryTransitionRecovered, payload.Event.Transition)
	require.Equal(t, key.Coordinate(), recovered[0].EntityID)
	require.Equal(t, t0.Add(time.Minute), payload.OccurredAt)

	batch := h.observe()
	require.Len(t, batch, 3, "30315, 30900 and a 4903 audit for the recovery")
	status, outage = replaceableStatus(t, batch)
	require.Equal(t, "healthy", status)
	require.Equal(t, "closed", outage)
	body := h.metrics()
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 0`)
	require.Contains(t, body, `bahia_fleet_health_nostr_entities{domain="route",status="healthy"} 1`)

	// 3. Later sweeps see a closed route_ok and report no transition; the
	// projection is already correct, so nothing further is published.
	supervisor.now = fixedClock(t0.Add(2 * time.Minute))
	published := len(h.bus.published)
	supervisor.EvaluateOnce(ctx)
	require.Len(t, h.bus.published, published, "a steady healthy route announces nothing")
	require.Empty(t, h.observe())
	require.Contains(t, h.metrics(), `bahia_fleet_health_nostr_entities{domain="route",status="healthy"} 1`)
}

// TestGateOpenedOutageIsPublishedProjectedAndRolledBack proves a gate-declared
// outage reaches Nostr immediately, and stays projected while the supervisor
// keeps observing the same failure without a transition of its own.
func TestGateOpenedOutageIsPublishedProjectedAndRolledBack(t *testing.T) {
	t0 := time.Now().UTC().Truncate(time.Second)
	h := newRouteProjectionHarness(t)
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	repo := newMemoryRouteCanaryRepo()
	health := staticHealthSource{status: domain.InstanceHealthStatusHealthy, ok: true}
	applier := &stubRouteApplier{}
	gate := newPublishingTestGate(t, prober, applier, repo, health, h.bus, t0)
	ctx := context.Background()

	err := gate.Apply(ctx, testRoutePlan())
	require.ErrorIs(t, err, ErrRouteCanaryGateFailed)
	require.Equal(t, 1, applier.compensated, "the broken route is still withdrawn")

	opened := h.bus.ofType(events.EventRouteCanaryOutageOpened)
	require.Len(t, opened, 1, "the gate announces the outage it opened")
	payload, ok := opened[0].Data.(RouteCanaryChanged)
	require.True(t, ok, "payload type %T", opened[0].Data)
	require.Equal(t, domain.AlertSeverityCritical, payload.Severity)
	require.True(t, payload.State.Open)
	require.Equal(t, domain.RouteCanaryClassificationUpstreamError, payload.State.Classification)
	require.Equal(t, domain.InstanceHealthStatusHealthy, payload.ObservedInstanceStatus)

	batch := h.observe()
	require.Len(t, batch, 3)
	status, outage := replaceableStatus(t, batch)
	require.Equal(t, "unhealthy", status)
	require.Equal(t, "open", outage)
	for _, ev := range batch {
		require.Equal(t, "true", managedTagValue(ev.Tags, "service_healthy_route_broken"))
	}
	require.Contains(t, h.metrics(), `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 1`)

	// The supervisor then sees the same failure on an already-open outage and
	// reports no transition; the projection must already show the outage.
	supervisor := newTestSupervisor(t, prober, repo, health, h.bus)
	supervisor.now = fixedClock(t0.Add(time.Minute))
	published := len(h.bus.published)
	supervisor.EvaluateOnce(ctx)
	require.Len(t, h.bus.published, published)
	require.Contains(t, h.metrics(), `bahia_fleet_health_nostr_entities{domain="route",status="unhealthy"} 1`)
}

// TestGateAndSupervisorPublishTheCanonicalTransitionPayload proves both
// observers announce exactly the event the shared builder derives from the
// state and lineage they persisted: same type, severity, payload and entity.
func TestGateAndSupervisorPublishTheCanonicalTransitionPayload(t *testing.T) {
	t0 := time.Now().UTC().Truncate(time.Second)
	health := staticHealthSource{status: domain.InstanceHealthStatusRunning, ok: true}
	key := domain.RouteCanaryKeyForPlan(testRoutePlan())
	ctx := context.Background()

	canonical := func(t *testing.T, repo *memoryRouteCanaryRepo) events.Event {
		t.Helper()
		state, err := repo.GetState(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, state)
		lineage := repo.events[len(repo.events)-1]
		want, ok := routeCanaryTransitionEvent(*state, lineage, lineage.ObservedInstanceStatus, lineage.ObservedAt)
		require.True(t, ok)
		return want
	}

	t.Run("supervisor", func(t *testing.T) {
		bus := &recordingRouteBus{}
		repo := newMemoryRouteCanaryRepo()
		prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
			domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
		}}
		supervisor := newTestSupervisor(t, prober, repo, health, bus)
		supervisor.now = fixedClock(t0)
		supervisor.EvaluateOnce(ctx)
		supervisor.EvaluateOnce(ctx)
		opened := bus.published[len(bus.published)-1]
		require.Equal(t, events.EventRouteCanaryOutageOpened, opened.Type)
		require.True(t, reflect.DeepEqual(canonical(t, repo), opened), "supervisor published %+v", opened)
	})

	for _, tc := range []struct {
		name        string
		prior       *domain.RouteCanaryState
		observation domain.RouteCanaryObservation
		wantType    events.EventType
		wantSev     domain.AlertSeverity
	}{
		{"gate opens", nil, staleUpstreamObservation(), events.EventRouteCanaryOutageOpened, domain.AlertSeverityCritical},
		{"gate recovers", openRouteState(key, t0.Add(-time.Hour)), healthyObservation(), events.EventRouteCanaryRecovered, domain.AlertSeverityInfo},
		{"gate reclassifies an open outage", openRouteState(key, t0.Add(-time.Hour)), statusMismatchObservation(404), events.EventRouteCanaryClassificationChanged, domain.AlertSeverityError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := &recordingRouteBus{}
			repo := newMemoryRouteCanaryRepo()
			if tc.prior != nil {
				require.NoError(t, repo.UpsertState(ctx, tc.prior))
			}
			prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
				domain.RouteCanaryPerspectivePublicEdge: tc.observation,
			}}
			gate := newPublishingTestGate(t, prober, &stubRouteApplier{}, repo, health, bus, t0)
			_ = gate.Apply(ctx, testRoutePlan())
			require.Len(t, bus.published, 1)
			got := bus.published[0]
			require.Equal(t, tc.wantType, got.Type)
			require.Equal(t, tc.wantSev, got.Data.(RouteCanaryChanged).Severity)
			require.True(t, reflect.DeepEqual(canonical(t, repo), got), "gate published %+v", got)
		})
	}
}

func openRouteState(key domain.RouteCanaryKey, openedAt time.Time) *domain.RouteCanaryState {
	opened := openedAt
	return &domain.RouteCanaryState{
		RouteCanaryKey:      key,
		Open:                true,
		Classification:      domain.RouteCanaryClassificationUpstreamError,
		Perspective:         domain.RouteCanaryPerspectivePublicEdge,
		ConsecutiveFailures: 3,
		FailureReason:       "upstream returned HTTP 502",
		LastObservedAt:      openedAt,
		OpenedAt:            &opened,
	}
}

func TestRouteCanaryTransitionEventMapsEveryTransition(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	key := domain.RouteCanaryKeyForPlan(testRoutePlan())
	for _, tc := range []struct {
		transition domain.RouteCanaryTransition
		open       bool
		wantType   events.EventType
		wantSev    domain.AlertSeverity
		wantOK     bool
	}{
		{domain.RouteCanaryTransitionOpened, true, events.EventRouteCanaryOutageOpened, domain.AlertSeverityCritical, true},
		{domain.RouteCanaryTransitionRecovered, false, events.EventRouteCanaryRecovered, domain.AlertSeverityInfo, true},
		{domain.RouteCanaryTransitionClassificationChanged, true, events.EventRouteCanaryClassificationChanged, domain.AlertSeverityError, true},
		{domain.RouteCanaryTransitionClassificationChanged, false, events.EventRouteCanaryClassificationChanged, domain.AlertSeverityWarning, true},
		{domain.RouteCanaryTransitionNone, false, "", "", false},
	} {
		state := domain.RouteCanaryState{RouteCanaryKey: key, Open: tc.open, FailureReason: "reason"}
		got, ok := routeCanaryTransitionEvent(state, domain.RouteCanaryEvent{RouteCanaryKey: key, Transition: tc.transition}, domain.InstanceHealthStatusHealthy, now)
		require.Equal(t, tc.wantOK, ok, "transition %q", tc.transition)
		if !ok {
			continue
		}
		require.Equal(t, tc.wantType, got.Type)
		require.Equal(t, key.Coordinate(), got.EntityID)
		payload := got.Data.(RouteCanaryChanged)
		require.Equal(t, tc.wantSev, payload.Severity)
		require.Equal(t, "reason", payload.Reason)
		require.Equal(t, now, payload.OccurredAt)
	}
}

// failingRouteCanaryRepo refuses to persist gate outcomes.
type failingRouteCanaryRepo struct{ *memoryRouteCanaryRepo }

func (failingRouteCanaryRepo) UpsertStateWithEvent(context.Context, *domain.RouteCanaryState, *domain.RouteCanaryEvent) error {
	return errors.New("database unavailable")
}

// TestGateDoesNotAnnounceUnpersistedTransitions keeps projection consistent
// with durable state: a transition that was not stored is not published.
func TestGateDoesNotAnnounceUnpersistedTransitions(t *testing.T) {
	bus := &recordingRouteBus{}
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	applier := &stubRouteApplier{}
	gate := newPublishingTestGate(t, prober, applier, failingRouteCanaryRepo{newMemoryRouteCanaryRepo()}, nil, bus, time.Now().UTC())
	require.ErrorIs(t, gate.Apply(context.Background(), testRoutePlan()), ErrRouteCanaryGateFailed)
	require.Equal(t, 1, applier.compensated)
	require.Empty(t, bus.published)
}

// cancellingProber cancels the deployment context once it has observed the
// route, as an operator canceling a run mid-gate would.
type cancellingProber struct {
	stubRouteProber
	cancel context.CancelFunc
}

func (p *cancellingProber) ProbeRoute(ctx context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error) {
	observation, err := p.stubRouteProber.ProbeRoute(ctx, target)
	p.cancel()
	return observation, err
}

// ctxRecordingApplier records whether the rollback ran on a live context.
type ctxRecordingApplier struct {
	compensationCtxErr error
	compensated        int
}

func (a *ctxRecordingApplier) ApplyWithCompensation(context.Context, *domain.DesiredPublicRoutePlan) (routing.Compensation, error) {
	return func(ctx context.Context) error {
		a.compensated++
		a.compensationCtxErr = ctx.Err()
		return nil
	}, nil
}

// TestGateCanceledDeploymentStillAnnouncesAndRollsBack proves a canceled run
// neither strands a broken route nor suppresses the durable transition.
func TestGateCanceledDeploymentStillAnnouncesAndRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prober := &cancellingProber{
		stubRouteProber: stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
			domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
		}},
		cancel: cancel,
	}
	bus := &recordingRouteBus{}
	applier := &ctxRecordingApplier{}
	repo := newMemoryRouteCanaryRepo()
	gate := newPublishingTestGate(t, prober, applier, repo, nil, bus, time.Now().UTC())

	require.ErrorIs(t, gate.Apply(ctx, testRoutePlan()), ErrRouteCanaryGateFailed)
	require.Error(t, ctx.Err(), "the deployment context was canceled mid-gate")
	require.Equal(t, 1, applier.compensated)
	require.NoError(t, applier.compensationCtxErr, "rollback runs on a non-canceled context")
	require.Len(t, bus.ofType(events.EventRouteCanaryOutageOpened), 1)
	require.NoError(t, bus.publishCtxErr[0], "the durable transition is announced on a non-canceled context")
	state, _ := repo.GetState(context.Background(), domain.RouteCanaryKeyForPlan(testRoutePlan()))
	require.NotNil(t, state)
	require.True(t, state.Open)
}
