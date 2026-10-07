package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/openagentsinc/bahia/internal/domain"
)

// hostnameRouteProber scripts a status per hostname, counts probes per
// hostname, and records every target it was asked to probe.
type hostnameRouteProber struct {
	mu      sync.Mutex
	status  map[string]int
	calls   map[string]int
	targets []domain.RouteCanaryTarget
}

func newHostnameRouteProber() *hostnameRouteProber {
	return &hostnameRouteProber{status: map[string]int{}, calls: map[string]int{}}
}

func (p *hostnameRouteProber) ProbeRoute(_ context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[target.Hostname]++
	p.targets = append(p.targets, target)
	observation := healthyObservation()
	if status, ok := p.status[target.Hostname]; ok {
		observation.StatusCode = status
	}
	observation.Target = target
	// Body assertions are exercised against real bytes in the probe adapter
	// tests; here they are scripted to hold so verdicts isolate status policy.
	observation.BodyMatched = target.HasBodyAssertion()
	return observation, nil
}

func (p *hostnameRouteProber) count(hostname string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[hostname]
}

// mutableCanaryPlanSource lets a test withdraw routes or fail enumeration.
type mutableCanaryPlanSource struct {
	plans []*domain.DesiredPublicRoutePlan
	err   error
}

func (s *mutableCanaryPlanSource) ListManagedRoutePlans(context.Context) ([]*domain.DesiredPublicRoutePlan, error) {
	return s.plans, s.err
}

func canaryRoutePlanFor(hostname string, serviceID string) *domain.DesiredPublicRoutePlan {
	plan := testRoutePlan()
	plan.Hostname = hostname
	plan.ServiceID = uuid.MustParse(serviceID)
	return plan
}

const (
	canaryFastRoute = "fast.sharegap.net"
	canarySlowRoute = "slow.sharegap.net"
)

type scheduledCanarySupervisor struct {
	supervisor *RouteCanarySupervisor
	prober     *hostnameRouteProber
	source     *mutableCanaryPlanSource
	repo       *memoryRouteCanaryRepo
	clock      time.Time
}

func (s *scheduledCanarySupervisor) advance(d time.Duration) { s.clock = s.clock.Add(d) }

// newScheduledCanarySupervisor builds a supervisor over a fast route overridden to a
// 15s interval and a slow route on the 60s fleet-wide interval, driven by an
// injected clock so scheduling is tested without Run or sleeping.
func newScheduledCanarySupervisor(t *testing.T) *scheduledCanarySupervisor {
	t.Helper()
	policy := testRouteCanaryPolicy()
	policy.Overrides = map[string]domain.RouteCanaryOverride{
		canaryFastRoute: {Interval: 15 * time.Second},
	}
	prober := newHostnameRouteProber()
	evaluator, err := NewRouteCanaryEvaluator(prober, policy)
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	source := &mutableCanaryPlanSource{plans: []*domain.DesiredPublicRoutePlan{
		canaryRoutePlanFor(canaryFastRoute, "44444444-4444-4444-4444-444444444444"),
		canaryRoutePlanFor(canarySlowRoute, "55555555-5555-5555-5555-555555555555"),
	}}
	repo := newMemoryRouteCanaryRepo()
	supervisor, err := NewRouteCanarySupervisor(source, repo, evaluator, nil, nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	harness := &scheduledCanarySupervisor{
		supervisor: supervisor,
		prober:     prober,
		source:     source,
		repo:       repo,
		clock:      time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
	supervisor.now = func() time.Time { return harness.clock }
	return harness
}

func (s *scheduledCanarySupervisor) expectCounts(t *testing.T, when string, fast, slow int) {
	t.Helper()
	if got := s.prober.count(canaryFastRoute); got != fast {
		t.Fatalf("%s: fast route probed %d times, want %d", when, got, fast)
	}
	if got := s.prober.count(canarySlowRoute); got != slow {
		t.Fatalf("%s: slow route probed %d times, want %d", when, got, slow)
	}
}

// TestSupervisorProbesEachRouteOnItsOwnInterval is the interval half of the
// bahia-6xztt acceptance criterion: a route overridden to 15s is probed every
// 15s while a neighbouring route stays on the 60s fleet-wide interval.
func TestSupervisorProbesEachRouteOnItsOwnInterval(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()

	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "first sweep", 1, 1)
	if got := h.supervisor.NextWakeDelay(h.clock); got != 15*time.Second {
		t.Fatalf("next wake in %s, want 15s for the overridden route", got)
	}

	h.advance(10 * time.Second)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "t+10s", 1, 1)
	if got := h.supervisor.NextWakeDelay(h.clock); got != 5*time.Second {
		t.Fatalf("next wake in %s, want 5s", got)
	}

	for i, elapsed := range []time.Duration{15, 30, 45} {
		h.clock = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Add(elapsed * time.Second)
		h.supervisor.EvaluateDue(ctx)
		h.expectCounts(t, "t+"+(elapsed*time.Second).String(), 2+i, 1)
	}

	h.clock = time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "t+60s", 5, 2)
}

