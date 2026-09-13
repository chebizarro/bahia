package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

type mockBudgetPolicyRepo struct {
	policies []domain.BudgetPolicy
	createErr error
	getByIDFunc func(id uuid.UUID) (*domain.BudgetPolicy, error)
	resolveFunc func(agentPubkey, taskID string) ([]domain.BudgetPolicy, error)
	listFunc func(enabledOnly bool) ([]domain.BudgetPolicy, error)
	updateErr error
	deleteErr error
}

func (m *mockBudgetPolicyRepo) Create(ctx context.Context, p *domain.BudgetPolicy) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.policies = append(m.policies, *p)
	return nil
}

func (m *mockBudgetPolicyRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.BudgetPolicy, error) {
	if m.getByIDFunc != nil {
		return m.getByIDFunc(id)
	}
	for i := range m.policies {
		if m.policies[i].ID == id {
			return &m.policies[i], nil
		}
	}
	return nil, errors.New("not found")
}

func (m *mockBudgetPolicyRepo) GetByIDVersion(ctx context.Context, id uuid.UUID, version int64) (*domain.BudgetPolicy, error) {
	return m.GetByID(ctx, id)
}

func (m *mockBudgetPolicyRepo) List(ctx context.Context, enabledOnly bool) ([]domain.BudgetPolicy, error) {
	if m.listFunc != nil {
		return m.listFunc(enabledOnly)
	}
	return m.policies, nil
}

func (m *mockBudgetPolicyRepo) Resolve(ctx context.Context, agentPubkey, taskID string) ([]domain.BudgetPolicy, error) {
	if m.resolveFunc != nil {
		return m.resolveFunc(agentPubkey, taskID)
	}
	var result []domain.BudgetPolicy
	for _, p := range m.policies {
		if p.Enabled && (p.AgentPubkey == agentPubkey || p.AgentPubkey == "") && (p.TaskID == taskID || p.TaskID == "") {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	// Filter to best match only
	bestOrder := 3
	for _, p := range result {
		var order int
		if p.AgentPubkey == agentPubkey && p.TaskID == taskID {
			order = 0
		} else if p.AgentPubkey == agentPubkey && p.TaskID == "" {
			order = 1
		} else if p.AgentPubkey == "" && p.TaskID == taskID {
			order = 2
		} else {
			order = 3
		}
		if order < bestOrder {
			bestOrder = order
		}
	}
	var filtered []domain.BudgetPolicy
	for _, p := range result {
		var order int
		if p.AgentPubkey == agentPubkey && p.TaskID == taskID {
			order = 0
		} else if p.AgentPubkey == agentPubkey && p.TaskID == "" {
			order = 1
		} else if p.AgentPubkey == "" && p.TaskID == taskID {
			order = 2
		} else {
			order = 3
		}
		if order == bestOrder {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func (m *mockBudgetPolicyRepo) Update(ctx context.Context, p *domain.BudgetPolicy) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	for i := range m.policies {
		if m.policies[i].ID == p.ID {
			m.policies[i] = *p
			return nil
		}
	}
	return errors.New("not found")
}

func (m *mockBudgetPolicyRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	for i := range m.policies {
		if m.policies[i].ID == id {
			m.policies = append(m.policies[:i], m.policies[i+1:]...)
			return nil
		}
	}
	return errors.New("not found")
}

type mockLedgerQuery struct {
	sumByAgentVal int64
	sumByAgentErr error
	sumByTaskVal  int64
	sumByTaskErr  error
}

func (m *mockLedgerQuery) SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	if m.sumByAgentErr != nil {
		return 0, m.sumByAgentErr
	}
	return m.sumByAgentVal, nil
}

func (m *mockLedgerQuery) SumByTask(ctx context.Context, taskID string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	if m.sumByTaskErr != nil {
		return 0, m.sumByTaskErr
	}
	return m.sumByTaskVal, nil
}

func nowPtr(t time.Time) *time.Time {
	return &t
}

func TestBudgetPolicyValidateRejectsNil(t *testing.T) {
	err := domain.ValidateBudgetPolicy(nil)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsEmptyName(t *testing.T) {
	p := &domain.BudgetPolicy{
		AgentPubkey: "agent1",
		TaskID:      "task1",
		Scope:       domain.BudgetPolicyScopeBoth,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	p.Name = ""
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsNoAgentOrTask(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:  "test",
		Scope: domain.BudgetPolicyScopeAgent,
		Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsNoLimits(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsInvalidScope(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       "invalid",
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsInvalidResourceType(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: "unknown", MaxAmount: 1000}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsNonPositiveMaxAmount(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 0}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsWarnAtGteMaxAmount(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 100, WarnAt: 100}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateRejectsDuplicateResourceType(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits: []domain.BudgetLimit{
			{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 100},
			{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 200},
		},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.Error(t, err)
}

func TestBudgetPolicyValidateAcceptsValidPolicy(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "agent-compute-limit",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	err := domain.ValidateBudgetPolicy(p)
	require.NoError(t, err)
}

func TestBudgetPolicyValidateDefaultsVersion(t *testing.T) {
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	_ = domain.ValidateBudgetPolicy(p)
	require.Equal(t, int64(1), p.Version)
}

func TestBudgetPolicyResolutionPrecedence(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "agent-task", AgentPubkey: "agent1", TaskID: "task1", Scope: domain.BudgetPolicyScopeBoth, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 100}}, Enabled: true},
			{ID: uuid.New(), Name: "agent-only", AgentPubkey: "agent1", TaskID: "", Scope: domain.BudgetPolicyScopeAgent, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 500}}, Enabled: true},
			{ID: uuid.New(), Name: "task-only", AgentPubkey: "", TaskID: "task1", Scope: domain.BudgetPolicyScopeTask, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 300}}, Enabled: true},
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{})
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Equal(t, "exact_agent_task", eval.Resolution.MatchType)
	require.Equal(t, int64(100), eval.Resolution.Limits[0].MaxAmount, "most specific (agent+task) policy should win")
}

func TestBudgetPolicyResolutionAgentOnly(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "agent-only", AgentPubkey: "agent1", TaskID: "", Scope: domain.BudgetPolicyScopeAgent, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 500}}, Enabled: true},
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{sumByAgentVal: 200})
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Equal(t, "agent_only", eval.Resolution.MatchType)
}

