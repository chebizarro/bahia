package domain

import (
	"time"

	"github.com/google/uuid"
)

// RolloutStatus represents the lifecycle state of a rollout plan.
type RolloutStatus string

const (
	RolloutStatusPending    RolloutStatus = "pending"
	RolloutStatusRunning    RolloutStatus = "running"
	RolloutStatusCompleted  RolloutStatus = "completed"
	RolloutStatusFailed     RolloutStatus = "failed"
	RolloutStatusRolledBack RolloutStatus = "rolled_back"
)

// StepAction identifies what a rollout step does.
type StepAction string

const (
	StepActionDeployCanary  StepAction = "deploy_canary"
	StepActionShiftTraffic  StepAction = "shift_traffic"
	StepActionObserve       StepAction = "observe"
	StepActionPromote       StepAction = "promote"
	StepActionRollback      StepAction = "rollback"
	StepActionDeployGreen   StepAction = "deploy_green"
	StepActionSwitch        StepAction = "switch"
)

// StepStatus tracks the progress of an individual step.
type StepStatus string

const (
	StepStatusPending  StepStatus = "pending"
	StepStatusRunning  StepStatus = "running"
	StepStatusPassed   StepStatus = "passed"
	StepStatusFailed   StepStatus = "failed"
	StepStatusSkipped  StepStatus = "skipped"
)

// RolloutPlan describes a progressive delivery execution plan.
type RolloutPlan struct {
	ID                   uuid.UUID      `json:"id"`
	DeploymentIntentID   uuid.UUID      `json:"deployment_intent_id"`
	Strategy             DeployStrategy `json:"strategy"`
	CurrentStep          int            `json:"current_step"`
	Status               RolloutStatus  `json:"status"`
	StartedAt            *time.Time     `json:"started_at,omitempty"`
	CompletedAt          *time.Time     `json:"completed_at,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
}

// RolloutStep is a single step in a rollout plan.
type RolloutStep struct {
	ID            uuid.UUID         `json:"id"`
	RolloutPlanID uuid.UUID         `json:"rollout_plan_id"`
	StepOrder     int               `json:"step_order"`
	Action        StepAction        `json:"action"`
	Config        map[string]any    `json:"config,omitempty"`
	Status        StepStatus        `json:"status"`
	HealthResult  map[string]any    `json:"health_result,omitempty"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

// HealthGateConfig configures health checking between rollout steps.
type HealthGateConfig struct {
	Interval         time.Duration `json:"interval"`           // how often to poll health
	Timeout          time.Duration `json:"timeout"`            // max wait for health gate
	SuccessThreshold int           `json:"success_threshold"`  // consecutive healthy checks needed
	FailureThreshold int           `json:"failure_threshold"`  // consecutive failures to trigger rollback
}

// DefaultHealthGate returns a sensible default health gate configuration.
func DefaultHealthGate() HealthGateConfig {
	return HealthGateConfig{
		Interval:         10 * time.Second,
		Timeout:          5 * time.Minute,
		SuccessThreshold: 3,
		FailureThreshold: 2,
	}
}
