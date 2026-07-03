package config

import (
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantExternalMCPDefaultsDisabled(t *testing.T) {
	cfg := Defaults()
	if len(cfg.Assistant.MCP.ExternalServers) != 0 {
		t.Fatalf("default external servers = %#v, want empty", cfg.Assistant.MCP.ExternalServers)
	}
	if err := cfg.validateAssistant(); err != nil {
		t.Fatalf("validateAssistant() error = %v", err)
	}
}

func TestAssistantExternalMCPEnabledRequiresExplicitPermissions(t *testing.T) {
	cfg := Defaults()
	cfg.Assistant.MCP.ExternalServers = []AssistantExternalMCPServerConfig{{
		Enabled:    true,
		Name:       "docs",
		URL:        "https://mcp.example.test/mcp",
		ToolPrefix: "docs_",
	}}
	err := cfg.validateAssistant()
	if err == nil || !strings.Contains(err.Error(), "permissions must contain at least one explicit rule") {
		t.Fatalf("validateAssistant() error = %v, want explicit permissions failure", err)
	}
}

func TestAssistantExternalMCPValidationNormalizesEnabledServer(t *testing.T) {
	cfg := Defaults()
	cfg.Assistant.MCP.ExternalServers = []AssistantExternalMCPServerConfig{{
		Enabled:       true,
		Name:          " docs ",
		URL:           " https://mcp.example.test/ ",
		ToolPrefix:    "docs_",
		DefaultEffect: "read",
		DefaultRisk:   "low",
		ResourceTypes: []string{" docs ", ""},
		Permissions: []AssistantExternalMCPPermissionConfig{{
			Decision:      "allow",
			ToolPrefixes:  []string{" search_ ", ""},
			Effects:       []domain.AssistantToolEffect{"read"},
			Risks:         []domain.AssistantToolRisk{"low"},
			ResourceTypes: []string{" docs "},
			Reason:        " trusted docs ",
		}},
	}}
	if err := cfg.validateAssistant(); err != nil {
		t.Fatalf("validateAssistant() error = %v", err)
	}
	server := cfg.Assistant.MCP.ExternalServers[0]
	if server.Name != "docs" || server.URL != "https://mcp.example.test" || server.Timeout != 30*time.Second {
		t.Fatalf("server normalization = %#v", server)
	}
	if server.DefaultEffect != domain.AssistantToolEffectRead || server.DefaultRisk != domain.AssistantToolRiskLow {
		t.Fatalf("effect/risk = %s/%s", server.DefaultEffect, server.DefaultRisk)
	}
	if len(server.ResourceTypes) != 1 || server.ResourceTypes[0] != "docs" {
		t.Fatalf("resource types = %#v", server.ResourceTypes)
	}
	perm := server.Permissions[0]
	if perm.Decision != domain.AssistantPermissionDecisionAllow || perm.ToolPrefixes[0] != "search_" || perm.Reason != "trusted docs" {
		t.Fatalf("permission normalization = %#v", perm)
	}
}

func TestAssistantExternalMCPRejectsInvalidPrefix(t *testing.T) {
	cfg := Defaults()
	cfg.Assistant.MCP.ExternalServers = []AssistantExternalMCPServerConfig{{
		Enabled:    true,
		Name:       "docs",
		URL:        "https://mcp.example.test/mcp",
		ToolPrefix: "docs.search.",
		Permissions: []AssistantExternalMCPPermissionConfig{{
			Decision: "deny",
		}},
	}}
	err := cfg.validateAssistant()
	if err == nil || !strings.Contains(err.Error(), "tool_prefix may only contain") {
		t.Fatalf("validateAssistant() error = %v, want invalid prefix failure", err)
	}
}
