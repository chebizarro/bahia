package service

import (
	"context"
	"testing"
	"time"

	llmadapter "github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeLLMProvisioner struct {
	observations []domain.HealthStatus
	calls        int
}

func (f *fakeLLMProvisioner) Provision(context.Context, llmadapter.ProvisionCandidateRequest) (*llmadapter.ProvisionCandidateResult, error) {
	return nil, nil
}
func (f *fakeLLMProvisioner) Observe(context.Context, llmadapter.ProvisionCandidateRequest) (*llmadapter.BackendObservation, error) {
	status := domain.HealthStatusUnhealthy
	if f.calls < len(f.observations) {
		status = f.observations[f.calls]
	}
	f.calls++
	return &llmadapter.BackendObservation{HealthStatus: status}, nil
}
func (f *fakeLLMProvisioner) Deprovision(context.Context, llmadapter.ProvisionCandidateRequest) error {
	return nil
}

func TestLLMPromotionGatePassesAfterHealthyThreshold(t *testing.T) {
	gate := NewLLMPromotionGate()
	gate.after = immediateAfter
	provisioner := &fakeLLMProvisioner{observations: []domain.HealthStatus{domain.HealthStatusHealthy, domain.HealthStatusHealthy}}
	result, err := gate.Evaluate(t.Context(), provisioner, llmadapter.ProvisionCandidateRequest{}, &domain.LLMPromotionGateConfig{
		IntervalSeconds:  1,
		TimeoutSeconds:   5,
		SuccessThreshold: 2,
		FailureThreshold: 2,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !result.Passed || provisioner.calls != 2 {
		t.Fatalf("expected pass after two healthy checks: result=%#v calls=%d", result, provisioner.calls)
	}
}

func TestLLMPromotionGateFailsAfterFailureThreshold(t *testing.T) {
	gate := NewLLMPromotionGate()
	gate.after = immediateAfter
	provisioner := &fakeLLMProvisioner{observations: []domain.HealthStatus{domain.HealthStatusUnhealthy}}
	result, err := gate.Evaluate(t.Context(), provisioner, llmadapter.ProvisionCandidateRequest{}, &domain.LLMPromotionGateConfig{
		IntervalSeconds:  1,
		TimeoutSeconds:   5,
		SuccessThreshold: 1,
		FailureThreshold: 1,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if result.Passed {
		t.Fatalf("expected failed gate: %#v", result)
	}
}

func immediateAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}
