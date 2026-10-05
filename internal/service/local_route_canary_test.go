package service

import (
	"context"
	"errors"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// routeServiceState is the desired state of a service whose route is managed.
func routeServiceState(plan *domain.DesiredPublicRoutePlan) domain.EnvironmentServiceState {
	return domain.EnvironmentServiceState{ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DesiredRuntimeState: &domain.DesiredServiceSpec{
		ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, PublicRoute: plan,
	}}
}

// routeDaemon is the route canary wiring of one daemon process over the
// fixture's local event store. Building another one is a restart: nothing but
// the store carries over.
type routeDaemon struct {
	supervisor *RouteCanarySupervisor
	repo       *LocalRouteCanaryRepository
	projector  *RouteCanaryProjector
	evaluator  *RouteCanaryEvaluator
	bus        *eventBusFake
	publisher  *storePublisher
}

func (f *supervisionFixture) routeDaemon(clock *testClock, prober RouteProber, policy domain.RouteCanaryPolicy, index RouteCanaryRepository) *routeDaemon {
	f.t.Helper()
	evaluator, err := NewRouteCanaryEvaluator(prober, policy)
	require.NoError(f.t, err)
	bus := &eventBusFake{}
	publisher := f.publisher()
	projector, err := NewRouteCanaryProjector(bus, publisher, nil)
	require.NoError(f.t, err)
	repo := NewLocalRouteCanaryRepository(f.state, index, nil)
	supervisor, err := NewRouteCanarySupervisor(LocalRoutePlanSource{State: f.state}, repo, evaluator,
		LocalRouteInstanceHealthSource{State: f.state}, bus, time.Minute, nil)
	require.NoError(f.t, err)
	supervisor.now = clock.Now
	supervisor.SetCanonicalProjector(projector)
	return &routeDaemon{supervisor: supervisor, repo: repo, projector: projector, evaluator: evaluator, bus: bus, publisher: publisher}
}

func (d *routeDaemon) announced(eventType events.EventType) int {
	return publishedEventCount(d.bus.published, eventType)
}

func failingRouteProber() *stubRouteProber {
	return &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation()}}
}

func healthyRouteProber() *stubRouteProber {
	return &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{domain.RouteCanaryPerspectivePublicEdge: healthyObservation()}}
}

// offlineRouteIndex is a SQL index during a database outage: every write
// fails, and a read fails the test, because supervision must never decide from
// SQL.
type offlineRouteIndex struct{ t *testing.T }

func (i offlineRouteIndex) UpsertStateWithEvent(context.Context, *domain.RouteCanaryState, *domain.RouteCanaryEvent) error {
	return errSQLOffline
}
func (i offlineRouteIndex) UpsertState(context.Context, *domain.RouteCanaryState) error {
	return errSQLOffline
}
func (i offlineRouteIndex) DeleteState(context.Context, domain.RouteCanaryKey) error {
	return errSQLOffline
}
func (i offlineRouteIndex) GetState(context.Context, domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	i.t.Error("route canary supervision read state from the SQL index")
	return nil, errSQLOffline
}
func (i offlineRouteIndex) ListState(context.Context) ([]domain.RouteCanaryState, error) {
	i.t.Error("route canary supervision listed state from the SQL index")
	return nil, errSQLOffline
}

func auditTransitions(audits []gonostr.Event, transition domain.RouteCanaryTransition) int {
	count := 0
	for _, audit := range audits {
		if supervisionTag(audit, "transition") == string(transition) {
			count++
		}
	}
	return count
}

func TestLocalRouteCanarySupervisorSweepsWithoutSQL(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	prober := failingRouteProber()
	daemon := f.routeDaemon(clock, prober, testRouteCanaryPolicy(), nil)

	daemon.supervisor.EvaluateOnce(context.Background())

	require.Equal(t, 1, prober.calls, "the route in relay-derived desired state is probed")
	require.Len(t, daemon.bus.published, 1, "the observation is announced in process")
	require.Equal(t, 1, daemon.publisher.published(kinds.NIP38Status, routeCanaryStatusSchema))
	states := f.stored(kinds.CASControlState, routeCanaryStateSchema)
	require.Len(t, states, 1, "one canonical state record per route")
	state, ok := decodeRouteCanaryStateRecord(states[0])
	require.True(t, ok)
	require.Equal(t, domain.RouteCanaryKeyForPlan(plan).Coordinate(), state.Coordinate())
	require.Equal(t, 1, state.ConsecutiveFailures)
	require.False(t, state.Open)
}

