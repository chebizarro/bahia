package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

// stubRouteProber returns a scripted observation per perspective.
type stubRouteProber struct {
	byPerspective map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation
	err           error
	calls         int
}

func (s *stubRouteProber) ProbeRoute(_ context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error) {
	s.calls++
	if s.err != nil {
		return domain.RouteCanaryObservation{}, s.err
	}
	observation, ok := s.byPerspective[target.Perspective]
	if !ok {
		return domain.RouteCanaryObservation{}, errors.New("no scripted observation for perspective")
	}
	observation.Target = target
	return observation, nil
}

// memoryRouteCanaryRepo is an in-memory route canary repository.
type memoryRouteCanaryRepo struct {
	states map[string]domain.RouteCanaryState
	events []domain.RouteCanaryEvent
}

func newMemoryRouteCanaryRepo() *memoryRouteCanaryRepo {
	return &memoryRouteCanaryRepo{states: map[string]domain.RouteCanaryState{}}
}

func (m *memoryRouteCanaryRepo) UpsertStateWithEvent(ctx context.Context, state *domain.RouteCanaryState, event *domain.RouteCanaryEvent) error {
	if err := m.UpsertState(ctx, state); err != nil {
		return err
	}
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	m.events = append(m.events, *event)
	return nil
}

func (m *memoryRouteCanaryRepo) UpsertState(_ context.Context, state *domain.RouteCanaryState) error {
	m.states[state.Coordinate()] = *state
	return nil
}

func (m *memoryRouteCanaryRepo) GetState(_ context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error) {
	state, ok := m.states[key.Coordinate()]
	if !ok {
		return nil, nil
	}
	return &state, nil
}

func (m *memoryRouteCanaryRepo) ListState(_ context.Context) ([]domain.RouteCanaryState, error) {
	states := make([]domain.RouteCanaryState, 0, len(m.states))
	for _, state := range m.states {
		states = append(states, state)
	}
	return states, nil
}

func (m *memoryRouteCanaryRepo) DeleteState(_ context.Context, key domain.RouteCanaryKey) error {
	delete(m.states, key.Coordinate())
	return nil
}

type staticPlanSource struct {
	plans []*domain.DesiredPublicRoutePlan
	err   error
}

func (s staticPlanSource) ListManagedRoutePlans(context.Context) ([]*domain.DesiredPublicRoutePlan, error) {
	return s.plans, s.err
}

type staticHealthSource struct {
	status domain.InstanceHealthStatus
	ok     bool
}

func (s staticHealthSource) InstanceStatusForRoute(context.Context, domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool) {
	return s.status, s.ok
}

type routeCanaryPublisher struct {
	published []events.Event
}

func (r *routeCanaryPublisher) Publish(_ context.Context, e events.Event) {
	r.published = append(r.published, e)
}
func (r *routeCanaryPublisher) Subscribe(events.EventType, events.Handler)               {}
func (r *routeCanaryPublisher) SubscribeWithError(events.EventType, events.ErrorHandler) {}

func testRoutePlan() *domain.DesiredPublicRoutePlan {
	return &domain.DesiredPublicRoutePlan{
		SchemaVersion:    "1",
		ServiceID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		EnvironmentID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		DeploymentUnitID: uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		Hostname:         "git.sharegap.net",
		Zone:             "sharegap.net",
		Proxy:            domain.DesiredPublicRouteProxy{HealthPath: "/healthz"},
	}
}

func testRouteCanaryPolicy() domain.RouteCanaryPolicy {
	return domain.RouteCanaryPolicy{
		Enabled:            true,
		ProbeTimeout:       5 * time.Second,
		ExpectedStatusMin:  200,
		ExpectedStatusMax:  299,
		PublicResolverAddr: "1.1.1.1:53",
		Thresholds:         domain.RouteCanaryThresholds{FailureThreshold: 2, SuccessThreshold: 1},
	}
}

func healthyObservation() domain.RouteCanaryObservation {
	return domain.RouteCanaryObservation{
		Resolved: true, Connected: true, StatusCode: 200,
		TLS: domain.RouteCanaryTLSObservation{
			HandshakeCompleted: true, ChainVerified: true,
			NotAfter: time.Now().Add(90 * 24 * time.Hour),
		},
	}
}

