package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgRolloutPlanRepository is a PostgreSQL implementation of RolloutPlanRepository.
type PgRolloutPlanRepository struct {
	pool *pgxpool.Pool
}

// NewPgRolloutPlanRepository creates a new PgRolloutPlanRepository.
func NewPgRolloutPlanRepository(pool *pgxpool.Pool) *PgRolloutPlanRepository {
	return &PgRolloutPlanRepository{pool: pool}
}

func (r *PgRolloutPlanRepository) CreatePlan(ctx context.Context, plan *domain.RolloutPlan) error {
	if plan.ID == uuid.Nil {
		plan.ID = uuid.New()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO rollout_plans (id, deployment_intent_id, strategy, current_step, status, started_at, completed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
	`, plan.ID, plan.DeploymentIntentID, string(plan.Strategy), plan.CurrentStep,
		string(plan.Status), plan.StartedAt, plan.CompletedAt)
	if err != nil {
		return fmt.Errorf("creating rollout plan: %w", err)
	}
	return nil
}

func (r *PgRolloutPlanRepository) GetPlanByID(ctx context.Context, id uuid.UUID) (*domain.RolloutPlan, error) {
	plan := &domain.RolloutPlan{}
	var strategy, status string
	err := r.pool.QueryRow(ctx, `
		SELECT id, deployment_intent_id, strategy, current_step, status, started_at, completed_at, created_at
		FROM rollout_plans WHERE id = $1
	`, id).Scan(&plan.ID, &plan.DeploymentIntentID, &strategy, &plan.CurrentStep,
		&status, &plan.StartedAt, &plan.CompletedAt, &plan.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting rollout plan: %w", err)
	}
	plan.Strategy = domain.DeployStrategy(strategy)
	plan.Status = domain.RolloutStatus(status)
	return plan, nil
}

func (r *PgRolloutPlanRepository) GetPlanByIntent(ctx context.Context, intentID uuid.UUID) (*domain.RolloutPlan, error) {
	plan := &domain.RolloutPlan{}
	var strategy, status string
	err := r.pool.QueryRow(ctx, `
		SELECT id, deployment_intent_id, strategy, current_step, status, started_at, completed_at, created_at
		FROM rollout_plans WHERE deployment_intent_id = $1
		ORDER BY created_at DESC LIMIT 1
	`, intentID).Scan(&plan.ID, &plan.DeploymentIntentID, &strategy, &plan.CurrentStep,
		&status, &plan.StartedAt, &plan.CompletedAt, &plan.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting rollout plan by intent: %w", err)
	}
	plan.Strategy = domain.DeployStrategy(strategy)
	plan.Status = domain.RolloutStatus(status)
	return plan, nil
}

func (r *PgRolloutPlanRepository) UpdatePlan(ctx context.Context, plan *domain.RolloutPlan) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE rollout_plans SET current_step = $1, status = $2, started_at = $3, completed_at = $4
		WHERE id = $5
	`, plan.CurrentStep, string(plan.Status), plan.StartedAt, plan.CompletedAt, plan.ID)
	if err != nil {
		return fmt.Errorf("updating rollout plan: %w", err)
	}
	return nil
}

func (r *PgRolloutPlanRepository) ListActivePlans(ctx context.Context) ([]domain.RolloutPlan, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, deployment_intent_id, strategy, current_step, status, started_at, completed_at, created_at
		FROM rollout_plans WHERE status IN ('pending', 'running')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("listing active plans: %w", err)
	}
	defer rows.Close()

	var plans []domain.RolloutPlan
	for rows.Next() {
		var p domain.RolloutPlan
		var strategy, status string
		if err := rows.Scan(&p.ID, &p.DeploymentIntentID, &strategy, &p.CurrentStep,
			&status, &p.StartedAt, &p.CompletedAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning plan: %w", err)
		}
		p.Strategy = domain.DeployStrategy(strategy)
		p.Status = domain.RolloutStatus(status)
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func (r *PgRolloutPlanRepository) CreateStep(ctx context.Context, step *domain.RolloutStep) error {
	if step.ID == uuid.Nil {
		step.ID = uuid.New()
	}
	configJSON, _ := json.Marshal(step.Config)
	healthJSON, _ := json.Marshal(step.HealthResult)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO rollout_steps (id, rollout_plan_id, step_order, action, config, status, health_result, started_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, step.ID, step.RolloutPlanID, step.StepOrder, string(step.Action),
		configJSON, string(step.Status), healthJSON, step.StartedAt, step.CompletedAt)
	if err != nil {
		return fmt.Errorf("creating step: %w", err)
	}
	return nil
}

func (r *PgRolloutPlanRepository) CreateSteps(ctx context.Context, steps []domain.RolloutStep) error {
	batch := &pgx.Batch{}
	for _, step := range steps {
		if step.ID == uuid.Nil {
			step.ID = uuid.New()
		}
		configJSON, _ := json.Marshal(step.Config)
		healthJSON, _ := json.Marshal(step.HealthResult)

		batch.Queue(`
			INSERT INTO rollout_steps (id, rollout_plan_id, step_order, action, config, status, health_result)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, step.ID, step.RolloutPlanID, step.StepOrder, string(step.Action),
			configJSON, string(step.Status), healthJSON)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range steps {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("batch insert step: %w", err)
		}
	}
	return nil
}

func (r *PgRolloutPlanRepository) GetStepByID(ctx context.Context, id uuid.UUID) (*domain.RolloutStep, error) {
	step := &domain.RolloutStep{}
	var action, status string
	var configJSON, healthJSON []byte

	err := r.pool.QueryRow(ctx, `
		SELECT id, rollout_plan_id, step_order, action, config, status, health_result, started_at, completed_at
		FROM rollout_steps WHERE id = $1
	`, id).Scan(&step.ID, &step.RolloutPlanID, &step.StepOrder, &action,
		&configJSON, &status, &healthJSON, &step.StartedAt, &step.CompletedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting step: %w", err)
	}
	step.Action = domain.StepAction(action)
	step.Status = domain.StepStatus(status)
	_ = json.Unmarshal(configJSON, &step.Config)
	_ = json.Unmarshal(healthJSON, &step.HealthResult)
	return step, nil
}

func (r *PgRolloutPlanRepository) ListStepsByPlan(ctx context.Context, planID uuid.UUID) ([]domain.RolloutStep, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, rollout_plan_id, step_order, action, config, status, health_result, started_at, completed_at
		FROM rollout_steps WHERE rollout_plan_id = $1
		ORDER BY step_order ASC
	`, planID)
	if err != nil {
		return nil, fmt.Errorf("listing steps: %w", err)
	}
	defer rows.Close()

	var steps []domain.RolloutStep
	for rows.Next() {
		var s domain.RolloutStep
		var action, status string
		var configJSON, healthJSON []byte
		if err := rows.Scan(&s.ID, &s.RolloutPlanID, &s.StepOrder, &action,
			&configJSON, &status, &healthJSON, &s.StartedAt, &s.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning step: %w", err)
		}
		s.Action = domain.StepAction(action)
		s.Status = domain.StepStatus(status)
		_ = json.Unmarshal(configJSON, &s.Config)
		_ = json.Unmarshal(healthJSON, &s.HealthResult)
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

func (r *PgRolloutPlanRepository) UpdateStep(ctx context.Context, step *domain.RolloutStep) error {
	configJSON, _ := json.Marshal(step.Config)
	healthJSON, _ := json.Marshal(step.HealthResult)

	_, err := r.pool.Exec(ctx, `
		UPDATE rollout_steps SET status = $1, health_result = $2, config = $3, started_at = $4, completed_at = $5
		WHERE id = $6
	`, string(step.Status), healthJSON, configJSON, step.StartedAt, step.CompletedAt, step.ID)
	if err != nil {
		return fmt.Errorf("updating step: %w", err)
	}
	return nil
}