func TestBudgetPolicyResolutionTaskOnly(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "task-only", AgentPubkey: "", TaskID: "task1", Scope: domain.BudgetPolicyScopeTask, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 300}}, Enabled: true},
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{sumByTaskVal: 150})
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Equal(t, "task_only", eval.Resolution.MatchType)
}

func TestBudgetPolicyFailClosedOnNoPolicy(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		resolveFunc: func(agentPubkey, taskID string) ([]domain.BudgetPolicy, error) {
			return nil, nil
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{})
	eval, err := svc.Evaluate(context.Background(), "unknown-agent", "unknown-task", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.False(t, eval.Valid)
	require.Contains(t, eval.Errors[0], "no budget policy found")
}

func TestBudgetPolicyFailClosedOnAmbiguousPolicy(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		resolveFunc: func(agentPubkey, taskID string) ([]domain.BudgetPolicy, error) {
			return []domain.BudgetPolicy{
				{ID: uuid.New(), Name: "policy-a", AgentPubkey: agentPubkey, TaskID: taskID, Scope: domain.BudgetPolicyScopeBoth, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 100}}, Enabled: true},
				{ID: uuid.New(), Name: "policy-b", AgentPubkey: agentPubkey, TaskID: taskID, Scope: domain.BudgetPolicyScopeBoth, Limits: []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 200}}, Enabled: true},
			}, nil
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{})
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.False(t, eval.Valid)
	require.Contains(t, eval.Errors[0], "ambiguous")
}

func TestBudgetPolicyBurnRemainingForecastMath(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "test", AgentPubkey: "agent1", TaskID: "task1", Scope: domain.BudgetPolicyScopeBoth, Limits: []domain.BudgetLimit{
				{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000},
				{ResourceType: domain.UsageResourceTypeInference, MaxAmount: 500},
			}, Enabled: true},
		},
	}
	ledger := &mockLedgerQuery{
		sumByAgentVal: 200,
		sumByTaskVal:  300,
	}
	svc := NewBudgetPolicyService(repo, ledger)
	periodStart := time.Now().Add(-12 * time.Hour)
	periodEnd := time.Now().Add(12 * time.Hour)
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", periodStart, periodEnd)
	require.NoError(t, err)
	require.True(t, eval.Valid)

	require.Len(t, eval.Burn, 2)
	require.Len(t, eval.Remaining, 2)
	require.Len(t, eval.Forecast, 2)

	// For task-bound limits, SumByTask is used (300)
	computeBurn := eval.Burn[0]
	require.Equal(t, domain.UsageResourceTypeCompute, computeBurn.ResourceType)
	require.Equal(t, int64(300), computeBurn.Amount) // tasks use SumByTask

	computeRemaining := eval.Remaining[0]
	require.Equal(t, int64(1000), computeRemaining.Budget)
	require.Equal(t, int64(300), computeRemaining.Used)
	require.Equal(t, int64(700), computeRemaining.Remaining)

	computeForecast := eval.Forecast[0]
	require.True(t, computeForecast.PeriodRatio > 0)
	require.False(t, computeForecast.AtRisk)
}

