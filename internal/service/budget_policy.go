package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

type BudgetPolicyRepository interface {
	Create(ctx context.Context, p *domain.BudgetPolicy) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.BudgetPolicy, error)
	GetByIDVersion(ctx context.Context, id uuid.UUID, version int64) (*domain.BudgetPolicy, error)
	List(ctx context.Context, enabledOnly bool) ([]domain.BudgetPolicy, error)
	Resolve(ctx context.Context, agentPubkey, taskID string) ([]domain.BudgetPolicy, error)
	Update(ctx context.Context, p *domain.BudgetPolicy) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type UsageLedgerQuery interface {
	SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error)
	SumByTask(ctx context.Context, taskID string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error)
}

type BudgetPolicyService struct {
	policies  BudgetPolicyRepository
	ledger    UsageLedgerQuery
	clock     func() time.Time
}

func NewBudgetPolicyService(policies BudgetPolicyRepository, ledger UsageLedgerQuery) *BudgetPolicyService {
	return &BudgetPolicyService{
		policies: policies,
		ledger:   ledger,
		clock:    time.Now,
	}
}

func (s *BudgetPolicyService) CreatePolicy(ctx context.Context, p *domain.BudgetPolicy) error {
	if err := domain.ValidateBudgetPolicy(p); err != nil {
		return err
	}
	return s.policies.Create(ctx, p)
}

func (s *BudgetPolicyService) GetPolicy(ctx context.Context, id uuid.UUID) (*domain.BudgetPolicy, error) {
	return s.policies.GetByID(ctx, id)
}

func (s *BudgetPolicyService) ListPolicies(ctx context.Context, enabledOnly bool) ([]domain.BudgetPolicy, error) {
	return s.policies.List(ctx, enabledOnly)
}

func (s *BudgetPolicyService) UpdatePolicy(ctx context.Context, p *domain.BudgetPolicy) error {
	if err := domain.ValidateBudgetPolicy(p); err != nil {
		return err
	}
	return s.policies.Update(ctx, p)
}

func (s *BudgetPolicyService) DeletePolicy(ctx context.Context, id uuid.UUID) error {
	return s.policies.Delete(ctx, id)
}

func (s *BudgetPolicyService) Evaluate(ctx context.Context, agentPubkey, taskID string, periodStart, periodEnd time.Time) (*domain.BudgetEvaluation, error) {
	eval := &domain.BudgetEvaluation{
		AgentPubkey: agentPubkey,
		TaskID:      taskID,
		EvaluatedAt: s.clock().UTC(),
	}

	resolved, err := s.resolvePolicy(ctx, agentPubkey, taskID)
	if err != nil {
		eval.Valid = false
		eval.Errors = append(eval.Errors, err.Error())
		return eval, nil
	}
	if resolved == nil {
		eval.Valid = false
		eval.Errors = append(eval.Errors, fmt.Sprintf("no budget policy found for agent %q and task %q", agentPubkey, taskID))
		return eval, nil
	}

	limits := resolved.Limits
	eval.Resolution = domain.BudgetPolicyResolution{
		Policy:    *resolved,
		Limits:    limits,
		MatchType: resolveMatchType(resolved, agentPubkey, taskID),
	}

	now := s.clock().UTC()
	elapsed := now.Sub(periodStart)
	total := periodEnd.Sub(periodStart)
	periodRatio := 1.0
	if total > 0 {
		periodRatio = math.Min(1.0, float64(elapsed)/float64(total))
	}

	var burnDetails []domain.BurnDetail
	var remainingDetails []domain.RemainingDetail
	var forecastDetails []domain.BudgetForecastDetail

	for _, limit := range limits {
		used, err := s.usageForLimit(ctx, agentPubkey, taskID, limit, periodStart, periodEnd)
		if err != nil {
			eval.Valid = false
			eval.Errors = append(eval.Errors, fmt.Sprintf("computing usage for %s: %v", limit.ResourceType, err))
			continue
		}

		burnDetails = append(burnDetails, domain.BurnDetail{
			ResourceType: limit.ResourceType,
			Amount:       used,
		})

		remaining := limit.MaxAmount - used
		if remaining < 0 {
			remaining = 0
		}
		remainingDetails = append(remainingDetails, domain.RemainingDetail{
			ResourceType: limit.ResourceType,
			Budget:       limit.MaxAmount,
			Used:         used,
			Remaining:    remaining,
		})

		var projected int64
		atRisk := false
		if periodRatio > 0 {
			projected = int64(math.Ceil(float64(used) / periodRatio))
			atRisk = projected > limit.MaxAmount
		} else {
			projected = used
		}
		if projected < used {
			projected = used
		}

		forecastDetails = append(forecastDetails, domain.BudgetForecastDetail{
			ResourceType: limit.ResourceType,
			Budget:       limit.MaxAmount,
			Used:         used,
			Projected:    projected,
			PeriodRatio:  periodRatio,
			AtRisk:       atRisk,
		})
	}

	eval.Burn = burnDetails
	eval.Remaining = remainingDetails
	eval.Forecast = forecastDetails
	eval.Valid = len(eval.Errors) == 0
	return eval, nil
}

func (s *BudgetPolicyService) resolvePolicy(ctx context.Context, agentPubkey, taskID string) (*domain.BudgetPolicy, error) {
	candidates, err := s.policies.Resolve(ctx, agentPubkey, taskID)
	if err != nil {
		return nil, fmt.Errorf("resolving budget policy: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 1 {
		ids := make([]uuid.UUID, len(candidates))
		for i, c := range candidates {
			ids[i] = c.ID
		}
		return nil, fmt.Errorf("ambiguous budget policy: %d policies match agent %q and task %q (%v)", len(candidates), agentPubkey, taskID, ids)
	}
	return &candidates[0], nil
}

func (s *BudgetPolicyService) usageForLimit(ctx context.Context, agentPubkey, taskID string, limit domain.BudgetLimit, periodStart, periodEnd time.Time) (int64, error) {
	if taskID != "" {
		taskSum, err := s.ledger.SumByTask(ctx, taskID, limit.ResourceType, periodStart, periodEnd)
		if err != nil {
			return 0, err
		}
		return taskSum, nil
	}
	agentSum, err := s.ledger.SumByAgent(ctx, agentPubkey, limit.ResourceType, periodStart, periodEnd)
	if err != nil {
		return 0, err
	}
	return agentSum, nil
}

func resolveMatchType(p *domain.BudgetPolicy, agentPubkey, taskID string) string {
	if p.AgentPubkey == agentPubkey && p.TaskID == taskID && p.TaskID != "" {
		return "exact_agent_task"
	}
	if p.AgentPubkey == agentPubkey && p.TaskID == "" {
		return "agent_only"
	}
	if p.AgentPubkey == "" && p.TaskID == taskID {
		return "task_only"
	}
	return "default"
}
