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
}

// Failing reports whether the verdict represents a broken route.
func (v RouteCanaryVerdict) Failing() bool {
	return v.Classification.Failing()
}

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

	classification, perspective, ok := domain.ReduceRouteObservations(observations, now)
	if !ok {
		return RouteCanaryVerdict{}, false, nil
	}

	verdict := RouteCanaryVerdict{
		Key:            domain.RouteCanaryKeyForPlan(plan),
		Classification: classification,
		Perspective:    perspective,
		Observations:   observations,
	}
	for _, observation := range observations {
		if observation.Target.Perspective != perspective {
			continue
		}
		verdict.Reason = domain.DescribeRouteObservation(observation, classification, now)
		if observation.TLS.HandshakeCompleted && !observation.TLS.NotAfter.IsZero() {
			notAfter := observation.TLS.NotAfter
			verdict.TLSNotAfter = &notAfter
		}
		break
	}
	return verdict, true, nil
}

// RouteCanarySupervisor periodically probes every managed route and maintains
// durable outage state.
//
// The ticker is a health-check timer, which is the permitted use of a timer in
// this codebase: it schedules observation, it never waits for an event or infers
// completion from elapsed time.
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
	}, nil
}

// Run evaluates immediately and then on every tick until the context is done.
func (s *RouteCanarySupervisor) Run(ctx context.Context) error {
	s.EvaluateOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.EvaluateOnce(ctx)
		}
	}
}

// EvaluateOnce probes every managed route exactly once.
//
// A failure for one route never aborts the sweep: an unreachable route must not
// prevent the rest of the fleet from being observed.
func (s *RouteCanarySupervisor) EvaluateOnce(ctx context.Context) {
	plans, err := s.source.ListManagedRoutePlans(ctx)
	if err != nil {
		s.logger.Error("list managed route plans", zap.Error(err))
		return
	}
	sort.Slice(plans, func(i, j int) bool {
		return domain.RouteCanaryKeyForPlan(plans[i]).Coordinate() < domain.RouteCanaryKeyForPlan(plans[j]).Coordinate()
	})
	for _, plan := range plans {
		if ctx.Err() != nil {
			return
		}
		if err := s.EvaluatePlan(ctx, plan); err != nil {
			s.logger.Error("evaluate managed route",
				zap.String("route", domain.RouteCanaryKeyForPlan(plan).Coordinate()),
				zap.Error(err))
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
	s.publishTransition(ctx, next, event, instanceStatus, now)
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

func (s *RouteCanarySupervisor) publishTransition(
	ctx context.Context,
	state domain.RouteCanaryState,
	event domain.RouteCanaryEvent,
	instanceStatus domain.InstanceHealthStatus,
	now time.Time,
) {
	if s.publisher == nil {
		return
	}
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
		return
	}
	s.publisher.Publish(ctx, events.Event{
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
	})
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
