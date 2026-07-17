package rollout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// TrafficState is the runtime-reported result of a traffic transition.
type TrafficState struct {
	TargetSlot    string
	WeightPercent int
}

// TrafficController is an optional runtime capability for progressive traffic management.
// Runtimes that do not implement it cannot safely run canary or blue/green traffic steps.
type TrafficController interface {
	ShiftTraffic(ctx context.Context, serviceName, targetSlot string, weightPercent int) (TrafficState, error)
	SwitchTraffic(ctx context.Context, serviceName, fromSlot, toSlot string) (TrafficState, error)
}

// Executor drives rollout plans through their steps.
type Executor struct {
	repo       repository.RolloutPlanRepository
	healthGate *HealthGate
	rt         runtime.Runtime
	publisher  events.Publisher
	logger     *zap.Logger
}

// NewExecutor creates a new rollout executor.
func NewExecutor(
	repo repository.RolloutPlanRepository,
	observer runtime.Observer,
	rt runtime.Runtime,
	publisher events.Publisher,
	logger *zap.Logger,
) *Executor {
	return &Executor{
		repo:       repo,
		healthGate: NewHealthGate(observer, logger),
		rt:         rt,
		publisher:  publisher,
		logger:     logger,
	}
}

// CreateAndStart builds a rollout plan for the given strategy and begins execution.
func (e *Executor) CreateAndStart(ctx context.Context, intentID uuid.UUID, strategy domain.DeployStrategy, serviceName, image string) (*domain.RolloutPlan, error) {
	previousArtifact, err := e.captureArtifact(ctx, serviceName)
	if err != nil {
		return nil, fmt.Errorf("snapshotting previous deployment for rollback: %w", err)
	}
	plan, steps := BuildPlan(intentID, strategy)

	if err := e.repo.CreatePlan(ctx, plan); err != nil {
		return nil, fmt.Errorf("creating rollout plan: %w", err)
	}
	if err := e.repo.CreateSteps(ctx, steps); err != nil {
		return nil, fmt.Errorf("creating rollout steps: %w", err)
	}

	// Mark as running.
	now := time.Now().UTC()
	plan.Status = domain.RolloutStatusRunning
	plan.StartedAt = &now
	if err := e.repo.UpdatePlan(ctx, plan); err != nil {
		return nil, fmt.Errorf("starting rollout plan: %w", err)
	}

	e.publisher.Publish(ctx, events.Event{
		Type:     "rollout.started",
		EntityID: plan.ID.String(),
		Data: map[string]string{
			"strategy":  string(strategy),
			"intent_id": intentID.String(),
		},
	})

	// Execute steps sequentially.
	go e.executeSteps(context.Background(), plan, steps, serviceName, image, previousArtifact)

	return plan, nil
}

// executeSteps runs through the rollout steps in order.
func (e *Executor) executeSteps(ctx context.Context, plan *domain.RolloutPlan, steps []domain.RolloutStep, serviceName, image, previousArtifact string) {
	for i := range steps {
		step := &steps[i]

		plan.CurrentStep = step.StepOrder
		_ = e.repo.UpdatePlan(ctx, plan)

		now := time.Now().UTC()
		step.StartedAt = &now
		step.Status = domain.StepStatusRunning
		_ = e.repo.UpdateStep(ctx, step)

		e.logger.Info("executing rollout step",
			zap.String("plan_id", plan.ID.String()),
			zap.Int("step", step.StepOrder),
			zap.String("action", string(step.Action)),
		)

		err := e.executeStep(ctx, step, serviceName, image)
		completed := time.Now().UTC()
		step.CompletedAt = &completed

		if err != nil {
			step.Status = domain.StepStatusFailed
			step.HealthResult = map[string]any{"error": err.Error()}
			_ = e.repo.UpdateStep(ctx, step)

			e.logger.Error("rollout step failed, initiating rollback",
				zap.String("plan_id", plan.ID.String()),
				zap.Int("step", step.StepOrder),
				zap.Error(err),
			)

			if rollbackErr := e.autoRollback(ctx, plan, serviceName, previousArtifact); rollbackErr != nil {
				e.logger.Error("rollout auto-rollback failed",
					zap.String("plan_id", plan.ID.String()),
					zap.Error(rollbackErr),
				)
			}
			return
		}

		step.Status = domain.StepStatusPassed
		_ = e.repo.UpdateStep(ctx, step)

		e.publisher.Publish(ctx, events.Event{
			Type:     "rollout.step_completed",
			EntityID: plan.ID.String(),
			Data: map[string]any{
				"step":   step.StepOrder,
				"action": string(step.Action),
			},
		})
	}

	// All steps passed — mark plan as completed.
	completed := time.Now().UTC()
	plan.Status = domain.RolloutStatusCompleted
	plan.CompletedAt = &completed
	_ = e.repo.UpdatePlan(ctx, plan)

	e.publisher.Publish(ctx, events.Event{
		Type:     "rollout.completed",
		EntityID: plan.ID.String(),
	})

	e.logger.Info("rollout completed successfully",
		zap.String("plan_id", plan.ID.String()),
		zap.String("strategy", string(plan.Strategy)),
	)
}

