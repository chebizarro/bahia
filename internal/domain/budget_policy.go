package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type BudgetPolicyScope string

const (
	BudgetPolicyScopeAgent BudgetPolicyScope = "agent"
	BudgetPolicyScopeTask  BudgetPolicyScope = "task"
	BudgetPolicyScopeBoth  BudgetPolicyScope = "both"
)

type BudgetLimit struct {
	ResourceType UsageResourceType `json:"resource_type"`
	MaxAmount    int64             `json:"max_amount"`
	WarnAt       int64             `json:"warn_at,omitempty"`
}

type BudgetPolicy struct {
	ID          uuid.UUID         `json:"id"`
	Version     int64             `json:"version"`
	Name        string            `json:"name"`
	AgentPubkey string            `json:"agent_pubkey,omitempty"`
	TaskID      string            `json:"task_id,omitempty"`
	Scope       BudgetPolicyScope `json:"scope"`
	Limits      []BudgetLimit     `json:"limits"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type BudgetPolicyResolution struct {
	Policy    BudgetPolicy `json:"policy"`
	Limits    []BudgetLimit `json:"limits"`
	MatchType string       `json:"match_type"`
}

type BurnDetail struct {
	ResourceType UsageResourceType `json:"resource_type"`
	Amount       int64             `json:"amount"`
}

type RemainingDetail struct {
	ResourceType UsageResourceType `json:"resource_type"`
	Budget       int64             `json:"budget"`
	Used         int64             `json:"used"`
	Remaining    int64             `json:"remaining"`
}

type BudgetForecastDetail struct {
	ResourceType UsageResourceType `json:"resource_type"`
	Budget       int64             `json:"budget"`
	Used         int64             `json:"used"`
	Projected    int64             `json:"projected"`
	PeriodRatio  float64           `json:"period_ratio"`
	AtRisk       bool              `json:"at_risk"`
}

type BudgetEvaluation struct {
	AgentPubkey string                 `json:"agent_pubkey"`
	TaskID      string                 `json:"task_id,omitempty"`
	Resolution  BudgetPolicyResolution `json:"resolution"`
	Burn        []BurnDetail           `json:"burn"`
	Remaining   []RemainingDetail      `json:"remaining"`
	Forecast    []BudgetForecastDetail  `json:"forecast"`
	EvaluatedAt time.Time              `json:"evaluated_at"`
	Valid       bool                   `json:"valid"`
	Errors      []string               `json:"errors,omitempty"`
}

func ValidateBudgetPolicy(p *BudgetPolicy) error {
	if p == nil {
		return fmt.Errorf("%w: budget policy must not be nil", ErrInvalidValue)
	}
	p.Name = strings.TrimSpace(p.Name)
	p.AgentPubkey = strings.TrimSpace(p.AgentPubkey)
	p.TaskID = strings.TrimSpace(p.TaskID)

	if p.Name == "" {
		return fmt.Errorf("%w: budget policy name must not be empty", ErrEmptyField)
	}
	if p.AgentPubkey == "" && p.TaskID == "" {
		return fmt.Errorf("%w: at least one of agent_pubkey or task_id must be non-empty", ErrInvalidValue)
	}
	if len(p.Limits) == 0 {
		return fmt.Errorf("%w: budget policy must have at least one limit", ErrEmptyField)
	}

	switch p.Scope {
	case BudgetPolicyScopeAgent, BudgetPolicyScopeTask, BudgetPolicyScopeBoth:
	default:
		return fmt.Errorf("%w: budget policy scope %q is not valid (allowed: agent, task, both)", ErrInvalidValue, p.Scope)
	}

	if p.Version < 1 {
		p.Version = 1
	}

	seenResources := make(map[UsageResourceType]bool)
	for i, l := range p.Limits {
		switch l.ResourceType {
		case UsageResourceTypeCompute, UsageResourceTypeInference, UsageResourceTypeStorage:
		default:
			return fmt.Errorf("%w: limit[%d] resource_type %q is not valid", ErrInvalidValue, i, l.ResourceType)
		}
		if l.MaxAmount <= 0 {
			return fmt.Errorf("%w: limit[%d] max_amount must be positive", ErrInvalidValue, i)
		}
		if l.WarnAt < 0 {
			return fmt.Errorf("%w: limit[%d] warn_at must not be negative", ErrInvalidValue, i)
		}
		if l.WarnAt > 0 && l.WarnAt >= l.MaxAmount {
			return fmt.Errorf("%w: limit[%d] warn_at must be less than max_amount", ErrInvalidValue, i)
		}
		if seenResources[l.ResourceType] {
			return fmt.Errorf("%w: limit[%d] duplicate resource_type %q", ErrInvalidValue, i, l.ResourceType)
		}
		seenResources[l.ResourceType] = true
	}
	return nil
}

func MergeBudgetLimits(existing []BudgetLimit, override []BudgetLimit) []BudgetLimit {
	merged := make([]BudgetLimit, 0, len(existing))
	overrideMap := make(map[UsageResourceType]BudgetLimit)
	for _, l := range override {
		overrideMap[l.ResourceType] = l
	}
	for _, l := range existing {
		if ol, ok := overrideMap[l.ResourceType]; ok {
			merged = append(merged, ol)
			delete(overrideMap, l.ResourceType)
		} else {
			merged = append(merged, l)
		}
	}
	for _, l := range overrideMap {
		merged = append(merged, l)
	}
	return merged
}
