package service

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
)

// RouteProber observes one managed-route target from a single perspective.
type RouteProber interface {
	ProbeRoute(ctx context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error)
}

// RouteCanaryRepository persists route outage state and its lineage.
type RouteCanaryRepository interface {
	UpsertStateWithEvent(ctx context.Context, state *domain.RouteCanaryState, event *domain.RouteCanaryEvent) error
	UpsertState(ctx context.Context, state *domain.RouteCanaryState) error
	GetState(ctx context.Context, key domain.RouteCanaryKey) (*domain.RouteCanaryState, error)
	ListState(ctx context.Context) ([]domain.RouteCanaryState, error)
	DeleteState(ctx context.Context, key domain.RouteCanaryKey) error
}

// RouteCanaryPlanSource enumerates the managed routes that should be probed.
//
// The source of truth is desired state, not live provider configuration: a route
// withdrawn from desired state must stop alarming, and a route present in
// desired state must be probed even when the provider has lost it.
type RouteCanaryPlanSource interface {
	ListManagedRoutePlans(ctx context.Context) ([]*domain.DesiredPublicRoutePlan, error)
}

// RouteInstanceHealthSource supplies the container-level status observed for a
// route's deployment unit. It is used only to annotate route events, never to
// decide them, so route verdicts stay independent of supervision timing.
type RouteInstanceHealthSource interface {
	InstanceStatusForRoute(ctx context.Context, key domain.RouteCanaryKey) (domain.InstanceHealthStatus, bool)
}

// RouteCanaryChanged is the payload published when route outage state changes.
type RouteCanaryChanged struct {
	EventID string                  `json:"event_id"`
	State   domain.RouteCanaryState `json:"state"`
	Event   domain.RouteCanaryEvent `json:"event"`
	// ObservedInstanceStatus is the container-level status at the same instant.
	// A healthy value alongside an open route outage is exactly the
	// "service up, route down" condition operators need to see.
	ObservedInstanceStatus domain.InstanceHealthStatus `json:"observed_instance_status,omitempty"`
	Severity               domain.AlertSeverity        `json:"severity"`
	Reason                 string                      `json:"reason,omitempty"`
	OccurredAt             time.Time                   `json:"occurred_at"`
}

// RouteCanaryEvaluator observes every perspective of one managed route and folds
// the result into durable outage state.
type RouteCanaryEvaluator struct {
	prober RouteProber
	policy domain.RouteCanaryPolicy
}