func (e *Executor) executeStep(ctx context.Context, step *domain.RolloutStep, serviceName, image string) error {
	switch step.Action {
	case domain.StepActionDeployCanary:
		// Deploy the canary, then make its configured traffic weight real before passing the step.
		canaryName := serviceName + "-canary"
		if err := e.rt.Deploy(ctx, canaryName, image, runtime.DeployOptions{
			Labels: map[string]string{
				"bahia.service": serviceName,
				"bahia.slot":    "canary",
			},
		}); err != nil {
			return err
		}
		weight := 10
		if configured, ok := step.Config["weight"].(float64); ok {
			weight = int(configured)
		}
		return e.shiftTraffic(ctx, serviceName, "canary", weight)

	case domain.StepActionDeployGreen:
		// Deploy green instance alongside blue.
		greenName := serviceName + "-green"
		return e.rt.Deploy(ctx, greenName, image, runtime.DeployOptions{
			Labels: map[string]string{
				"bahia.service": serviceName,
				"bahia.slot":    "green",
			},
		})

	case domain.StepActionObserve:
		// Run health gate.
		cfg := domain.DefaultHealthGate()
		if durSec, ok := step.Config["duration_seconds"].(float64); ok {
			cfg.Timeout = time.Duration(durSec) * time.Second
		}
		if st, ok := step.Config["success_threshold"].(float64); ok {
			cfg.SuccessThreshold = int(st)
		}
		if ft, ok := step.Config["failure_threshold"].(float64); ok {
			cfg.FailureThreshold = int(ft)
		}

		// Determine which service to observe.
		observeName := serviceName
		if slot, ok := step.Config["target_slot"].(string); ok {
			observeName = serviceName + "-" + slot
		}

		result, err := e.healthGate.Check(ctx, uuid.Nil, uuid.Nil, observeName, cfg)
		if err != nil {
			return fmt.Errorf("health gate error: %w", err)
		}

		// Store health result in step.
		step.HealthResult = map[string]any{
			"passed":           result.Passed,
			"total_checks":     result.TotalChecks,
			"healthy_checks":   result.HealthyChecks,
			"unhealthy_checks": result.UnhealthyChecks,
			"last_health":      string(result.LastHealth),
			"duration_ms":      result.Duration.Milliseconds(),
		}
		if result.Error != "" {
			step.HealthResult["error"] = result.Error
		}

		if !result.Passed {
			return fmt.Errorf("health gate failed: %s", result.Error)
		}
		return nil

	case domain.StepActionShiftTraffic:
		weight := 50
		if w, ok := step.Config["weight"].(float64); ok {
			weight = int(w)
		}
		if weight < 0 || weight > 100 {
			return fmt.Errorf("invalid traffic weight %d", weight)
		}
		targetSlot := "canary"
		if configured, ok := step.Config["target_slot"].(string); ok && strings.TrimSpace(configured) != "" {
			targetSlot = strings.TrimSpace(configured)
		}
		return e.shiftTraffic(ctx, serviceName, targetSlot, weight)

	case domain.StepActionSwitch:
		controller, err := e.trafficController()
		if err != nil {
			return err
		}
		from, _ := step.Config["from"].(string)
		to, _ := step.Config["to"].(string)
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" {
			return fmt.Errorf("blue/green switch requires non-empty from and to slots")
		}
		state, err := controller.SwitchTraffic(ctx, serviceName, from, to)
		if err != nil {
			return fmt.Errorf("switching traffic from %s to %s: %w", from, to, err)
		}
		if state.TargetSlot != to || state.WeightPercent != 100 {
			return fmt.Errorf("traffic switch verification failed: requested %s, runtime reports %s at %d%%", to, state.TargetSlot, state.WeightPercent)
		}
		return nil

	case domain.StepActionPromote:
		// Final promotion: deploy to the primary service name, clean up canary/green.
		if err := e.rt.Deploy(ctx, serviceName, image, runtime.DeployOptions{
			Labels:     map[string]string{"bahia.service": serviceName},
			PullAlways: true,
		}); err != nil {
			return fmt.Errorf("promoting deployment: %w", err)
		}
		if fromSlot, ok := step.Config["from_slot"].(string); ok && strings.TrimSpace(fromSlot) != "" {
			controller, err := e.trafficController()
			if err != nil {
				return fmt.Errorf("routing promoted primary: %w", err)
			}
			state, err := controller.SwitchTraffic(ctx, serviceName, strings.TrimSpace(fromSlot), "primary")
			if err != nil {
				return fmt.Errorf("routing promoted primary: %w", err)
			}
			if state.TargetSlot != "primary" || state.WeightPercent != 100 {
				return fmt.Errorf("promoted traffic verification failed: runtime reports %s at %d%%", state.TargetSlot, state.WeightPercent)
			}
		}
		return e.cleanupSlots(ctx, serviceName)

	case domain.StepActionRollback:
		return e.cleanupSlots(ctx, serviceName)

	default:
		return fmt.Errorf("unknown step action: %s", step.Action)
	}
}