func TestLocalRouteCanaryRestartResumesOutageState(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	key := domain.RouteCanaryKeyForPlan(plan)
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	ctx := context.Background()

	// First failure: below the open threshold of two.
	first := f.routeDaemon(clock, failingRouteProber(), testRouteCanaryPolicy(), nil)
	first.supervisor.EvaluateOnce(ctx)
	require.Zero(t, first.announced(events.EventRouteCanaryOutageOpened))

	// A restarted daemon resumes the streak, so its first failure opens.
	clock.Advance(time.Minute)
	second := f.routeDaemon(clock, failingRouteProber(), testRouteCanaryPolicy(), nil)
	second.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, second.announced(events.EventRouteCanaryOutageOpened), "restart must resume the canonical failure streak")
	opened, err := second.repo.GetState(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, opened)
	require.True(t, opened.Open)
	require.NotNil(t, opened.OpenedAt)
	failingSince := *opened.OpenedAt
	require.True(t, failingSince.Equal(clock.Now()))

	// Restarted again while the outage is open: it stays the same outage.
	clock.Advance(time.Minute)
	third := f.routeDaemon(clock, failingRouteProber(), testRouteCanaryPolicy(), nil)
	third.supervisor.EvaluateOnce(ctx)
	require.Zero(t, third.announced(events.EventRouteCanaryOutageOpened), "an open outage must not be opened again after a restart")
	resumed, err := third.repo.GetState(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.True(t, resumed.Open)
	require.Equal(t, 3, resumed.ConsecutiveFailures)
	require.NotNil(t, resumed.OpenedAt)
	require.True(t, resumed.OpenedAt.Equal(failingSince), "restart must keep the canonical outage start")

	// And a restarted daemon is the one that recovers it.
	clock.Advance(time.Minute)
	fourth := f.routeDaemon(clock, healthyRouteProber(), testRouteCanaryPolicy(), nil)
	fourth.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, fourth.announced(events.EventRouteCanaryRecovered))

	audits := f.stored(kinds.CASAudit, routeCanaryAuditSchema)
	require.Equal(t, 1, auditTransitions(audits, domain.RouteCanaryTransitionOpened), "one outage across three restarts")
	require.Equal(t, 1, auditTransitions(audits, domain.RouteCanaryTransitionRecovered))
}

func TestLocalRouteCanaryDesiredStateChangeFromRelayChangesProbedSet(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	prober := healthyRouteProber()
	daemon := f.routeDaemon(clock, prober, testRouteCanaryPolicy(), offlineRouteIndex{t})
	ctx := context.Background()

	daemon.supervisor.EvaluateOnce(ctx)
	require.Zero(t, prober.calls, "no desired route, nothing to probe")

	// The route arrives as a relay event.
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, prober.calls)

	// A replacement without a route withdraws it.
	clock.Advance(time.Minute)
	withdrawn := routeServiceState(plan)
	withdrawn.DesiredRuntimeState.PublicRoute = nil
	f.deliverServiceState(withdrawn, clock.Now())
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, prober.calls, "a withdrawn route must stop being probed")

	// The route returns, then the whole state is tombstoned.
	clock.Advance(time.Minute)
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 2, prober.calls)
	clock.Advance(time.Minute)
	f.deliverServiceStateTombstone(plan.ServiceID, plan.EnvironmentID, clock.Now())
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 2, prober.calls, "a tombstoned state must stop being probed")
}