// NewRouteCanaryEvaluator builds an evaluator for a validated policy.
func NewRouteCanaryEvaluator(prober RouteProber, policy domain.RouteCanaryPolicy) (*RouteCanaryEvaluator, error) {
	if prober == nil {
		return nil, fmt.Errorf("route canary evaluator requires a prober")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &RouteCanaryEvaluator{prober: prober, policy: policy}, nil
}

// Policy exposes the evaluator's configured policy.
func (e *RouteCanaryEvaluator) Policy() domain.RouteCanaryPolicy {
	return e.policy
}

// RouteCanaryVerdict is one complete evaluation of a managed route.
type RouteCanaryVerdict struct {
	Key            domain.RouteCanaryKey
	Classification domain.RouteCanaryClassification
	Perspective    domain.RouteCanaryPerspective
	Reason         string
	Observations   []domain.RouteCanaryObservation
	TLSNotAfter    *time.Time
	// IsFailing is target-aware, so a warning an operator has configured as
	// mandatory for this route counts as a failure here.
	IsFailing bool
}

// Failing reports whether the verdict represents a broken route.
func (v RouteCanaryVerdict) Failing() bool { return v.IsFailing }

// Evaluate probes every derived target for the plan once and reduces the result.
//
// It returns ok=false when the policy derives no targets, which is a legitimate
// configuration outcome rather than an error.
func (e *RouteCanaryEvaluator) Evaluate(ctx context.Context, plan *domain.DesiredPublicRoutePlan, now time.Time) (RouteCanaryVerdict, bool, error) {
	targets, err := domain.DeriveRouteCanaryTargets(plan, e.policy)
	if err != nil {
		return RouteCanaryVerdict{}, false, err
	}
	if len(targets) == 0 {
		return RouteCanaryVerdict{}, false, nil
	}

	observations := make([]domain.RouteCanaryObservation, 0, len(targets))
	for _, target := range targets {
		observation, err := e.prober.ProbeRoute(ctx, target)
		if err != nil {
			// A probe error is a configuration fault, not a route verdict.
			return RouteCanaryVerdict{}, false, fmt.Errorf("probe %s: %w", target.Describe(), err)
		}
		observations = append(observations, observation)
	}

	reduction, ok := domain.ReduceRouteObservations(observations, now)
	if !ok {
		return RouteCanaryVerdict{}, false, nil
	}

	verdict := RouteCanaryVerdict{
		Key:            domain.RouteCanaryKeyForPlan(plan),
		Classification: reduction.Classification,
		Perspective:    reduction.Perspective,
		Observations:   observations,
		IsFailing:      reduction.Failing,
		Reason:         domain.DescribeRouteObservation(reduction.Observation, reduction.Classification, now),
	}
	if tls := reduction.Observation.TLS; tls.HandshakeCompleted && !tls.NotAfter.IsZero() {
		notAfter := tls.NotAfter
		verdict.TLSNotAfter = &notAfter
	}
	return verdict, true, nil
}

// routeCanaryScheduleTolerance lets a route whose next probe is due within this
// window be probed in the current sweep. Wake-ups are driven by a monotonic
// timer while due times are wall-clock, so without a tolerance a small clock
// slew could leave a route one full wake-up short of due. It also batches routes
// that fall due a moment apart into a single sweep.
const routeCanaryScheduleTolerance = time.Second

// minRouteCanaryWakeDelay floors the delay between sweeps so a scheduling
// anomaly can never turn the supervisor into a busy loop.
const minRouteCanaryWakeDelay = 100 * time.Millisecond

// RouteCanarySupervisor periodically probes every managed route and maintains
// durable outage state.
//
// Each route is probed on its own interval: the fleet-wide interval unless the
// route's override sets one. The supervisor records when each route is next
// due and wakes at the earliest due time, so a route tuned to 15s is probed
// every 15s while its neighbours stay on the fleet-wide cadence.
//
// The wake-up timer is a health-check timer, which is the permitted use of a
// timer in this codebase: it schedules observation, it never waits for an event
// or infers completion from elapsed time.
type RouteCanarySupervisor struct {
	source       RouteCanaryPlanSource
	repo         RouteCanaryRepository
	evaluator    *RouteCanaryEvaluator
	healthSource RouteInstanceHealthSource
	publisher    events.Publisher
	interval     time.Duration
	logger       *zap.Logger
	now          func() time.Time

	routeLocksMu sync.Mutex
	routeLocks   map[string]*sync.Mutex

	// scheduleMu guards nextDue, the instant each route coordinate is next due,
	// and enumerationFailed, whether the latest sweep could not list routes.
	// A route with no entry is due immediately.
	scheduleMu        sync.Mutex
	nextDue           map[string]time.Time
	enumerationFailed bool
}

// NewRouteCanarySupervisor builds a periodic route canary supervisor.
func NewRouteCanarySupervisor(
	source RouteCanaryPlanSource,
	repo RouteCanaryRepository,
	evaluator *RouteCanaryEvaluator,
	healthSource RouteInstanceHealthSource,
	publisher events.Publisher,
	interval time.Duration,
	logger *zap.Logger,
) (*RouteCanarySupervisor, error) {
	if source == nil {
		return nil, fmt.Errorf("route canary supervisor requires a plan source")
	}
	if repo == nil {
		return nil, fmt.Errorf("route canary supervisor requires a repository")
	}
	if evaluator == nil {
		return nil, fmt.Errorf("route canary supervisor requires an evaluator")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("route canary supervisor interval must be positive")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RouteCanarySupervisor{
		source:       source,
		repo:         repo,
		evaluator:    evaluator,
		healthSource: healthSource,
		publisher:    publisher,
		interval:     interval,
		logger:       logger.Named("route-canary-supervisor"),
		now:          func() time.Time { return time.Now().UTC() },
		routeLocks:   map[string]*sync.Mutex{},
		nextDue:      map[string]time.Time{},
	}, nil
}

// Name identifies the supervisor in background-runner logging and health.
func (s *RouteCanarySupervisor) Name() string { return "route-canary-supervisor" }

// Run probes every route immediately and then each route whenever it falls
// due, until the context is done.
func (s *RouteCanarySupervisor) Run(ctx context.Context) error {
	for {
		s.EvaluateDue(ctx)
		timer := time.NewTimer(s.NextWakeDelay(s.now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// EvaluateOnce probes every managed route exactly once, whether or not it is
// due, and restarts each route's schedule from now.
//
// A failure for one route never aborts the sweep: an unreachable route must not
// prevent the rest of the fleet from being observed.
func (s *RouteCanarySupervisor) EvaluateOnce(ctx context.Context) {
	s.sweep(ctx, true)
}

// EvaluateDue probes every managed route whose interval has elapsed since it
// was last probed, and routes never probed before. It is what the periodic
// loop runs; tests drive it directly with an injected clock.
func (s *RouteCanarySupervisor) EvaluateDue(ctx context.Context) {
	s.sweep(ctx, false)
}

// sweep enumerates managed routes and probes those that are due, or all of
// them when force is set.
func (s *RouteCanarySupervisor) sweep(ctx context.Context, force bool) {
	listed, err := s.source.ListManagedRoutePlans(ctx)
	s.scheduleMu.Lock()
	s.enumerationFailed = err != nil
	s.scheduleMu.Unlock()
	if err != nil {
		s.logger.Error("list managed route plans", zap.Error(err))
		return
	}
	plans := make([]*domain.DesiredPublicRoutePlan, 0, len(listed))
	for _, plan := range listed {
		if plan != nil {
			plans = append(plans, plan)
		}
	}
	sort.Slice(plans, func(i, j int) bool {
		return domain.RouteCanaryKeyForPlan(plans[i]).Coordinate() < domain.RouteCanaryKeyForPlan(plans[j]).Coordinate()
	})

	// Scheduling uses one instant for the whole sweep, so routes that share an
	// interval stay batched however long an individual probe takes.
	sweepAt := s.now()
	s.pruneSchedule(plans)
	for _, plan := range plans {
		if ctx.Err() != nil {
			return
		}
		coordinate := domain.RouteCanaryKeyForPlan(plan).Coordinate()
		interval := s.IntervalFor(plan)
		if !force && !s.isDue(coordinate, interval, sweepAt) {
			continue
		}
		// Schedule before probing, so a route whose evaluation errors waits a
		// full interval rather than being retried on every wake-up.
		s.schedule(coordinate, sweepAt.Add(interval))
		if err := s.EvaluatePlan(ctx, plan); err != nil {
			s.logger.Error("evaluate managed route",
				zap.String("route", coordinate),
				zap.Error(err))
		}
	}
}

// IntervalFor reports the periodic probe interval that applies to a route: its
// override when one is configured, otherwise the fleet-wide interval.
func (s *RouteCanarySupervisor) IntervalFor(plan *domain.DesiredPublicRoutePlan) time.Duration {
	if plan != nil {
		if interval, ok := s.evaluator.Policy().ProbeIntervalFor(plan.Hostname); ok {
			return interval
		}
	}
	return s.interval
}

// NextWakeDelay reports how long the periodic loop should wait before the next
// sweep, given the current instant.
//
// It is the time until the earliest due route, capped at the fleet-wide
// interval so newly added routes are still discovered on the fleet-wide
// cadence.
//
// A route can already be overdue when the sweep that scheduled it took longer
// than its interval, for example while several routes time out. The next sweep
// then starts at once, as the previous ticker did. The exception is a sweep that
// could not list routes at all: retrying a failing plan source immediately
// would only hammer it, so overdue entries are ignored and the loop backs off to
// the next future due time or the fleet-wide interval.
func (s *RouteCanarySupervisor) NextWakeDelay(now time.Time) time.Duration {
	delay := s.interval
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	for _, due := range s.nextDue {
		wait := due.Sub(now)
		if wait <= 0 && s.enumerationFailed {
			continue
		}
		if wait < delay {
			delay = wait
		}
	}
	if delay < minRouteCanaryWakeDelay {
		delay = minRouteCanaryWakeDelay
	}
	return delay
}

// isDue reports whether a route should be probed at the given instant.
func (s *RouteCanarySupervisor) isDue(coordinate string, interval time.Duration, now time.Time) bool {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	due, ok := s.nextDue[coordinate]
	if !ok {
		return true
	}
	// A due time further away than one interval can only mean the wall clock
	// stepped backwards. Probe now rather than going silent until the clock
	// catches up.
	if due.Sub(now) > interval {
		return true
	}
	return !now.Before(due.Add(-routeCanaryScheduleTolerance))
}

func (s *RouteCanarySupervisor) schedule(coordinate string, due time.Time) {
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	s.nextDue[coordinate] = due
}

// pruneSchedule forgets routes that are no longer in desired state, so a route
// that is withdrawn and later re-added is probed immediately.
func (s *RouteCanarySupervisor) pruneSchedule(plans []*domain.DesiredPublicRoutePlan) {
	current := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		current[domain.RouteCanaryKeyForPlan(plan).Coordinate()] = struct{}{}
	}
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	for coordinate := range s.nextDue {
		if _, ok := current[coordinate]; !ok {
			delete(s.nextDue, coordinate)
		}
	}
}

// EvaluatePlan probes one managed route and persists any resulting transition.
func (s *RouteCanarySupervisor) EvaluatePlan(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if plan == nil {
		return fmt.Errorf("route canary supervisor: plan is required")
	}
	key := domain.RouteCanaryKeyForPlan(plan)
	lock := s.lockFor(key.Coordinate())
	lock.Lock()
	defer lock.Unlock()

	now := s.now()
	verdict, ok, err := s.evaluator.Evaluate(ctx, plan, now)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	previous, err := s.repo.GetState(ctx, key)
	if err != nil {
		return err
	}
	prior := domain.RouteCanaryState{RouteCanaryKey: key}
	if previous != nil {
		prior = *previous
	}

	next, transition := domain.EvaluateRouteCanary(
		prior,
		verdict.Classification,
		verdict.Failing(),
		verdict.Perspective,
		verdict.Reason,
		verdict.TLSNotAfter,
		s.evaluator.Policy().Thresholds,
		now,
	)
	next.RouteCanaryKey = key

	if transition == domain.RouteCanaryTransitionNone {
		// Nothing operator-visible changed; still record the fresh observation
		// so staleness is visible and recovery streaks accumulate.
		return s.repo.UpsertState(ctx, &next)
	}

	instanceStatus := s.instanceStatus(ctx, key)
	event := domain.RouteCanaryEvent{
		RouteCanaryKey:         key,
		Transition:             transition,
		PreviousClassification: prior.Classification,
		Classification:         next.Classification,
		Perspective:            next.Perspective,
		Reason:                 next.FailureReason,
		Evidence:               summarizeRouteObservations(verdict.Observations),
		ObservedInstanceStatus: instanceStatus,
		ObservedAt:             now,
	}
	if err := s.repo.UpsertStateWithEvent(ctx, &next, &event); err != nil {
		return err
	}
	publishRouteCanaryTransition(ctx, s.publisher, next, event, instanceStatus, now)
	return nil
}

func (s *RouteCanarySupervisor) instanceStatus(ctx context.Context, key domain.RouteCanaryKey) domain.InstanceHealthStatus {
	if s.healthSource == nil {
		return ""
	}
	status, ok := s.healthSource.InstanceStatusForRoute(ctx, key)
	if !ok {
		return ""
	}
	return status
}

// routeCanaryTransitionEvent builds the in-process event announcing one
// persisted route canary transition. It reports false for a non-transition,
// which announces nothing.
//
// It is the only place a route transition becomes an event: the periodic
// supervisor and the post-deploy gate both publish through it, so the
// projector, notifications and alerting see identical event types, payloads
// and severities whichever of them observed the transition.
func routeCanaryTransitionEvent(
	state domain.RouteCanaryState,
	event domain.RouteCanaryEvent,
	instanceStatus domain.InstanceHealthStatus,
	now time.Time,
) (events.Event, bool) {
	var eventType events.EventType
	var severity domain.AlertSeverity
	switch event.Transition {
	case domain.RouteCanaryTransitionOpened:
		eventType = events.EventRouteCanaryOutageOpened
		severity = domain.AlertSeverityCritical
	case domain.RouteCanaryTransitionRecovered:
		eventType = events.EventRouteCanaryRecovered
		severity = domain.AlertSeverityInfo
	case domain.RouteCanaryTransitionClassificationChanged:
		eventType = events.EventRouteCanaryClassificationChanged
		severity = domain.AlertSeverityWarning
		if state.Open {
			severity = domain.AlertSeverityError
		}
	default:
		return events.Event{}, false
	}
	return events.Event{
		Type:     eventType,
		EntityID: state.Coordinate(),
		Data: RouteCanaryChanged{
			EventID:                event.ID.String(),
			State:                  state,
			Event:                  event,
			ObservedInstanceStatus: instanceStatus,
			Severity:               severity,
			Reason:                 state.FailureReason,
			OccurredAt:             now,
		},
	}, true
}

// publishRouteCanaryTransition publishes one persisted route canary transition.
// Callers publish only after the transition and its lineage are durable, so an
// announced transition always has a stored record behind it.
func publishRouteCanaryTransition(
	ctx context.Context,
	publisher events.Publisher,
	state domain.RouteCanaryState,
	event domain.RouteCanaryEvent,
	instanceStatus domain.InstanceHealthStatus,
	now time.Time,
) {
	if publisher == nil {
		return
	}
	if e, ok := routeCanaryTransitionEvent(state, event, instanceStatus, now); ok {
		publisher.Publish(ctx, e)
	}
}

func (s *RouteCanarySupervisor) lockFor(coordinate string) *sync.Mutex {
	s.routeLocksMu.Lock()
	defer s.routeLocksMu.Unlock()
	lock, ok := s.routeLocks[coordinate]
	if !ok {
		lock = &sync.Mutex{}
		s.routeLocks[coordinate] = lock
	}
	return lock
}

// summarizeRouteObservations renders bounded, sanitized per-perspective evidence.
func summarizeRouteObservations(observations []domain.RouteCanaryObservation) string {
	parts := make([]string, 0, len(observations))
	for _, observation := range observations {
		part := fmt.Sprintf("%s=", observation.Target.Perspective)
		switch {
		case !observation.Resolved:
			part += "unresolved"
		case observation.TLSError != "":
			part += "tls_error"
		case !observation.Connected:
			part += "no_response"
		default:
			part += fmt.Sprintf("http_%d", observation.StatusCode)
		}
		part += fmt.Sprintf(" in %dms", observation.Duration.Milliseconds())
		parts = append(parts, part)
	}
	summary := ""
	for i, part := range parts {
		if i > 0 {
			summary += "; "
		}
		summary += part
	}
	return domain.SanitizeEvidence(summary)
}