func (e *Executor) autoRollback(ctx context.Context, plan *domain.RolloutPlan, serviceName, previousArtifact string) error {
	rollbackErr := errors.Join(
		e.restoreTraffic(ctx, plan.Strategy, serviceName),
		e.restorePrimary(ctx, serviceName, previousArtifact),
		e.cleanupSlots(ctx, serviceName),
	)
	if rollbackErr != nil {
		return e.markRollbackFailed(ctx, plan, rollbackErr)
	}

	completed := time.Now().UTC()
	plan.Status = domain.RolloutStatusRolledBack
	plan.CompletedAt = &completed
	if err := e.repo.UpdatePlan(ctx, plan); err != nil {
		return e.markRollbackFailed(ctx, plan, fmt.Errorf("persisting rolled-back plan: %w", err))
	}

	e.publisher.Publish(ctx, events.Event{
		Type:     "rollout.rolled_back",
		EntityID: plan.ID.String(),
	})
	e.logger.Warn("rollout auto-rolled back",
		zap.String("plan_id", plan.ID.String()),
		zap.String("strategy", string(plan.Strategy)),
	)
	return nil
}

func (e *Executor) shiftTraffic(ctx context.Context, serviceName, targetSlot string, weight int) error {
	if weight < 0 || weight > 100 {
		return fmt.Errorf("invalid traffic weight %d", weight)
	}
	controller, err := e.trafficController()
	if err != nil {
		return err
	}
	state, err := controller.ShiftTraffic(ctx, serviceName, targetSlot, weight)
	if err != nil {
		return fmt.Errorf("shifting traffic to %s at %d%%: %w", targetSlot, weight, err)
	}
	if state.TargetSlot != targetSlot || state.WeightPercent != weight {
		return fmt.Errorf("traffic shift verification failed: requested %s at %d%%, runtime reports %s at %d%%", targetSlot, weight, state.TargetSlot, state.WeightPercent)
	}
	return nil
}

func (e *Executor) trafficController() (TrafficController, error) {
	controller, ok := e.rt.(TrafficController)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support verifiable traffic transitions", e.rt.Type())
	}
	return controller, nil
}

func (e *Executor) restoreTraffic(ctx context.Context, strategy domain.DeployStrategy, serviceName string) error {
	if strategy == domain.DeployStrategyReplace {
		return nil
	}
	controller, err := e.trafficController()
	if err != nil {
		return fmt.Errorf("restoring traffic: %w", err)
	}
	var state TrafficState
	switch strategy {
	case domain.DeployStrategyCanary:
		state, err = controller.ShiftTraffic(ctx, serviceName, "canary", 0)
		if err == nil && (state.TargetSlot != "canary" || state.WeightPercent != 0) {
			err = fmt.Errorf("runtime reports %s at %d%%", state.TargetSlot, state.WeightPercent)
		}
	case domain.DeployStrategyBlueGreen:
		state, err = controller.SwitchTraffic(ctx, serviceName, "green", "blue")
		if err == nil && (state.TargetSlot != "blue" || state.WeightPercent != 100) {
			err = fmt.Errorf("runtime reports %s at %d%%", state.TargetSlot, state.WeightPercent)
		}
	default:
		return fmt.Errorf("unsupported rollback strategy %s", strategy)
	}
	if err != nil {
		return fmt.Errorf("restoring traffic for %s rollout: %w", strategy, err)
	}
	return nil
}

