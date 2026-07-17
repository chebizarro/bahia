// Package rollout implements progressive delivery strategies (canary, blue-green).
package rollout

import (
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// BuildPlan creates a rollout plan with ordered steps for the given strategy.
func BuildPlan(intentID uuid.UUID, strategy domain.DeployStrategy) (*domain.RolloutPlan, []domain.RolloutStep) {
	planID := uuid.New()
	plan := &domain.RolloutPlan{
		ID:                 planID,
		DeploymentIntentID: intentID,
		Strategy:           strategy,
		CurrentStep:        0,
		Status:             domain.RolloutStatusPending,
	}

	var steps []domain.RolloutStep

	switch strategy {
	case domain.DeployStrategyCanary:
		steps = buildCanarySteps(planID)
	case domain.DeployStrategyBlueGreen:
		steps = buildBlueGreenSteps(planID)
	default:
		// Replace strategy: single deploy step, no progressive rollout.
		steps = []domain.RolloutStep{
			{
				ID:            uuid.New(),
				RolloutPlanID: planID,
				StepOrder:     0,
				Action:        domain.StepActionPromote,
				Status:        domain.StepStatusPending,
				Config:        map[string]any{"weight": 100},
			},
		}
	}

	return plan, steps
}

// buildCanarySteps creates the step sequence for a canary deployment:
// 1. Deploy canary (10% traffic)
// 2. Observe (health gate)
// 3. Shift traffic to 50%
// 4. Observe (health gate)
// 5. Promote to 100%
func buildCanarySteps(planID uuid.UUID) []domain.RolloutStep {
	return []domain.RolloutStep{
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 0,
			Action: domain.StepActionDeployCanary,
			Status: domain.StepStatusPending,
			Config: map[string]any{"weight": 10},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 1,
			Action: domain.StepActionObserve,
			Status: domain.StepStatusPending,
			Config: map[string]any{
				"duration_seconds":  60,
				"target_slot":       "canary",
				"success_threshold": 3,
				"failure_threshold": 2,
			},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 2,
			Action: domain.StepActionShiftTraffic,
			Status: domain.StepStatusPending,
			Config: map[string]any{"weight": 50},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 3,
			Action: domain.StepActionObserve,
			Status: domain.StepStatusPending,
			Config: map[string]any{
				"duration_seconds":  120,
				"target_slot":       "canary",
				"success_threshold": 3,
				"failure_threshold": 2,
			},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 4,
			Action: domain.StepActionPromote,
			Status: domain.StepStatusPending,
			Config: map[string]any{"weight": 100, "from_slot": "canary"},
		},
	}
}

// buildBlueGreenSteps creates the step sequence for a blue-green deployment:
// 1. Deploy green (new version alongside blue)
// 2. Observe (health gate on green)
// 3. Switch traffic to green
// 4. Observe (post-switch health gate)
// 5. Promote (clean up blue)
func buildBlueGreenSteps(planID uuid.UUID) []domain.RolloutStep {
	return []domain.RolloutStep{
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 0,
			Action: domain.StepActionDeployGreen,
			Status: domain.StepStatusPending,
			Config: map[string]any{"slot": "green"},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 1,
			Action: domain.StepActionObserve,
			Status: domain.StepStatusPending,
			Config: map[string]any{
				"duration_seconds":  60,
				"target_slot":       "green",
				"success_threshold": 3,
				"failure_threshold": 2,
			},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 2,
			Action: domain.StepActionSwitch,
			Status: domain.StepStatusPending,
			Config: map[string]any{"from": "blue", "to": "green"},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 3,
			Action: domain.StepActionObserve,
			Status: domain.StepStatusPending,
			Config: map[string]any{
				"duration_seconds":  30,
				"success_threshold": 2,
				"failure_threshold": 1,
			},
		},
		{
			ID: uuid.New(), RolloutPlanID: planID, StepOrder: 4,
			Action: domain.StepActionPromote,
			Status: domain.StepStatusPending,
			Config: map[string]any{"cleanup_slot": "blue", "from_slot": "green"},
		},
	}
}
