package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/routing"
	"github.com/openagentsinc/bahia/internal/domain"
)

// RouteApplier applies a reviewed route plan and can undo a successful apply.
type RouteApplier interface {
	ApplyWithCompensation(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (routing.Compensation, error)
}

// RouteCanaryGateConfig bounds the post-deploy verification window.
type RouteCanaryGateConfig struct {
	// Timeout bounds the whole gate, including retries.
	Timeout time.Duration
	// RetryInterval is the delay between verification attempts while the route
	// is still converging.
	RetryInterval time.Duration
}

// Normalized fills unset values with safe defaults.
func (c RouteCanaryGateConfig) Normalized() RouteCanaryGateConfig {
	normalized := c
	if normalized.Timeout <= 0 {
		normalized.Timeout = 90 * time.Second
	}
	if normalized.RetryInterval <= 0 {
		normalized.RetryInterval = 3 * time.Second
	}
	return normalized
}

// RouteCanaryGate applies a route and proves it actually serves traffic before
// the deployment is allowed to succeed.
//
// This closes the gap the git.sharegap.net outage exposed. Converging a provider
// only proves configuration was accepted: nginx reported a valid config and a
// successful reload while serving 502 from a stale upstream. The gate requires
// an end-to-end observation from every configured perspective, and withdraws the
// route when that observation does not come.
type RouteCanaryGate struct {
	applier      RouteApplier
	evaluator    *RouteCanaryEvaluator
	repo         RouteCanaryRepository
	healthSource RouteInstanceHealthSource
	cfg          RouteCanaryGateConfig
	logger       *zap.Logger
	now          func() time.Time
}

// NewRouteCanaryGate builds a post-deploy route gate.
func NewRouteCanaryGate(
	applier RouteApplier,
	evaluator *RouteCanaryEvaluator,
	repo RouteCanaryRepository,
	healthSource RouteInstanceHealthSource,
	cfg RouteCanaryGateConfig,
	logger *zap.Logger,
) (*RouteCanaryGate, error) {
	if applier == nil {
		return nil, fmt.Errorf("route canary gate requires a route applier")
	}
	if evaluator == nil {
		return nil, fmt.Errorf("route canary gate requires an evaluator")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &RouteCanaryGate{
		applier:      applier,
		evaluator:    evaluator,
		repo:         repo,
		healthSource: healthSource,
		cfg:          cfg.Normalized(),
		logger:       logger.Named("route-canary-gate"),
		now:          func() time.Time { return time.Now().UTC() },
	}, nil
}

// ErrRouteCanaryGateFailed marks a deployment failure caused by the route not
// serving traffic, as distinct from a provider apply failure.
var ErrRouteCanaryGateFailed = errors.New("route canary gate failed")

// Apply applies the route plan and verifies it end to end.
//
// On verification failure the route is withdrawn through the backend's own
// compensation and the returned error names the classification, so the operator
// sees whether the origin was broken, DNS was missing, or TLS was untrusted.
func (g *RouteCanaryGate) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if plan == nil {
		return fmt.Errorf("route canary gate: plan is required")
	}

	compensation, err := g.applier.ApplyWithCompensation(ctx, plan)
	if err != nil {
		return err
	}

	verdict, verified, verifyErr := g.verify(ctx, plan)
	if verifyErr == nil && (!verified || !verdict.Failing()) {
		// Either the policy derives no targets for this route, or every
		// perspective passed. Record the healthy observation and keep the route.
		if verified {
			g.record(ctx, plan, verdict)
		}
		return nil
	}

	failure := verifyErr
	if failure == nil {
		g.record(ctx, plan, verdict)
		failure = fmt.Errorf("%w: %s", ErrRouteCanaryGateFailed, verdict.Reason)
	}

	// Withdraw the route on a context that outlives a canceled deployment, so a
	// canceled run cannot strand a route that was proven broken.
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	if compensationErr := compensation(rollbackCtx); compensationErr != nil {
		return errors.Join(failure, fmt.Errorf("roll back route after canary failure: %w", compensationErr))
	}
	return fmt.Errorf("%w; previous public route restored", failure)
}

// verify probes the route until it passes or the gate deadline expires.
//
// The loop is deadline-driven and cancellable rather than sleep-driven: a route
// legitimately takes time to propagate, but the gate must never conclude success
// merely because time elapsed.
func (g *RouteCanaryGate) verify(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (RouteCanaryVerdict, bool, error) {
	gateCtx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	var (
		lastVerdict  RouteCanaryVerdict
		lastVerified bool
	)
	for {
		verdict, verified, err := g.evaluator.Evaluate(gateCtx, plan, g.now())
		if err != nil {
			// A probe construction fault is a configuration error, not a route
			// verdict, and retrying it cannot help.
			if ctxErr := gateCtx.Err(); ctxErr == nil {
				return RouteCanaryVerdict{}, false, err
			}
		} else {
			lastVerdict, lastVerified = verdict, verified
			if !verified || !verdict.Failing() {
				return verdict, verified, nil
			}
		}

		timer := time.NewTimer(g.cfg.RetryInterval)
		select {
		case <-gateCtx.Done():
			timer.Stop()
			if lastVerified {
				return lastVerdict, true, nil
			}
			return RouteCanaryVerdict{}, false, fmt.Errorf("%w: route did not serve traffic within %s",
				ErrRouteCanaryGateFailed, g.cfg.Timeout)
		case <-timer.C:
		}
	}
}

// record persists the gate's observation so a blocked deployment leaves durable,
// operator-visible lineage rather than only a run error string.
func (g *RouteCanaryGate) record(ctx context.Context, plan *domain.DesiredPublicRoutePlan, verdict RouteCanaryVerdict) {
	if g.repo == nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	key := domain.RouteCanaryKeyForPlan(plan)
	prior := domain.RouteCanaryState{RouteCanaryKey: key}
	if existing, err := g.repo.GetState(recordCtx, key); err == nil && existing != nil {
		prior = *existing
	}

	now := g.now()
	next, transition := domain.EvaluateRouteCanary(
		prior,
		verdict.Classification,
		verdict.Perspective,
		verdict.Reason,
		verdict.TLSNotAfter,
		// A post-deploy gate has already retried to its deadline, so a failing
		// verdict here is conclusive and opens immediately rather than waiting
		// for the periodic supervisor to accumulate a streak.
		domain.RouteCanaryThresholds{FailureThreshold: 1, SuccessThreshold: 1},
		now,
	)
	next.RouteCanaryKey = key

	var instanceStatus domain.InstanceHealthStatus
	if g.healthSource != nil {
		if status, ok := g.healthSource.InstanceStatusForRoute(recordCtx, key); ok {
			instanceStatus = status
		}
	}

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
	if err := g.repo.UpsertStateWithEvent(recordCtx, &next, &event); err != nil {
		g.logger.Error("record route canary gate outcome",
			zap.String("route", key.Coordinate()), zap.Error(err))
	}
}