func (e *Executor) captureArtifact(ctx context.Context, serviceName string) (string, error) {
	obs, err := e.healthGate.observer.Observe(ctx, uuid.Nil, uuid.Nil, serviceName)
	if err != nil {
		return "", err
	}
	if obs == nil {
		return "", fmt.Errorf("runtime returned no observation")
	}
	if obs.HealthStatus == domain.HealthStatusStopped {
		return "", nil
	}
	return observedArtifact(obs), nil
}

func (e *Executor) restorePrimary(ctx context.Context, serviceName, previousArtifact string) error {
	if previousArtifact == "" {
		if err := e.rt.Undeploy(ctx, serviceName); err != nil {
			return fmt.Errorf("removing newly-created primary deployment: %w", err)
		}
		obs, err := e.healthGate.observer.Observe(ctx, uuid.Nil, uuid.Nil, serviceName)
		if err != nil {
			return fmt.Errorf("verifying primary removal: %w", err)
		}
		if obs == nil || obs.HealthStatus != domain.HealthStatusStopped {
			return fmt.Errorf("verifying primary removal: runtime still reports a deployment")
		}
		return nil
	}

	obs, observeErr := e.healthGate.observer.Observe(ctx, uuid.Nil, uuid.Nil, serviceName)
	if observeErr != nil || obs == nil || observedArtifact(obs) != previousArtifact || obs.HealthStatus != domain.HealthStatusHealthy {
		if err := e.rt.Deploy(ctx, serviceName, previousArtifact, runtime.DeployOptions{
			Labels:     map[string]string{"bahia.service": serviceName, "bahia.rollback": "true"},
			PullAlways: true,
		}); err != nil {
			return fmt.Errorf("restoring previous primary artifact %s: %w", previousArtifact, err)
		}
		obs, observeErr = e.healthGate.observer.Observe(ctx, uuid.Nil, uuid.Nil, serviceName)
	}
	if observeErr != nil {
		return fmt.Errorf("verifying restored primary: %w", observeErr)
	}
	if obs == nil {
		return fmt.Errorf("verifying restored primary: runtime returned no observation")
	}
	if actual := observedArtifact(obs); actual != previousArtifact {
		return fmt.Errorf("verifying restored primary: expected %s, observed %s", previousArtifact, actual)
	}
	if obs.HealthStatus != domain.HealthStatusHealthy {
		return fmt.Errorf("verifying restored primary: health is %s", obs.HealthStatus)
	}
	return nil
}

func observedArtifact(obs *domain.RuntimeObservation) string {
	if obs == nil {
		return ""
	}
	repo := strings.TrimSpace(obs.ObservedImageRepo)
	digest := strings.TrimSpace(obs.ObservedImageDigest)
	if digest == "" || strings.Contains(repo, "@") {
		return repo
	}
	if lastSlash, lastColon := strings.LastIndex(repo, "/"), strings.LastIndex(repo, ":"); lastColon > lastSlash {
		repo = repo[:lastColon]
	}
	if repo == "" {
		return digest
	}
	return repo + "@" + digest
}

func (e *Executor) cleanupSlots(ctx context.Context, serviceName string) error {
	return errors.Join(
		wrapRuntimeError("undeploying canary slot", e.rt.Undeploy(ctx, serviceName+"-canary")),
		wrapRuntimeError("undeploying green slot", e.rt.Undeploy(ctx, serviceName+"-green")),
	)
}

func wrapRuntimeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (e *Executor) markRollbackFailed(ctx context.Context, plan *domain.RolloutPlan, cause error) error {
	completed := time.Now().UTC()
	plan.Status = domain.RolloutStatusFailed
	plan.CompletedAt = &completed
	persistErr := e.repo.UpdatePlan(ctx, plan)
	resultErr := cause
	if persistErr != nil {
		resultErr = errors.Join(cause, fmt.Errorf("persisting rollback failure: %w", persistErr))
	}
	e.publisher.Publish(ctx, events.Event{
		Type:     "rollout.rollback_failed",
		EntityID: plan.ID.String(),
		Data:     map[string]string{"error": resultErr.Error()},
	})
	return resultErr
}

// GetPlanStatus returns the current plan and its steps.
func (e *Executor) GetPlanStatus(ctx context.Context, planID uuid.UUID) (*domain.RolloutPlan, []domain.RolloutStep, error) {
	plan, err := e.repo.GetPlanByID(ctx, planID)
	if err != nil {
		return nil, nil, err
	}
	if plan == nil {
		return nil, nil, nil
	}

	steps, err := e.repo.ListStepsByPlan(ctx, planID)
	if err != nil {
		return nil, nil, err
	}

	return plan, steps, nil
}
