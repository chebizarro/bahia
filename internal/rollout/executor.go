package rollout

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// Executor drives rollout plans through their steps.
type Executor struct {
	repo        repository.RolloutPlanRepository
	healthGate  *HealthGate
	rt          runtime.Runtime
	publisher   events.Publisher
	logger      *zap.Logger
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
	go e.executeSteps(context.Background(), plan, steps, serviceName, image)

	return plan, nil
}

// executeSteps runs through the rollout steps in order.
func (e *Executor) executeSteps(ctx context.Context, plan *domain.RolloutPlan, steps []domain.RolloutStep, serviceName, image string) {
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

			// Auto-rollback: undeploy the canary/green.
			e.autoRollback(ctx, plan, serviceName)
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
		// Deploy canary instance alongside the existing deployment.
		canaryName := serviceName + "-canary"
		return e.rt.Deploy(ctx, canaryName, image, runtime.DeployOptions{
			Labels: map[string]string{
				"bahia.service": serviceName,
				"bahia.slot":    "canary",
			},
		})

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
			"passed":          result.Passed,
			"total_checks":    result.TotalChecks,
			"healthy_checks":  result.HealthyChecks,
			"unhealthy_checks": result.UnhealthyChecks,
			"last_health":     string(result.LastHealth),
			"duration_ms":     result.Duration.Milliseconds(),
		}
		if result.Error != "" {
			step.HealthResult["error"] = result.Error
		}

		if !result.Passed {
			return fmt.Errorf("health gate failed: %s", result.Error)
		}
		return nil

	case domain.StepActionShiftTraffic:
		// Traffic shifting is runtime-specific. Log the intent.
		weight := 50
		if w, ok := step.Config["weight"].(float64); ok {
			weight = int(w)
		}
		e.logger.Info("traffic shift requested",
			zap.String("service", serviceName),
			zap.Int("weight_percent", weight),
		)
		// In a production system, this would configure a load balancer or service mesh.
		return nil

	case domain.StepActionSwitch:
		// Blue-green switch: traffic goes to green.
		e.logger.Info("blue-green switch",
			zap.String("service", serviceName),
			zap.Any("from", step.Config["from"]),
			zap.Any("to", step.Config["to"]),
		)
		return nil

	case domain.StepActionPromote:
		// Final promotion: deploy to the primary service name, clean up canary/green.
		if err := e.rt.Deploy(ctx, serviceName, image, runtime.DeployOptions{
			Labels:     map[string]string{"bahia.service": serviceName},
			PullAlways: true,
		}); err != nil {
			return fmt.Errorf("promoting deployment: %w", err)
		}

		// Cleanup canary/green slots.
		_ = e.rt.Undeploy(ctx, serviceName+"-canary")
		_ = e.rt.Undeploy(ctx, serviceName+"-green")
		return nil

	case domain.StepActionRollback:
		// Remove canary/green and leave the original running.
		_ = e.rt.Undeploy(ctx, serviceName+"-canary")
		_ = e.rt.Undeploy(ctx, serviceName+"-green")
		return nil

	default:
		return fmt.Errorf("unknown step action: %s", step.Action)
	}
}

func (e *Executor) autoRollback(ctx context.Context, plan *domain.RolloutPlan, serviceName string) {
	// Undeploy canary/green slots.
	_ = e.rt.Undeploy(ctx, serviceName+"-canary")
	_ = e.rt.Undeploy(ctx, serviceName+"-green")

	completed := time.Now().UTC()
	plan.Status = domain.RolloutStatusRolledBack
	plan.CompletedAt = &completed
	_ = e.repo.UpdatePlan(ctx, plan)

	e.publisher.Publish(ctx, events.Event{
		Type:     "rollout.rolled_back",
		EntityID: plan.ID.String(),
	})

	e.logger.Warn("rollout auto-rolled back",
		zap.String("plan_id", plan.ID.String()),
		zap.String("strategy", string(plan.Strategy)),
	)
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
