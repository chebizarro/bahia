package mcp

import (
	"reflect"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestAssistantToolRegistryIncludesAssistantToolDescriptors(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	registry := server.AssistantToolRegistry()

	assistantTools := append([]Tool{}, assistantAsyncToolDefinitions()...)
	assistantTools = append(assistantTools, dnsAssistantToolDefinitions()...)
	for _, tool := range assistantTools {
		descriptor, ok := registry.GetAgentTool(tool.Name)
		if !ok {
			t.Fatalf("assistant tool %s missing from agent-safe registry", tool.Name)
		}
		if descriptor.Tool.Name != tool.Name || descriptor.Tool.Description != tool.Description || !reflect.DeepEqual(descriptor.Tool.InputSchema, tool.InputSchema) {
			t.Fatalf("descriptor for %s does not wrap existing tool definition", tool.Name)
		}
	}
}

func TestAssistantToolRegistryMetadataCorrectness(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	registry := server.AssistantToolRegistry()

	cases := []struct {
		name          string
		mode          domain.AssistantToolExecutionMode
		effect        domain.AssistantToolEffect
		risk          domain.AssistantToolRisk
		agentSafe     bool
		resourceTypes []string
	}{
		{
			name:          "bahia_assistant_service_deploy",
			mode:          domain.AssistantToolExecutionModeAsync,
			effect:        domain.AssistantToolEffectMutation,
			risk:          domain.AssistantToolRiskMedium,
			agentSafe:     true,
			resourceTypes: []string{"service", "environment", "artifact", "deployment"},
		},
		{
			name:          "bahia_assistant_service_rollback",
			mode:          domain.AssistantToolExecutionModeAsync,
			effect:        domain.AssistantToolEffectMutation,
			risk:          domain.AssistantToolRiskHigh,
			agentSafe:     true,
			resourceTypes: []string{"service", "environment", "deployment"},
		},
		{
			name:          "bahia_assistant_dns_list_endpoints",
			mode:          domain.AssistantToolExecutionModeSync,
			effect:        domain.AssistantToolEffectRead,
			risk:          domain.AssistantToolRiskLow,
			agentSafe:     true,
			resourceTypes: []string{"dns_endpoint", "dns_zone"},
		},
		{
			name:          "bahia_assistant_dns_policy_apply",
			mode:          domain.AssistantToolExecutionModeAsync,
			effect:        domain.AssistantToolEffectMutation,
			risk:          domain.AssistantToolRiskHigh,
			agentSafe:     true,
			resourceTypes: []string{"dns_policy", "dns_zone", "environment"},
		},
		{
			name:          "bahia_llm_update_route",
			mode:          domain.AssistantToolExecutionModeSync,
			effect:        domain.AssistantToolEffectMutation,
			risk:          domain.AssistantToolRiskMedium,
			agentSafe:     false,
			resourceTypes: []string{"llm_route"},
		},
		{
			name:          "bahia_ml_get_provenance",
			mode:          domain.AssistantToolExecutionModeSync,
			effect:        domain.AssistantToolEffectRead,
			risk:          domain.AssistantToolRiskLow,
			agentSafe:     true,
			resourceTypes: []string{"ml_artifact", "ml_provenance"},
		},
		{
			name:          "bahia_ml_rollback",
			mode:          domain.AssistantToolExecutionModeAsync,
			effect:        domain.AssistantToolEffectMutation,
			risk:          domain.AssistantToolRiskHigh,
			agentSafe:     false,
			resourceTypes: []string{"ml_endpoint", "environment", "deployment"},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			descriptor, ok := registry.Get(tt.name)
			if !ok {
				t.Fatalf("descriptor %s not found", tt.name)
			}
			if descriptor.ExecutionMode != tt.mode {
				t.Fatalf("mode = %s, want %s", descriptor.ExecutionMode, tt.mode)
			}
			if descriptor.Effect != tt.effect {
				t.Fatalf("effect = %s, want %s", descriptor.Effect, tt.effect)
			}
			if descriptor.DefaultRisk != tt.risk {
				t.Fatalf("risk = %s, want %s", descriptor.DefaultRisk, tt.risk)
			}
			if descriptor.AgentSafe != tt.agentSafe {
				t.Fatalf("agentSafe = %v, want %v", descriptor.AgentSafe, tt.agentSafe)
			}
			for _, resourceType := range tt.resourceTypes {
				if !hasString(descriptor.ResourceTypes, resourceType) {
					t.Fatalf("resource type %q missing from %v", resourceType, descriptor.ResourceTypes)
				}
			}
		})
	}
}