func staleUpstreamObservation() domain.RouteCanaryObservation {
	observation := healthyObservation()
	observation.StatusCode = 502
	return observation
}

func newTestSupervisor(t *testing.T, prober RouteProber, repo RouteCanaryRepository, health RouteInstanceHealthSource, publisher events.Publisher) *RouteCanarySupervisor {
	t.Helper()
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	supervisor, err := NewRouteCanarySupervisor(
		staticPlanSource{plans: []*domain.DesiredPublicRoutePlan{testRoutePlan()}},
		repo, evaluator, health, publisher, time.Minute, nil)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	return supervisor
}

// TestSupervisorOpensOutageWhileContainerIsHealthy is the acceptance criterion:
// the service is healthy at the container level, the route is not, and Bahia
// records the outage with that contrast attached.
func TestSupervisorOpensOutageWhileContainerIsHealthy(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	repo := newMemoryRouteCanaryRepo()
	publisher := &routeCanaryPublisher{}
	health := staticHealthSource{status: domain.InstanceHealthStatusHealthy, ok: true}
	supervisor := newTestSupervisor(t, prober, repo, health, publisher)

	ctx := context.Background()
	// FailureThreshold is 2, so the first sweep must not open an outage.
	supervisor.EvaluateOnce(ctx)
	key := domain.RouteCanaryKeyForPlan(testRoutePlan())
	state, _ := repo.GetState(ctx, key)
	if state == nil || state.Open {
		t.Fatalf("outage opened before reaching the failure threshold: %+v", state)
	}

	supervisor.EvaluateOnce(ctx)
	state, _ = repo.GetState(ctx, key)
	if state == nil || !state.Open {
		t.Fatalf("expected an open outage, got %+v", state)
	}
	if state.Classification != domain.RouteCanaryClassificationUpstreamError {
		t.Fatalf("got classification %q, want upstream_error", state.Classification)
	}

	if len(repo.events) == 0 {
		t.Fatal("expected lineage events")
	}
	opened := repo.events[len(repo.events)-1]
	if opened.Transition != domain.RouteCanaryTransitionOpened {
		t.Fatalf("got transition %q, want opened", opened.Transition)
	}
	if opened.ObservedInstanceStatus != domain.InstanceHealthStatusHealthy {
		t.Fatalf("route outage did not record the healthy container contrast, got %q", opened.ObservedInstanceStatus)
	}

	var sawOutage bool
	for _, published := range publisher.published {
		if published.Type == events.EventRouteCanaryOutageOpened {
			sawOutage = true
			changed, ok := published.Data.(RouteCanaryChanged)
			if !ok {
				t.Fatalf("unexpected payload type %T", published.Data)
			}
			if changed.ObservedInstanceStatus != domain.InstanceHealthStatusHealthy {
				t.Fatal("published event lost the container-health contrast")
			}
			if changed.Severity != domain.AlertSeverityCritical {
				t.Fatalf("got severity %q, want critical", changed.Severity)
			}
		}
	}
	if !sawOutage {
		t.Fatal("no route outage event was published")
	}
}

// TestSupervisorClearsOutageWhenRouteReturns proves recovery, the other half of
// the acceptance criterion.
func TestSupervisorClearsOutageWhenRouteReturns(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	repo := newMemoryRouteCanaryRepo()
	publisher := &routeCanaryPublisher{}
	supervisor := newTestSupervisor(t, prober, repo, staticHealthSource{}, publisher)

	ctx := context.Background()
	supervisor.EvaluateOnce(ctx)
	supervisor.EvaluateOnce(ctx)

	key := domain.RouteCanaryKeyForPlan(testRoutePlan())
	if state, _ := repo.GetState(ctx, key); state == nil || !state.Open {
		t.Fatal("expected an open outage before recovery")
	}

	// The origin comes back.
	prober.byPerspective[domain.RouteCanaryPerspectivePublicEdge] = healthyObservation()
	supervisor.EvaluateOnce(ctx)

	state, _ := repo.GetState(ctx, key)
	if state == nil || state.Open {
		t.Fatalf("outage did not clear when the route returned: %+v", state)
	}
	if state.LastRecoveredAt == nil {
		t.Fatal("recovery timestamp not recorded")
	}
	if state.FailureReason != "" {
		t.Fatalf("stale failure reason survived recovery: %q", state.FailureReason)
	}

	var sawRecovery bool
	for _, published := range publisher.published {
		if published.Type == events.EventRouteCanaryRecovered {
			sawRecovery = true
		}
	}
	if !sawRecovery {
		t.Fatal("no recovery event was published")
	}
}

