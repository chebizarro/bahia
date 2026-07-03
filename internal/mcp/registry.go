package mcp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ToolDescriptor wraps an existing public MCP Tool with the assistant metadata
// needed by discovery, permission evaluation, and tool-runtime routing.
type ToolDescriptor struct {
	Tool               Tool                              `json:"tool"`
	ExecutionMode      domain.AssistantToolExecutionMode `json:"execution_mode"`
	Effect             domain.AssistantToolEffect        `json:"effect"`
	DefaultRisk        domain.AssistantToolRisk          `json:"default_risk"`
	ResourceTypes      []string                          `json:"resource_types,omitempty"`
	ResourceIDFields   []string                          `json:"resource_id_fields,omitempty"`
	AgentSafe          bool                              `json:"agent_safe"`
	ExternalServerName string                            `json:"external_server_name,omitempty"`
}

// ExternalToolDescriptor describes one tool discovered from an explicitly
// enabled external MCP server. External tools are sync MCP tools from the
// assistant runtime's point of view; permission rules decide whether the model
// can execute them.
type ExternalToolDescriptor struct {
	ServerName       string
	Tool             Tool
	Effect           domain.AssistantToolEffect
	DefaultRisk      domain.AssistantToolRisk
	ResourceTypes    []string
	ResourceIDFields []string
	AgentSafe        bool
}

// Name returns the wrapped MCP tool name.
func (d ToolDescriptor) Name() string {
	return d.Tool.Name
}

// AssistantToolRegistry is the assistant discovery surface for MCP tools. It is
// intentionally a wrapper over existing Tool definitions; Server.GetTools and
// Server.CallTool remain the public MCP API and dispatch path.
type AssistantToolRegistry struct {
	order       []string
	descriptors map[string]ToolDescriptor
}

// NewAssistantToolRegistry builds an assistant descriptor registry from an
// existing MCP tool list. Tools without assistant metadata are omitted so the
// discovery surface stays scoped when unrelated MCP tools are added.
func NewAssistantToolRegistry(tools []Tool) AssistantToolRegistry {
	metadata := assistantToolDescriptorMetadata()
	registry := AssistantToolRegistry{descriptors: make(map[string]ToolDescriptor)}
	seen := map[string]bool{}
	for _, tool := range tools {
		meta, ok := metadata[tool.Name]
		if !ok {
			continue
		}
		descriptor := ToolDescriptor{
			Tool:             tool,
			ExecutionMode:    meta.executionMode,
			Effect:           meta.effect,
			DefaultRisk:      meta.defaultRisk,
			ResourceTypes:    cloneStringSlice(meta.resourceTypes),
			ResourceIDFields: cloneStringSlice(meta.resourceIDFields),
			AgentSafe:        meta.agentSafe,
		}
		registry.descriptors[tool.Name] = descriptor
		if !seen[tool.Name] {
			registry.order = append(registry.order, tool.Name)
			seen[tool.Name] = true
		}
	}
	return registry
}

// NewAssistantToolRegistryWithExternalTools builds a registry from Bahia MCP
// tools and merges explicitly configured external MCP descriptors. It rejects
// name collisions so external servers cannot shadow Bahia tools or each other.
func NewAssistantToolRegistryWithExternalTools(tools []Tool, external []ExternalToolDescriptor) (AssistantToolRegistry, error) {
	registry := NewAssistantToolRegistry(tools)
	if err := registry.MergeExternalTools(external); err != nil {
		return AssistantToolRegistry{}, err
	}
	return registry, nil
}

// NewAssistantToolRegistryForServer constructs a discovery registry from the
// server's current public MCP tool definitions.
func NewAssistantToolRegistryForServer(server *Server) AssistantToolRegistry {
	if server == nil {
		return NewAssistantToolRegistry(nil)
	}
	return NewAssistantToolRegistry(server.GetTools())
}

// NewAssistantToolRegistryForServerWithExternal constructs a server registry and
// merges explicitly configured external MCP descriptors.
func NewAssistantToolRegistryForServerWithExternal(server *Server, external []ExternalToolDescriptor) (AssistantToolRegistry, error) {
	if server == nil {
		return NewAssistantToolRegistryWithExternalTools(nil, external)
	}
	return NewAssistantToolRegistryWithExternalTools(server.GetTools(), external)
}

// AssistantToolRegistry constructs the server-scoped assistant discovery
// registry without changing the public MCP GetTools/CallTool surface.
func (s *Server) AssistantToolRegistry() AssistantToolRegistry {
	return NewAssistantToolRegistryForServer(s)
}

