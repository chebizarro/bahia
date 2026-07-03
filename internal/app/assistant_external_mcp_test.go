package app

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestAssistantExternalPermissionRulesArePrefixedAndDeny(t *testing.T) {
	server := config.AssistantExternalMCPServerConfig{
		Name:       "docs",
		ToolPrefix: "docs_",
		Permissions: []config.AssistantExternalMCPPermissionConfig{{
			Decision:  domain.AssistantPermissionDecisionDeny,
			ToolNames: []string{"search"},
			Reason:    "docs search disabled",
		}},
	}
	rules := assistantExternalPermissionRules(server)
	if len(rules) != 1 {
		t.Fatalf("rules = %#v", rules)
	}
	if got := rules[0].ToolNames; len(got) != 1 || got[0] != "docs_search" {
		t.Fatalf("ToolNames = %#v, want docs_search", got)
	}
	engine := service.NewAssistantPermissionEngine(config.AssistantPermissionsConfig{Mode: domain.AssistantPermissionModeAudited}, rules)
	result := engine.Evaluate(service.AssistantPermissionRequest{
		Tool: service.AssistantToolPermissionMetadata{
			Name:          "docs_search",
			Effect:        domain.AssistantToolEffectRead,
			DefaultRisk:   domain.AssistantToolRiskLow,
			ExecutionMode: domain.AssistantToolExecutionModeSync,
		},
	})
	if result.Decision != domain.AssistantPermissionDecisionDeny {
		t.Fatalf("decision = %s, want deny", result.Decision)
	}
}

func TestAssistantExternalPermissionRulesDefaultToServerPrefix(t *testing.T) {
	server := config.AssistantExternalMCPServerConfig{
		Name:       "docs",
		ToolPrefix: "docs_",
		Permissions: []config.AssistantExternalMCPPermissionConfig{{
			Decision: domain.AssistantPermissionDecisionAllow,
		}},
	}
	rules := assistantExternalPermissionRules(server)
	if len(rules) != 1 || len(rules[0].ToolPrefixes) != 1 || rules[0].ToolPrefixes[0] != "docs_" {
		t.Fatalf("rules = %#v, want default docs_ prefix", rules)
	}
}