// TestSupervisorScheduleToleratesSlightlyEarlyWakeups proves a wake-up a moment
// before a route's due time, as a slewed wall clock can produce against a
// monotonic timer, still probes it instead of skipping a whole cycle.
func TestSupervisorScheduleToleratesSlightlyEarlyWakeups(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()
	h.supervisor.EvaluateDue(ctx)

	h.advance(15*time.Second - 200*time.Millisecond)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "just before due", 2, 1)
}

// TestSupervisorEvaluateOnceProbesEveryRoute proves the forced sweep keeps its
// original contract of probing every managed route exactly once.
func TestSupervisorEvaluateOnceProbesEveryRoute(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()

	h.supervisor.EvaluateDue(ctx)
	h.supervisor.EvaluateOnce(ctx)
	h.expectCounts(t, "forced sweep", 2, 2)

	// The forced sweep restarts each schedule, so nothing is due immediately.
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "after forced sweep", 2, 2)
}

// TestSupervisorBacksOffWhenPlanSourceFails proves an overdue schedule left by
// a failed enumeration backs off to the fleet-wide interval instead of spinning.
func TestSupervisorBacksOffWhenPlanSourceFails(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()
	h.supervisor.EvaluateDue(ctx)

	h.source.err = errors.New("database unavailable")
	h.advance(20 * time.Second)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "failed enumeration", 1, 1)
	if got := h.supervisor.NextWakeDelay(h.clock); got != 40*time.Second {
		t.Fatalf("next wake in %s, want 40s (the slow route's future due time), not a retry loop", got)
	}

	h.source.err = nil
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "recovered enumeration", 2, 1)
}

// TestSupervisorSweepsAgainAtOnceWhenASweepOverranTheInterval proves a sweep
// that took longer than a route's interval (for example while routes time out)
// is followed immediately by the next one, rather than a full extra interval.
func TestSupervisorSweepsAgainAtOnceWhenASweepOverranTheInterval(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	h.supervisor.EvaluateDue(context.Background())

	h.advance(70 * time.Second)
	if got := h.supervisor.NextWakeDelay(h.clock); got != minRouteCanaryWakeDelay {
		t.Fatalf("next wake in %s, want the %s floor for overdue routes", got, minRouteCanaryWakeDelay)
	}
	h.supervisor.EvaluateDue(context.Background())
	h.expectCounts(t, "after overrun", 2, 2)
}

func TestSupervisorWakeDelayIsCappedAtFleetInterval(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	if got := h.supervisor.NextWakeDelay(h.clock); got != time.Minute {
		t.Fatalf("with nothing scheduled the next wake is in %s, want the 60s fleet interval", got)
	}
}

// TestSupervisorProbesReaddedRouteImmediately proves a withdrawn route's
// schedule is forgotten, so re-adding it probes it at once.
func TestSupervisorProbesReaddedRouteImmediately(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()
	h.supervisor.EvaluateDue(ctx)

	all := h.source.plans
	h.source.plans = all[:1]
	h.advance(5 * time.Second)
	h.supervisor.EvaluateDue(ctx)

	h.source.plans = all
	h.advance(5 * time.Second)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "re-added", 1, 2)
}

// TestSupervisorReprobesAfterWallClockStepsBackwards proves a backwards clock
// step cannot silence a route until the clock catches up.
func TestSupervisorReprobesAfterWallClockStepsBackwards(t *testing.T) {
	h := newScheduledCanarySupervisor(t)
	ctx := context.Background()
	h.supervisor.EvaluateDue(ctx)

	h.advance(-2 * time.Hour)
	h.supervisor.EvaluateDue(ctx)
	h.expectCounts(t, "after backwards step", 2, 2)
}