// MergeExternalTools adds external MCP descriptors to the assistant discovery
// surface. External MCP is opt-in at configuration time; this method only merges
// descriptors passed by the application wiring.
func (r *AssistantToolRegistry) MergeExternalTools(external []ExternalToolDescriptor) error {
	if len(external) == 0 {
		return nil
	}
	if r.descriptors == nil {
		r.descriptors = make(map[string]ToolDescriptor)
	}
	seenInBatch := map[string]struct{}{}
	for _, ext := range external {
		serverName := strings.TrimSpace(ext.ServerName)
		tool := ext.Tool
		tool.Name = strings.TrimSpace(tool.Name)
		tool.Description = strings.TrimSpace(tool.Description)
		if serverName == "" {
			return fmt.Errorf("external MCP tool %q is missing server name", tool.Name)
		}
		if tool.Name == "" {
			return fmt.Errorf("external MCP server %s returned a tool with an empty name", serverName)
		}
		if _, ok := r.descriptors[tool.Name]; ok {
			return fmt.Errorf("external MCP tool %q from server %s collides with an existing assistant tool", tool.Name, serverName)
		}
		if _, ok := seenInBatch[tool.Name]; ok {
			return fmt.Errorf("external MCP tool %q is registered by more than one external server", tool.Name)
		}
		seenInBatch[tool.Name] = struct{}{}
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		descriptor := ToolDescriptor{
			Tool:               tool,
			ExecutionMode:      domain.AssistantToolExecutionModeSync,
			Effect:             normalizeExternalToolEffect(ext.Effect),
			DefaultRisk:        normalizeExternalToolRisk(ext.DefaultRisk),
			ResourceTypes:      cloneStringSlice(ext.ResourceTypes),
			ResourceIDFields:   cloneStringSlice(ext.ResourceIDFields),
			AgentSafe:          ext.AgentSafe,
			ExternalServerName: serverName,
		}
		r.descriptors[tool.Name] = descriptor
		r.order = append(r.order, tool.Name)
	}
	return nil
}

// All returns every descriptor known to the assistant registry, including
// descriptors that are not agent-safe. This lets callers explain why a public
// MCP tool is not discoverable to the agent.
func (r AssistantToolRegistry) All() []ToolDescriptor {
	return r.list(false)
}

// AgentTools returns the agent-safe descriptors in stable MCP tool order.
func (r AssistantToolRegistry) AgentTools() []ToolDescriptor {
	return r.list(true)
}

// AgentMCPTools returns just the wrapped public MCP Tool values for the
// agent-safe descriptors.
func (r AssistantToolRegistry) AgentMCPTools() []Tool {
	descriptors := r.AgentTools()
	tools := make([]Tool, 0, len(descriptors))
	for _, descriptor := range descriptors {
		tools = append(tools, descriptor.Tool)
	}
	return tools
}

// Get returns a descriptor by public MCP tool name.
func (r AssistantToolRegistry) Get(name string) (ToolDescriptor, bool) {
	descriptor, ok := r.descriptors[name]
	return descriptor, ok
}

// GetAgentTool returns a descriptor only when it is safe for assistant model
// discovery.
func (r AssistantToolRegistry) GetAgentTool(name string) (ToolDescriptor, bool) {
	descriptor, ok := r.Get(name)
	if !ok || !descriptor.AgentSafe {
		return ToolDescriptor{}, false
	}
	return descriptor, true
}

func (r AssistantToolRegistry) list(agentSafeOnly bool) []ToolDescriptor {
	if len(r.descriptors) == 0 {
		return nil
	}
	order := r.order
	if len(order) == 0 {
		order = make([]string, 0, len(r.descriptors))
		for name := range r.descriptors {
			order = append(order, name)
		}
		sort.Strings(order)
	}
	out := make([]ToolDescriptor, 0, len(order))
	for _, name := range order {
		descriptor, ok := r.descriptors[name]
		if !ok || (agentSafeOnly && !descriptor.AgentSafe) {
			continue
		}
		out = append(out, descriptor)
	}
	return out
}

type assistantToolMetadata struct {
	executionMode    domain.AssistantToolExecutionMode
	effect           domain.AssistantToolEffect
	defaultRisk      domain.AssistantToolRisk
	resourceTypes    []string
	resourceIDFields []string
	agentSafe        bool
}

