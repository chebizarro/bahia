package service

import (
	"context"
	"fmt"
	"time"

	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// LLMPromotionResult is the outcome of backend readiness checks.
type LLMPromotionResult struct {
	Passed              bool                           `json:"passed"`
	ConsecutiveHealthy  int                            `json:"consecutive_healthy"`
	ConsecutiveFailures int                            `json:"consecutive_failures"`
	LastHealth          domain.HealthStatus            `json:"last_health"`
	LastObservation     *llmadapter.BackendObservation `json:"last_observation,omitempty"`
}

// LLMPromotionGate evaluates whether a provisioned backend may be promoted.
type LLMPromotionGate struct {
	after func(time.Duration) <-chan time.Time
}

// NewLLMPromotionGate creates a promotion gate evaluator.
func NewLLMPromotionGate() *LLMPromotionGate {
	return &LLMPromotionGate{after: time.After}
}

// Evaluate observes a single candidate until it passes, fails, or times out.
func (g *LLMPromotionGate) Evaluate(ctx context.Context, provisioner llmadapter.Provisioner, req llmadapter.ProvisionCandidateRequest, cfg *domain.LLMPromotionGateConfig) (*LLMPromotionResult, error) {
	if provisioner == nil {
		return nil, fmt.Errorf("provisioner is required")
	}
	gate := defaultLLMPromotionGateConfig(cfg)
	timeout := time.Duration(gate.TimeoutSeconds) * time.Second
	interval := time.Duration(gate.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := &LLMPromotionResult{}
	for {
		obs, err := provisioner.Observe(ctx, req)
		if err != nil {
			result.ConsecutiveFailures++
			result.LastHealth = domain.HealthStatusUnhealthy
		} else {
			result.LastObservation = obs
			result.LastHealth = obs.HealthStatus
			if obs.HealthStatus == domain.HealthStatusHealthy {
				result.ConsecutiveHealthy++
				result.ConsecutiveFailures = 0
			} else {
				result.ConsecutiveFailures++
				result.ConsecutiveHealthy = 0
			}
		}

		if result.ConsecutiveHealthy >= gate.SuccessThreshold {
			result.Passed = true
			return result, nil
		}
		if result.ConsecutiveFailures >= gate.FailureThreshold {
			result.Passed = false
			return result, nil
		}

		select {
		case <-ctx.Done():
			result.Passed = false
			return result, ctx.Err()
		case <-g.after(interval):
		}
	}
}

func defaultLLMPromotionGateConfig(cfg *domain.LLMPromotionGateConfig) domain.LLMPromotionGateConfig {
	if cfg == nil {
		return domain.LLMPromotionGateConfig{
			IntervalSeconds:  5,
			TimeoutSeconds:   60,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		}
	}
	out := *cfg
	if out.IntervalSeconds <= 0 {
		out.IntervalSeconds = 5
	}
	if out.TimeoutSeconds <= 0 {
		out.TimeoutSeconds = 60
	}
	if out.SuccessThreshold <= 0 {
		out.SuccessThreshold = 1
	}
	if out.FailureThreshold <= 0 {
		out.FailureThreshold = 3
	}
	return out
}