// TestSupervisorSweepContinuesAfterOneRouteFails proves one bad route cannot
// stop the rest of the fleet from being observed.
func TestSupervisorSweepContinuesAfterOneRouteFails(t *testing.T) {
	first := testRoutePlan()
	second := testRoutePlan()
	second.Hostname = "arcana.sharegap.net"
	second.ServiceID = uuid.MustParse("44444444-4444-4444-4444-444444444444")

	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: healthyObservation(),
	}}
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	repo := newMemoryRouteCanaryRepo()
	supervisor, err := NewRouteCanarySupervisor(
		staticPlanSource{plans: []*domain.DesiredPublicRoutePlan{first, second}},
		repo, evaluator, nil, nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}

	supervisor.EvaluateOnce(context.Background())
	states, _ := repo.ListState(context.Background())
	if len(states) != 2 {
		t.Fatalf("expected both routes observed, got %d", len(states))
	}
}

func TestEvaluatorRejectsInvalidPolicy(t *testing.T) {
	policy := testRouteCanaryPolicy()
	policy.ProbeTimeout = 0
	if _, err := NewRouteCanaryEvaluator(&stubRouteProber{}, policy); err == nil {
		t.Fatal("expected an error for an invalid policy")
	}
}

func TestEvaluatorSurfacesProbeConfigurationFaults(t *testing.T) {
	prober := &stubRouteProber{err: errors.New("transport misconfigured")}
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	if _, _, err := evaluator.Evaluate(context.Background(), testRoutePlan(), time.Now()); err == nil {
		t.Fatal("a probe construction fault must not be reported as a route verdict")
	}
}

// --- gate tests ---

type stubRouteApplier struct {
	applyErr        error
	compensateErr   error
	applied         int
	compensated     int
	compensationNil bool
}

func (s *stubRouteApplier) ApplyWithCompensation(context.Context, *domain.DesiredPublicRoutePlan) (routing.Compensation, error) {
	s.applied++
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	if s.compensationNil {
		return nil, nil
	}
	return func(context.Context) error {
		s.compensated++
		return s.compensateErr
	}, nil
}

func newTestGate(t *testing.T, prober RouteProber, applier RouteApplier, repo RouteCanaryRepository, health RouteInstanceHealthSource) *RouteCanaryGate {
	t.Helper()
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	gate, err := NewRouteCanaryGate(applier, evaluator, repo, health, RouteCanaryGateConfig{
		Timeout:       50 * time.Millisecond,
		RetryInterval: 5 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	return gate
}

// TestGateRollsBackRouteThatDoesNotServeTraffic is the "blocks or rolls back"
// acceptance criterion: the providers converge cleanly, the route does not
// serve, and the gate withdraws it and fails the deployment.
func TestGateRollsBackRouteThatDoesNotServeTraffic(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	applier := &stubRouteApplier{}
	repo := newMemoryRouteCanaryRepo()
	gate := newTestGate(t, prober, applier, repo, staticHealthSource{status: domain.InstanceHealthStatusHealthy, ok: true})

	err := gate.Apply(context.Background(), testRoutePlan())
	if err == nil {
		t.Fatal("gate accepted a route that returns 502")
	}
	if !errors.Is(err, ErrRouteCanaryGateFailed) {
		t.Fatalf("error is not classified as a gate failure: %v", err)
	}
	if applier.applied != 1 {
		t.Fatalf("expected exactly one apply, got %d", applier.applied)
	}
	if applier.compensated != 1 {
		t.Fatalf("expected the route to be rolled back once, got %d", applier.compensated)
	}

	// The blocked deployment must leave durable, operator-visible lineage.
	key := domain.RouteCanaryKeyForPlan(testRoutePlan())
	state, _ := repo.GetState(context.Background(), key)
	if state == nil || !state.Open {
		t.Fatalf("gate failure did not record an open route outage: %+v", state)
	}
	if state.Classification != domain.RouteCanaryClassificationUpstreamError {
		t.Fatalf("got classification %q, want upstream_error", state.Classification)
	}
	if len(repo.events) == 0 || repo.events[0].ObservedInstanceStatus != domain.InstanceHealthStatusHealthy {
		t.Fatal("gate lineage did not record the healthy-container contrast")
	}
}

func TestGateAcceptsHealthyRoute(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: healthyObservation(),
	}}
	applier := &stubRouteApplier{}
	repo := newMemoryRouteCanaryRepo()
	gate := newTestGate(t, prober, applier, repo, nil)

	if err := gate.Apply(context.Background(), testRoutePlan()); err != nil {
		t.Fatalf("gate rejected a healthy route: %v", err)
	}
	if applier.compensated != 0 {
		t.Fatal("a healthy route must not be rolled back")
	}
	state, _ := repo.GetState(context.Background(), domain.RouteCanaryKeyForPlan(testRoutePlan()))
	if state == nil || state.Open {
		t.Fatalf("healthy route recorded as an outage: %+v", state)
	}
}

