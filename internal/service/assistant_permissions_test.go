package service

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantPermissionEngineReviewAllowsReadOnlyTool(t *testing.T) {
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{
		Mode: domain.AssistantPermissionModeReview,
	}, nil)

	result := engine.Evaluate(AssistantPermissionRequest{
		Tool: AssistantToolPermissionMetadata{
			Name:          "bahia_assistant_service_list",
			Effect:        domain.AssistantToolEffectRead,
			DefaultRisk:   domain.AssistantToolRiskLow,
			ExecutionMode: domain.AssistantToolExecutionModeSync,
			ResourceTypes: []string{"service"},
		},
	})

	assertPermissionDecision(t, result, domain.AssistantPermissionDecisionAllow)
	if result.Mode != domain.AssistantPermissionModeReview {
		t.Fatalf("result mode = %q, want review", result.Mode)
	}
	if result.Effect != domain.AssistantToolEffectRead {
		t.Fatalf("result effect = %q, want read", result.Effect)
	}
	if result.Risk != domain.AssistantToolRiskLow {
		t.Fatalf("result risk = %q, want low", result.Risk)
	}
}

func TestAssistantPermissionEngineReviewAsksForMutationTool(t *testing.T) {
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{
		Mode: domain.AssistantPermissionModeReview,
	}, nil)

	result := engine.Evaluate(AssistantPermissionRequest{
		Tool: AssistantToolPermissionMetadata{
			Name:          "bahia_assistant_deploy_service",
			Effect:        domain.AssistantToolEffectMutation,
			DefaultRisk:   domain.AssistantToolRiskMedium,
			ExecutionMode: domain.AssistantToolExecutionModeAsync,
			ResourceTypes: []string{"deployment"},
		},
		Args: map[string]any{"service": "api"},
	})

	assertPermissionDecision(t, result, domain.AssistantPermissionDecisionAsk)
	if result.ExecutionMode != domain.AssistantToolExecutionModeAsync {
		t.Fatalf("result execution mode = %q, want async", result.ExecutionMode)
	}
	if result.Risk != domain.AssistantToolRiskMedium {
		t.Fatalf("result risk = %q, want medium", result.Risk)
	}
}

func TestAssistantPermissionEngineExplicitDenyOverridesReviewReadAllow(t *testing.T) {
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{
		Mode: domain.AssistantPermissionModeReview,
	}, []AssistantPermissionRule{
		{
			ID:        "deny-secret-export",
			Decision:  domain.AssistantPermissionDecisionDeny,
			ToolNames: []string{"bahia_assistant_secret_export"},
			Reason:    "secret export is forbidden for assistant tools",
		},
	})

	result := engine.Evaluate(AssistantPermissionRequest{
		Tool: AssistantToolPermissionMetadata{
			Name:          "bahia_assistant_secret_export",
			Effect:        domain.AssistantToolEffectRead,
			DefaultRisk:   domain.AssistantToolRiskHigh,
			ExecutionMode: domain.AssistantToolExecutionModeSync,
			ResourceTypes: []string{"secret"},
		},
	})

	assertPermissionDecision(t, result, domain.AssistantPermissionDecisionDeny)
	if result.RuleID != "deny-secret-export" {
		t.Fatalf("result rule id = %q, want deny-secret-export", result.RuleID)
	}
}

func TestAssistantPermissionEngineDefaultAuditedAllowsLowRiskMutation(t *testing.T) {
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{}, nil)

	result := engine.Evaluate(AssistantPermissionRequest{
		Tool: AssistantToolPermissionMetadata{
			Name:          "bahia_assistant_update_dns_record",
			Effect:        domain.AssistantToolEffectMutation,
			DefaultRisk:   domain.AssistantToolRiskLow,
			ExecutionMode: domain.AssistantToolExecutionModeAsync,
			ResourceTypes: []string{"dns"},
		},
		Args: map[string]any{"zone": "staging", "record": "api.example.com"},
	})

	assertPermissionDecision(t, result, domain.AssistantPermissionDecisionAllow)
	if result.Mode != domain.AssistantPermissionModeAudited {
		t.Fatalf("result mode = %q, want audited", result.Mode)
	}
	if result.Risk != domain.AssistantToolRiskLow {
		t.Fatalf("result risk = %q, want low", result.Risk)
	}
}

func TestAssistantPermissionEngineAuditedAsksWhenArgsUpgradeRisk(t *testing.T) {
	engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{
		Mode: domain.AssistantPermissionModeAudited,
	}, nil)

	result := engine.Evaluate(AssistantPermissionRequest{
		Tool: AssistantToolPermissionMetadata{
			Name:          "bahia_assistant_update_dns_record",
			Effect:        domain.AssistantToolEffectMutation,
			DefaultRisk:   domain.AssistantToolRiskLow,
			ExecutionMode: domain.AssistantToolExecutionModeAsync,
			ResourceTypes: []string{"dns"},
		},
		Args: map[string]any{"operation": "revoke", "zone": "prod", "record": "api.example.com"},
	})

	assertPermissionDecision(t, result, domain.AssistantPermissionDecisionAsk)
	if result.Risk != domain.AssistantToolRiskHigh {
		t.Fatalf("result risk = %q, want high", result.Risk)
	}
	if result.Metadata["risk_upgraded_from"] != string(domain.AssistantToolRiskLow) {
		t.Fatalf("risk upgrade metadata = %#v, want previous low risk", result.Metadata)
	}
}

func TestAssistantPermissionEngineReadonlyAndEmergencyDenyMutations(t *testing.T) {
	for _, mode := range []domain.AssistantPermissionMode{domain.AssistantPermissionModeReadonly, domain.AssistantPermissionModeEmergency} {
		t.Run(string(mode), func(t *testing.T) {
			engine := NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: mode}, nil)
			result := engine.Evaluate(AssistantPermissionRequest{Tool: AssistantToolPermissionMetadata{Name: "bahia_assistant_delete_policy", Effect: domain.AssistantToolEffectMutation, DefaultRisk: domain.AssistantToolRiskLow, ExecutionMode: domain.AssistantToolExecutionModeSync}})
			assertPermissionDecision(t, result, domain.AssistantPermissionDecisionDeny)
		})
	}
}

func assertPermissionDecision(t *testing.T, result domain.AssistantPermissionResult, want domain.AssistantPermissionDecision) {
	t.Helper()
	if result.Decision != want {
		t.Fatalf("result decision = %q, want %q; result=%#v", result.Decision, want, result)
	}
	if result.Reason == "" {
		t.Fatalf("result missing reason: %#v", result)
	}
}