func TestLocalRouteCanarySQLIndexFailureDoesNotBlockProbeOrCanonicalObservables(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	policy := testRouteCanaryPolicy()
	policy.Thresholds.FailureThreshold = 1
	prober := failingRouteProber()
	daemon := f.routeDaemon(clock, prober, policy, offlineRouteIndex{t})

	daemon.supervisor.EvaluateOnce(context.Background())

	require.Equal(t, 1, prober.calls)
	require.Equal(t, 1, daemon.announced(events.EventRouteCanaryOutageOpened), "the outage is announced although SQL is down")
	require.Equal(t, 1, daemon.publisher.published(kinds.NIP38Status, routeCanaryStatusSchema))
	require.Equal(t, 1, daemon.publisher.published(kinds.CASControlState, routeCanaryStateSchema))
	require.Equal(t, 1, daemon.publisher.published(kinds.CASAudit, routeCanaryAuditSchema))
	state, err := daemon.repo.GetState(context.Background(), domain.RouteCanaryKeyForPlan(plan))
	require.NoError(t, err)
	require.NotNil(t, state)
	require.True(t, state.Open)
}

func TestLocalRouteCanaryRetryPublishesNoDuplicateCanonicalRecords(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	key := domain.RouteCanaryKeyForPlan(plan)
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	policy := testRouteCanaryPolicy()
	policy.Thresholds.FailureThreshold = 1
	daemon := f.routeDaemon(clock, failingRouteProber(), policy, nil)
	ctx := context.Background()

	// The relay path refuses the records: nothing is announced or remembered,
	// so the next sweep observes the transition again.
	daemon.publisher.failWith(errors.New("relay rejected the event"))
	daemon.supervisor.EvaluateOnce(ctx)
	require.Empty(t, daemon.bus.published)
	state, err := daemon.repo.GetState(ctx, key)
	require.NoError(t, err)
	require.Nil(t, state)
	require.Zero(t, daemon.publisher.total())

	daemon.publisher.failWith(nil)
	clock.Advance(time.Minute)
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, daemon.announced(events.EventRouteCanaryOutageOpened))
	require.Equal(t, 1, daemon.publisher.published(kinds.CASAudit, routeCanaryAuditSchema), "one audit fact for one transition")
	require.Len(t, f.stored(kinds.CASControlState, routeCanaryStateSchema), 1)

	// The bus redelivers the announced transition to the projector.
	published := daemon.publisher.total()
	for _, event := range append([]events.Event(nil), daemon.bus.published...) {
		daemon.bus.Publish(ctx, event)
	}
	require.Equal(t, published, daemon.publisher.total(), "a redelivered transition must not be signed again")
}