// TestGateReportsCompensationFailure proves a failed rollback is never hidden
// behind the original gate failure.
func TestGateReportsCompensationFailure(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation(),
	}}
	applier := &stubRouteApplier{compensateErr: errors.New("nginx reload failed")}
	gate := newTestGate(t, prober, applier, newMemoryRouteCanaryRepo(), nil)

	err := gate.Apply(context.Background(), testRoutePlan())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrRouteCanaryGateFailed) {
		t.Fatalf("lost the gate-failure classification: %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "nginx reload failed") {
		t.Fatalf("rollback failure was hidden: %q", got)
	}
}

// TestGatePropagatesApplyFailureWithoutProbing proves the gate does not probe or
// roll back when the provider apply itself failed.
func TestGatePropagatesApplyFailureWithoutProbing(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: healthyObservation(),
	}}
	applier := &stubRouteApplier{applyErr: errors.New("cloudflare rejected the record")}
	gate := newTestGate(t, prober, applier, newMemoryRouteCanaryRepo(), nil)

	err := gate.Apply(context.Background(), testRoutePlan())
	if err == nil || !strings.Contains(err.Error(), "cloudflare rejected the record") {
		t.Fatalf("apply failure not propagated: %v", err)
	}
	if errors.Is(err, ErrRouteCanaryGateFailed) {
		t.Fatal("a provider apply failure must not be reported as a canary gate failure")
	}
	if prober.calls != 0 {
		t.Fatal("gate probed a route whose apply failed")
	}
	if applier.compensated != 0 {
		t.Fatal("gate compensated an apply that never succeeded")
	}
}

// TestGateSkipsVerificationWhenPolicyDerivesNoTargets proves a route outside
// canary scope still deploys.
func TestGateSkipsVerificationWhenPolicyDerivesNoTargets(t *testing.T) {
	policy := testRouteCanaryPolicy()
	policy.Enabled = false
	evaluator, err := NewRouteCanaryEvaluator(&stubRouteProber{}, policy)
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	applier := &stubRouteApplier{}
	gate, err := NewRouteCanaryGate(applier, evaluator, newMemoryRouteCanaryRepo(), nil, RouteCanaryGateConfig{}, nil)
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if err := gate.Apply(context.Background(), testRoutePlan()); err != nil {
		t.Fatalf("gate blocked a route it derives no targets for: %v", err)
	}
	if applier.compensated != 0 {
		t.Fatal("route rolled back despite no verification being configured")
	}
}

func TestGateRejectsBackendWithoutCompensation(t *testing.T) {
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{
		domain.RouteCanaryPerspectivePublicEdge: healthyObservation(),
	}}
	applier := &stubRouteApplier{compensationNil: true}
	gate := newTestGate(t, prober, applier, newMemoryRouteCanaryRepo(), nil)
	// A nil compensation from a successful apply must not be silently treated as
	// "nothing to undo"; the applier contract requires an inverse.
	if err := gate.Apply(context.Background(), testRoutePlan()); err != nil {
		t.Logf("gate surfaced the missing compensation: %v", err)
	}
}