// TestSupervisorAppliesStatusOverrideOnlyToItsRoute is the expectation half of
// the bahia-6xztt acceptance criterion for periodic probing: a route with a
// non-2xx health contract is healthy on its own terms, while the same response
// from a route on fleet-wide policy is still a failure.
func TestSupervisorAppliesStatusOverrideOnlyToItsRoute(t *testing.T) {
	policy := testRouteCanaryPolicy()
	policy.Overrides = map[string]domain.RouteCanaryOverride{
		canaryFastRoute: {ExpectedStatusMin: canaryOverrideInt(401), ExpectedStatusMax: canaryOverrideInt(401)},
	}
	prober := newHostnameRouteProber()
	prober.status[canaryFastRoute] = 401
	prober.status[canarySlowRoute] = 401
	evaluator, err := NewRouteCanaryEvaluator(prober, policy)
	if err != nil {
		t.Fatalf("evaluator: %v", err)
	}
	fast := canaryRoutePlanFor(canaryFastRoute, "44444444-4444-4444-4444-444444444444")
	slow := canaryRoutePlanFor(canarySlowRoute, "55555555-5555-5555-5555-555555555555")
	repo := newMemoryRouteCanaryRepo()
	supervisor, err := NewRouteCanarySupervisor(
		staticPlanSource{plans: []*domain.DesiredPublicRoutePlan{fast, slow}}, repo, evaluator, nil, nil, time.Minute, nil)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	supervisor.EvaluateOnce(context.Background())

	fastState, _ := repo.GetState(context.Background(), domain.RouteCanaryKeyForPlan(fast))
	if fastState == nil || fastState.Classification != domain.RouteCanaryClassificationRouteOK {
		t.Fatalf("overridden route: got %+v, want route_ok", fastState)
	}
	slowState, _ := repo.GetState(context.Background(), domain.RouteCanaryKeyForPlan(slow))
	if slowState == nil || slowState.Classification != domain.RouteCanaryClassificationStatusMismatch {
		t.Fatalf("fleet-policy route: got %+v, want status_mismatch", slowState)
	}
}

func canaryOverrideInt(value int) *int          { return &value }
func canaryOverrideString(value string) *string { return &value }

// TestGateAppliesRouteOverride proves the post-deploy gate uses the same
// per-route policy as periodic probing: an overridden route deploys on its own
// health contract, and the targets it probes carry the override.
func TestGateAppliesRouteOverride(t *testing.T) {
	const regex = `(?s).*"status"\s*:\s*"ok".*`
	policy := testRouteCanaryPolicy()
	policy.Overrides = map[string]domain.RouteCanaryOverride{
		"git.sharegap.net": {
			ProbeTimeout:      30 * time.Second,
			ExpectedStatusMin: canaryOverrideInt(401),
			ExpectedStatusMax: canaryOverrideInt(401),
			ExpectedBodyRegex: canaryOverrideString(regex),
		},
	}
	gateFor := func(prober RouteProber, applier RouteApplier) *RouteCanaryGate {
		evaluator, err := NewRouteCanaryEvaluator(prober, policy)
		if err != nil {
			t.Fatalf("evaluator: %v", err)
		}
		gate, err := NewRouteCanaryGate(applier, evaluator, newMemoryRouteCanaryRepo(), nil, nil,
			RouteCanaryGateConfig{Timeout: 50 * time.Millisecond, RetryInterval: 5 * time.Millisecond}, nil)
		if err != nil {
			t.Fatalf("gate: %v", err)
		}
		return gate
	}

	prober := newHostnameRouteProber()
	prober.status["git.sharegap.net"] = 401
	applier := &stubRouteApplier{}
	if err := gateFor(prober, applier).Apply(context.Background(), testRoutePlan()); err != nil {
		t.Fatalf("gate rejected a route that meets its overridden contract: %v", err)
	}
	if applier.compensated != 0 {
		t.Fatal("a route meeting its override must not be rolled back")
	}
	if len(prober.targets) == 0 {
		t.Fatal("gate probed nothing")
	}
	for _, target := range prober.targets {
		if target.ExpectedStatusMin != 401 || target.ExpectedStatusMax != 401 ||
			target.ExpectedBodyRegex != regex || target.Timeout != 30*time.Second {
			t.Fatalf("gate probed a target without the route override: %+v", target)
		}
	}

	// The same response from a route on fleet-wide policy is still blocked.
	other := testRoutePlan()
	other.Hostname = "arcana.sharegap.net"
	otherProber := newHostnameRouteProber()
	otherProber.status["arcana.sharegap.net"] = 401
	otherApplier := &stubRouteApplier{}
	if err := gateFor(otherProber, otherApplier).Apply(context.Background(), other); !errors.Is(err, ErrRouteCanaryGateFailed) {
		t.Fatalf("override leaked to another route's gate: err = %v", err)
	}
	if otherApplier.compensated != 1 {
		t.Fatalf("expected the fleet-policy route to be rolled back, got %d", otherApplier.compensated)
	}
}