func TestAssistantToolRegistryListsOnlyAgentSafeTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	registry := server.AssistantToolRegistry()

	agentTools := registry.AgentTools()
	if len(agentTools) == 0 {
		t.Fatal("AgentTools returned no descriptors")
	}
	names := map[string]bool{}
	for _, descriptor := range agentTools {
		if !descriptor.AgentSafe {
			t.Fatalf("AgentTools included non-agent-safe descriptor %s", descriptor.Tool.Name)
		}
		names[descriptor.Tool.Name] = true
	}

	for _, required := range []string{
		"bahia_list_services",
		"bahia_get_service",
		"bahia_llm_list_routes",
		"bahia_ml_list_state",
		"bahia_dns_list_endpoints",
		"bahia_assistant_dns_list_drift",
		"bahia_assistant_service_deploy",
		"bahia_assistant_llm_deploy",
		"bahia_assistant_ml_deploy",
	} {
		if !names[required] {
			t.Fatalf("agent-safe registry missing %s", required)
		}
	}

	for _, forbidden := range []string{
		"bahia_create_service",
		"bahia_delete_service",
		"bahia_deploy",
		"bahia_llm_create_route",
		"bahia_ml_deploy",
	} {
		if names[forbidden] {
			t.Fatalf("agent-safe registry included %s", forbidden)
		}
		if _, ok := registry.GetAgentTool(forbidden); ok {
			t.Fatalf("GetAgentTool returned non-agent-safe descriptor %s", forbidden)
		}
	}
}

func TestAssistantToolRegistryMergesExternalToolsAndRejectsCollisions(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	registry, err := NewAssistantToolRegistryForServerWithExternal(server, []ExternalToolDescriptor{
		{
			ServerName:  "docs",
			Tool:        Tool{Name: "docs_search", Description: "Search external docs", InputSchema: map[string]interface{}{"type": "object"}},
			Effect:      domain.AssistantToolEffectRead,
			DefaultRisk: domain.AssistantToolRiskLow,
			AgentSafe:   true,
		},
	})
	if err != nil {
		t.Fatalf("NewAssistantToolRegistryForServerWithExternal() error = %v", err)
	}
	descriptor, ok := registry.GetAgentTool("docs_search")
	if !ok {
		t.Fatal("external tool missing from agent-safe registry")
	}
	if descriptor.ExternalServerName != "docs" {
		t.Fatalf("ExternalServerName = %q", descriptor.ExternalServerName)
	}
	if descriptor.ExecutionMode != domain.AssistantToolExecutionModeSync || descriptor.Effect != domain.AssistantToolEffectRead || descriptor.DefaultRisk != domain.AssistantToolRiskLow {
		t.Fatalf("external descriptor metadata = mode %s effect %s risk %s", descriptor.ExecutionMode, descriptor.Effect, descriptor.DefaultRisk)
	}

	_, err = NewAssistantToolRegistryForServerWithExternal(server, []ExternalToolDescriptor{{
		ServerName: "bad",
		Tool:       Tool{Name: "bahia_list_services"},
		AgentSafe:  true,
	}})
	if err == nil {
		t.Fatal("collision with Bahia tool did not fail")
	}

	_, err = NewAssistantToolRegistryWithExternalTools(nil, []ExternalToolDescriptor{
		{ServerName: "one", Tool: Tool{Name: "same_tool"}, AgentSafe: true},
		{ServerName: "two", Tool: Tool{Name: "same_tool"}, AgentSafe: true},
	})
	if err == nil {
		t.Fatal("collision between external tools did not fail")
	}
}

func TestAssistantToolRegistryWrapsGetToolsDefinitions(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	toolsByName := map[string]Tool{}
	for _, tool := range server.GetTools() {
		toolsByName[tool.Name] = tool
	}

	registry := server.AssistantToolRegistry()
	for _, descriptor := range registry.All() {
		tool, ok := toolsByName[descriptor.Tool.Name]
		if !ok {
			t.Fatalf("descriptor %s not found in GetTools", descriptor.Tool.Name)
		}
		if descriptor.Tool.Description != tool.Description {
			t.Fatalf("description mismatch for %s", descriptor.Tool.Name)
		}
		if !reflect.DeepEqual(descriptor.Tool.InputSchema, tool.InputSchema) {
			t.Fatalf("input schema mismatch for %s", descriptor.Tool.Name)
		}
	}

	agentDescriptors := registry.AgentTools()
	agentTools := registry.AgentMCPTools()
	if len(agentTools) != len(agentDescriptors) {
		t.Fatalf("AgentMCPTools length = %d, want %d", len(agentTools), len(agentDescriptors))
	}
	for i := range agentTools {
		if agentTools[i].Name != agentDescriptors[i].Tool.Name {
			t.Fatalf("AgentMCPTools[%d] = %s, want %s", i, agentTools[i].Name, agentDescriptors[i].Tool.Name)
		}
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