func TestBudgetPolicyForecastAtRisk(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "test", AgentPubkey: "agent1", TaskID: "", Scope: domain.BudgetPolicyScopeAgent, Limits: []domain.BudgetLimit{
				{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 100},
			}, Enabled: true},
		},
	}
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	periodStart := now.Add(-24 * time.Hour)
	periodEnd := now.Add(24 * time.Hour)
	// usage is 90 with only 16h elapsed in a 48h period => projected ~270
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{sumByAgentVal: 90, sumByTaskVal: 90})
	svc.clock = func() time.Time { return now }
	eval, err := svc.Evaluate(context.Background(), "agent1", "task1", periodStart, periodEnd)
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Len(t, eval.Forecast, 1)
	require.True(t, eval.Forecast[0].AtRisk, "projected 270 > budget 100 should be at risk")
	require.True(t, eval.Forecast[0].Projected > 100)
}

func TestBudgetPolicyCorrectionAwareSum(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "test", AgentPubkey: "agent1", TaskID: "", Scope: domain.BudgetPolicyScopeAgent, Limits: []domain.BudgetLimit{
				{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000},
			}, Enabled: true},
		},
	}
	// The SumByAgent in the usage ledger repo already excludes corrections with correction_of IS NULL
	// The budget evaluator just uses SumByAgent which returns the corrected sum
	ledger := &mockLedgerQuery{sumByAgentVal: 850}
	svc := NewBudgetPolicyService(repo, ledger)
	eval, err := svc.Evaluate(context.Background(), "agent1", "", time.Now().Add(-24*time.Hour), time.Now())
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Len(t, eval.Burn, 1)
	require.Equal(t, int64(850), eval.Burn[0].Amount)
	require.Equal(t, int64(150), eval.Remaining[0].Remaining)
}

func TestBudgetPolicyForecastZeroPeriod(t *testing.T) {
	repo := &mockBudgetPolicyRepo{
		policies: []domain.BudgetPolicy{
			{ID: uuid.New(), Name: "test", AgentPubkey: "agent1", TaskID: "", Scope: domain.BudgetPolicyScopeAgent, Limits: []domain.BudgetLimit{
				{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000},
			}, Enabled: true},
		},
	}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{sumByAgentVal: 100})
	now := time.Now()
	eval, err := svc.Evaluate(context.Background(), "agent1", "", now, now)
	require.NoError(t, err)
	require.True(t, eval.Valid)
	require.Equal(t, float64(1.0), eval.Forecast[0].PeriodRatio, "zero-period defaults to 1.0 ratio")
	require.Equal(t, int64(100), eval.Forecast[0].Projected)
}

func TestBudgetPolicyMergeLimitsOverride(t *testing.T) {
	existing := []domain.BudgetLimit{
		{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000},
		{ResourceType: domain.UsageResourceTypeStorage, MaxAmount: 500},
	}
	override := []domain.BudgetLimit{
		{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 2000},
		{ResourceType: domain.UsageResourceTypeInference, MaxAmount: 300},
	}
	merged := domain.MergeBudgetLimits(existing, override)
	require.Len(t, merged, 3)
	require.Equal(t, int64(2000), merged[0].MaxAmount, "compute should be overridden")
	require.Equal(t, int64(500), merged[1].MaxAmount, "storage should be preserved")
	require.Equal(t, int64(300), merged[2].MaxAmount, "inference should be added")
}

func TestBudgetPolicyCreateRejectsInvalid(t *testing.T) {
	repo := &mockBudgetPolicyRepo{}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{})
	err := svc.CreatePolicy(context.Background(), &domain.BudgetPolicy{})
	require.Error(t, err)
}

func TestBudgetPolicyCreateValid(t *testing.T) {
	repo := &mockBudgetPolicyRepo{}
	svc := NewBudgetPolicyService(repo, &mockLedgerQuery{})
	p := &domain.BudgetPolicy{
		Name:        "test",
		AgentPubkey: "agent1",
		Scope:       domain.BudgetPolicyScopeAgent,
		Limits:      []domain.BudgetLimit{{ResourceType: domain.UsageResourceTypeCompute, MaxAmount: 1000}},
	}
	err := svc.CreatePolicy(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, repo.policies, 1)
}