func assistantToolDescriptorMetadata() map[string]assistantToolMetadata {
	return map[string]assistantToolMetadata{
		// Service read-model tools.
		"bahia_list_services":              syncRead(domain.AssistantToolRiskLow, true, []string{"service"}, nil),
		"bahia_get_service":                syncRead(domain.AssistantToolRiskLow, true, []string{"service"}, []string{"service_id", "name"}),
		"bahia_get_deployment_status":      syncRead(domain.AssistantToolRiskLow, true, []string{"service", "environment", "deployment"}, []string{"service_id", "environment_id"}),
		"bahia_create_service":             syncMutation(domain.AssistantToolRiskHigh, false, []string{"service"}, []string{"name"}),
		"bahia_update_service":             syncMutation(domain.AssistantToolRiskHigh, false, []string{"service"}, []string{"service_id"}),
		"bahia_delete_service":             syncMutation(domain.AssistantToolRiskDestructive, false, []string{"service"}, []string{"service_id"}),
		"bahia_deploy":                     asyncMutation(domain.AssistantToolRiskMedium, false, []string{"service", "environment", "artifact", "deployment"}, []string{"service_id", "environment_id", "artifact_id"}),
		"bahia_rollback":                   asyncMutation(domain.AssistantToolRiskHigh, false, []string{"service", "environment", "deployment"}, []string{"service_id", "environment_id"}),
		"bahia_approve_deployment":         asyncMutation(domain.AssistantToolRiskHigh, false, []string{"service", "deployment"}, []string{"intent_id"}),
		"bahia_reject_deployment":          asyncMutation(domain.AssistantToolRiskHigh, false, []string{"service", "deployment"}, []string{"intent_id"}),
		"bahia_assistant_service_deploy":   asyncMutation(domain.AssistantToolRiskMedium, true, []string{"service", "environment", "artifact", "deployment"}, []string{"service_id", "environment_id", "artifact_id"}),
		"bahia_assistant_service_rollback": asyncMutation(domain.AssistantToolRiskHigh, true, []string{"service", "environment", "deployment"}, []string{"service_id", "environment_id"}),

		// DNS tools.
		"bahia_dns_list_endpoints":            syncRead(domain.AssistantToolRiskLow, true, []string{"dns_endpoint", "dns_zone"}, nil),
		"bahia_dns_list_drift":                syncRead(domain.AssistantToolRiskLow, true, []string{"dns_endpoint", "dns_drift"}, nil),
		"bahia_assistant_dns_list_endpoints":  syncRead(domain.AssistantToolRiskLow, true, []string{"dns_endpoint", "dns_zone"}, nil),
		"bahia_assistant_dns_list_drift":      syncRead(domain.AssistantToolRiskLow, true, []string{"dns_endpoint", "dns_drift"}, nil),
		"bahia_assistant_dns_zone_create":     asyncMutation(domain.AssistantToolRiskMedium, true, []string{"dns_zone"}, []string{"name", "zone"}),
		"bahia_assistant_dns_policy_apply":    asyncMutation(domain.AssistantToolRiskHigh, true, []string{"dns_policy", "dns_zone", "environment"}, []string{"policy_id", "zone_id", "environment_id"}),
		"bahia_assistant_dns_record_override": asyncMutation(domain.AssistantToolRiskHigh, true, []string{"dns_record", "dns_zone"}, []string{"override_id", "zone_name", "record_name", "record_type"}),
		"bahia_assistant_dns_drift_remediate": asyncMutation(domain.AssistantToolRiskMedium, true, []string{"dns_drift", "dns_zone"}, []string{"zone", "zone_name"}),

		// LLM route/release tools.
		"bahia_llm_list_routes":                  syncRead(domain.AssistantToolRiskLow, true, []string{"llm_route"}, nil),
		"bahia_llm_list_releases":                syncRead(domain.AssistantToolRiskLow, true, []string{"llm_route", "llm_release"}, []string{"route_id"}),
		"bahia_llm_create_route":                 asyncMutation(domain.AssistantToolRiskMedium, false, []string{"llm_route"}, []string{"name"}),
		"bahia_llm_update_route":                 syncMutation(domain.AssistantToolRiskMedium, false, []string{"llm_route"}, []string{"route_id"}),
		"bahia_llm_register_release":             asyncMutation(domain.AssistantToolRiskMedium, false, []string{"llm_route", "llm_release", "model"}, []string{"route_id", "version", "model_ref"}),
		"bahia_llm_deploy":                       asyncMutation(domain.AssistantToolRiskMedium, false, []string{"llm_route", "llm_release", "environment", "deployment"}, []string{"route_id", "release_id", "environment_id"}),
		"bahia_llm_approve_deployment":           asyncMutation(domain.AssistantToolRiskHigh, false, []string{"llm_route", "deployment"}, []string{"intent_id"}),
		"bahia_llm_reject_deployment":            asyncMutation(domain.AssistantToolRiskHigh, false, []string{"llm_route", "deployment"}, []string{"intent_id"}),
		"bahia_llm_rollback":                     asyncMutation(domain.AssistantToolRiskHigh, false, []string{"llm_route", "environment", "deployment"}, []string{"route_id", "environment_id"}),
		"bahia_assistant_llm_deploy":             asyncMutation(domain.AssistantToolRiskMedium, true, []string{"llm_route", "llm_release", "environment", "deployment"}, []string{"route_id", "release_id", "environment_id"}),
		"bahia_assistant_llm_approve_deployment": asyncMutation(domain.AssistantToolRiskHigh, true, []string{"llm_route", "deployment"}, []string{"intent_id"}),
		"bahia_assistant_llm_rollback":           asyncMutation(domain.AssistantToolRiskHigh, true, []string{"llm_route", "environment", "deployment"}, []string{"route_id", "environment_id"}),

		// ML model/recipe/inference tools.
		"bahia_ml_list_state":                   syncRead(domain.AssistantToolRiskLow, true, []string{"ml_endpoint", "ml_inference_state"}, nil),
		"bahia_ml_get_state":                    syncRead(domain.AssistantToolRiskLow, true, []string{"ml_endpoint", "ml_inference_state", "environment"}, []string{"endpoint_id", "environment_id"}),
		"bahia_ml_get_provenance":               syncRead(domain.AssistantToolRiskLow, true, []string{"ml_artifact", "ml_provenance"}, []string{"artifact_id"}),
		"bahia_ml_import_model":                 asyncMutation(domain.AssistantToolRiskMedium, false, []string{"ml_model", "ml_model_version", "ml_artifact"}, []string{"model", "model_version", "artifact"}),
		"bahia_ml_run_recipe":                   asyncMutation(domain.AssistantToolRiskMedium, false, []string{"ml_recipe", "ml_run"}, []string{"recipe"}),
		"bahia_ml_deploy":                       asyncMutation(domain.AssistantToolRiskMedium, false, []string{"ml_endpoint", "ml_model_version", "environment", "deployment"}, []string{"endpoint", "endpoint_id", "model_version", "model_version_id"}),
		"bahia_ml_rollback":                     asyncMutation(domain.AssistantToolRiskHigh, false, []string{"ml_endpoint", "environment", "deployment"}, []string{"endpoint", "endpoint_id"}),
		"bahia_assistant_ml_deploy":             asyncMutation(domain.AssistantToolRiskMedium, true, []string{"ml_endpoint", "ml_model_version", "environment", "deployment"}, []string{"endpoint", "endpoint_id", "model_version", "model_version_id"}),
		"bahia_assistant_ml_approve_deployment": asyncMutation(domain.AssistantToolRiskHigh, true, []string{"ml_endpoint", "deployment"}, []string{"intent_id"}),
		"bahia_assistant_ml_rollback":           asyncMutation(domain.AssistantToolRiskHigh, true, []string{"ml_endpoint", "environment", "deployment"}, []string{"endpoint", "endpoint_id"}),
	}
}