func TestLocalRouteCanaryGatePublishesCanonicalStateBeforeIndex(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	key := domain.RouteCanaryKeyForPlan(plan)
	f.deliverServiceState(routeServiceState(plan), clock.Now())
	policy := testRouteCanaryPolicy()
	policy.Thresholds.FailureThreshold = 1
	ctx := context.Background()

	// The periodic supervisor opens an outage.
	daemon := f.routeDaemon(clock, failingRouteProber(), policy, nil)
	daemon.supervisor.EvaluateOnce(ctx)
	require.Equal(t, 1, daemon.announced(events.EventRouteCanaryOutageOpened))

	// A deploy's gate verifies the route healthy while SQL is down.
	clock.Advance(time.Minute)
	gateEvaluator, err := NewRouteCanaryEvaluator(healthyRouteProber(), policy)
	require.NoError(t, err)
	gateBus := &eventBusFake{}
	gatePublisher := f.publisher()
	gateProjector, err := NewRouteCanaryProjector(gateBus, gatePublisher, nil)
	require.NoError(t, err)
	gate, err := NewRouteCanaryGate(&stubRouteApplier{}, gateEvaluator, NewLocalRouteCanaryRepository(f.state, offlineRouteIndex{t}, nil), nil, gateBus,
		RouteCanaryGateConfig{Timeout: time.Minute, RetryInterval: time.Second}, nil)
	require.NoError(t, err)
	gate.now = clock.Now
	gate.SetCanonicalProjector(gateProjector)
	require.NoError(t, gate.Apply(ctx, plan))
	require.Equal(t, 1, publishedEventCount(gateBus.published, events.EventRouteCanaryRecovered))
	require.Equal(t, 1, gatePublisher.published(kinds.CASAudit, routeCanaryAuditSchema), "the gate's bus delivery must not sign the recovery again")

	// A restarted daemon reads the gate's recovery from the canonical record.
	restarted := f.routeDaemon(clock, healthyRouteProber(), policy, nil)
	state, err := restarted.repo.GetState(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.False(t, state.Open)
	require.NotNil(t, state.LastRecoveredAt)
}

func TestLocalRouteInstanceHealthSourceReportsWorstCanonicalStatus(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	plan := testRoutePlan()
	routeKey := domain.RouteCanaryKeyForPlan(plan)
	projector := NewManagedInstanceHealthProjector(nil, f.publisher(), nil)
	for target, status := range map[string]domain.InstanceHealthStatus{"api": domain.InstanceHealthStatusHealthy, "worker": domain.InstanceHealthStatusUnhealthy} {
		health := domain.ManagedInstanceHealth{
			ManagedInstanceKey: domain.ManagedInstanceKey{ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DeploymentUnitID: plan.DeploymentUnitID, RuntimeTargetName: target},
			Status:             status, LastObservedAt: clock.Now(), UpdatedAt: clock.Now(),
		}
		require.NoError(t, projector.handle(context.Background(), events.Event{Type: events.EventRuntimeInstanceHealthChanged, Data: ManagedInstanceHealthChanged{
			EventID: uuid.NewString(), Health: health, OccurredAt: clock.Now(),
		}}))
	}
	source := LocalRouteInstanceHealthSource{State: f.state}

	status, ok := source.InstanceStatusForRoute(context.Background(), routeKey)
	require.True(t, ok)
	require.Equal(t, domain.InstanceHealthStatusUnhealthy, status, "the least healthy instance behind the route is reported")

	other := routeKey
	other.ServiceID = uuid.New()
	_, ok = source.InstanceStatusForRoute(context.Background(), other)
	require.False(t, ok)
}

func TestLocalRouteCanaryRepositoryReadsIndexOnlyWithoutCanonicalPublisher(t *testing.T) {
	f := newSupervisionFixture(t)
	plan := testRoutePlan()
	key := domain.RouteCanaryKeyForPlan(plan)
	ctx := context.Background()
	index := newMemoryRouteCanaryRepo()
	require.NoError(t, index.UpsertState(ctx, &domain.RouteCanaryState{RouteCanaryKey: key, Open: true, ConsecutiveFailures: 4}))

	// With canonical records published, the index is never the source.
	state, err := NewLocalRouteCanaryRepository(f.state, index, nil).GetState(ctx, key)
	require.NoError(t, err)
	require.Nil(t, state)

	// Without relay publishing it is the only durable copy.
	state, err = NewLocalRouteCanaryRepository(f.state, index, nil, WithRouteCanaryIndexResume()).GetState(ctx, key)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, 4, state.ConsecutiveFailures)
}

// signalRouteProber reports each probe on a channel.
type signalRouteProber struct {
	inner  RouteProber
	probed chan struct{}
}

func (p signalRouteProber) ProbeRoute(ctx context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error) {
	select {
	case p.probed <- struct{}{}:
	default:
	}
	return p.inner.ProbeRoute(ctx, target)
}

func TestRouteCanarySupervisorRunWaitsForLocalStoreReadiness(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	f.deliverServiceState(routeServiceState(testRoutePlan()), clock.Now())
	prober := signalRouteProber{inner: healthyRouteProber(), probed: make(chan struct{}, 1)}
	daemon := f.routeDaemon(clock, prober, testRouteCanaryPolicy(), nil)
	gate := newReadinessGate()
	daemon.supervisor.SetReadiness(gate)

	// Before catch-up Run only waits: stopping it finds nothing probed.
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, daemon.supervisor.Run(stopped), context.Canceled)
	require.Empty(t, prober.probed)
	require.Zero(t, daemon.publisher.total())

	// After catch-up the first sweep runs at once.
	gate.open()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.supervisor.Run(ctx) }()
	<-prober.probed
	stop()
	require.ErrorIs(t, <-done, context.Canceled)
}