func syncRead(risk domain.AssistantToolRisk, agentSafe bool, resourceTypes, resourceIDFields []string) assistantToolMetadata {
	return assistantToolMetadata{executionMode: domain.AssistantToolExecutionModeSync, effect: domain.AssistantToolEffectRead, defaultRisk: risk, resourceTypes: resourceTypes, resourceIDFields: resourceIDFields, agentSafe: agentSafe}
}

func syncMutation(risk domain.AssistantToolRisk, agentSafe bool, resourceTypes, resourceIDFields []string) assistantToolMetadata {
	return assistantToolMetadata{executionMode: domain.AssistantToolExecutionModeSync, effect: domain.AssistantToolEffectMutation, defaultRisk: risk, resourceTypes: resourceTypes, resourceIDFields: resourceIDFields, agentSafe: agentSafe}
}

func asyncMutation(risk domain.AssistantToolRisk, agentSafe bool, resourceTypes, resourceIDFields []string) assistantToolMetadata {
	return assistantToolMetadata{executionMode: domain.AssistantToolExecutionModeAsync, effect: domain.AssistantToolEffectMutation, defaultRisk: risk, resourceTypes: resourceTypes, resourceIDFields: resourceIDFields, agentSafe: agentSafe}
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func normalizeExternalToolEffect(effect domain.AssistantToolEffect) domain.AssistantToolEffect {
	switch domain.AssistantToolEffect(strings.ToLower(strings.TrimSpace(string(effect)))) {
	case domain.AssistantToolEffectRead:
		return domain.AssistantToolEffectRead
	case domain.AssistantToolEffectMutation:
		return domain.AssistantToolEffectMutation
	default:
		return domain.AssistantToolEffectMutation
	}
}

func normalizeExternalToolRisk(risk domain.AssistantToolRisk) domain.AssistantToolRisk {
	switch domain.AssistantToolRisk(strings.ToLower(strings.TrimSpace(string(risk)))) {
	case domain.AssistantToolRiskLow:
		return domain.AssistantToolRiskLow
	case domain.AssistantToolRiskMedium:
		return domain.AssistantToolRiskMedium
	case domain.AssistantToolRiskHigh:
		return domain.AssistantToolRiskHigh
	case domain.AssistantToolRiskDestructive:
		return domain.AssistantToolRiskDestructive
	default:
		return domain.AssistantToolRiskHigh
	}
}
