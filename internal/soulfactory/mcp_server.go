package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/openagentsinc/bahia/internal/domain"
)

// MCPServer provides an MCP-compatible interface for Soul Factory operations.
// This allows agents to interact with Soul Factory programmatically.
type MCPServer struct {
	reactor     *Reactor
	provisioner *FullProvisioner
	logger      *slog.Logger
}

// MCPServerConfig holds MCP server configuration.
type MCPServerConfig struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// NewMCPServer creates a new MCP server for Soul Factory.
func NewMCPServer(reactor *Reactor, provisioner *FullProvisioner, logger *slog.Logger) *MCPServer {
	return &MCPServer{
		reactor:     reactor,
		provisioner: provisioner,
		logger:      logger.With("component", "mcp-server"),
	}
}

// --- MCP Tool Definitions ---

// MCPTool represents an MCP tool definition.
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// GetTools returns the list of available MCP tools.
func (s *MCPServer) GetTools() []MCPTool {
	return []MCPTool{
		{
			Name:        "soul_factory_list_souls",
			Description: "List all provisioned agent souls",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Filter by status (active, suspended, revoked)",
						"enum":        []string{"active", "suspended", "revoked", "provisioning"},
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results",
						"default":     50,
					},
				},
			},
		},
		{
			Name:        "soul_factory_get_soul",
			Description: "Get details for a specific agent soul",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "The agent's unique identifier",
					},
				},
				"required": []string{"agent_id"},
			},
		},
		{
			Name:        "soul_factory_list_templates",
			Description: "List available soul templates",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"tier": map[string]interface{}{
						"type":        "string",
						"description": "Filter by resource tier",
						"enum":        []string{"lightweight", "standard", "heavy"},
					},
				},
			},
		},
		{
			Name:        "soul_factory_provision",
			Description: "Provision a new agent soul. Returns immediately with a request ID that can be used to track progress.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "Unique identifier for the agent (lowercase, no spaces)",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Display name for the agent",
					},
					"brief": map[string]interface{}{
						"type":        "string",
						"description": "Description of the agent's purpose and behavior",
					},
					"tier": map[string]interface{}{
						"type":        "string",
						"description": "Resource tier",
						"enum":        []string{"lightweight", "standard", "heavy"},
						"default":     "standard",
					},
					"template": map[string]interface{}{
						"type":        "string",
						"description": "Template reference (31950:pubkey:identifier)",
					},
				},
				"required": []string{"agent_id", "brief"},
			},
		},
		{
			Name:        "soul_factory_action",
			Description: "Execute a lifecycle action on a soul (suspend, resume, revoke, redeploy)",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "The agent's unique identifier",
					},
					"action": map[string]interface{}{
						"type":        "string",
						"description": "The action to perform",
						"enum":        []string{"suspend", "resume", "revoke", "redeploy"},
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Reason for the action (optional)",
					},
				},
				"required": []string{"agent_id", "action"},
			},
		},
		{
			Name:        "soul_factory_regenerate",
			Description: "Regenerate a soul's identity with a new brief",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "The agent's unique identifier",
					},
					"new_brief": map[string]interface{}{
						"type":        "string",
						"description": "New brief/description for the agent",
					},
				},
				"required": []string{"agent_id", "new_brief"},
			},
		},
		{
			Name:        "soul_factory_get_status",
			Description: "Get the status of a provisioning request",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"request_id": map[string]interface{}{
						"type":        "string",
						"description": "The provisioning request event ID",
					},
				},
				"required": []string{"request_id"},
			},
		},
	}
}

// --- MCP Tool Handlers ---

// MCPToolResult represents the result of an MCP tool call.
type MCPToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPContent represents content in an MCP response.
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallTool handles an MCP tool call.
func (s *MCPServer) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (*MCPToolResult, error) {
	s.logger.Info("tool call", "tool", name)

	switch name {
	case "soul_factory_list_souls":
		return s.handleListSouls(ctx, arguments)
	case "soul_factory_get_soul":
		return s.handleGetSoul(ctx, arguments)
	case "soul_factory_list_templates":
		return s.handleListTemplates(ctx, arguments)
	case "soul_factory_provision":
		return s.handleProvision(ctx, arguments)
	case "soul_factory_action":
		return s.handleAction(ctx, arguments)
	case "soul_factory_regenerate":
		return s.handleRegenerate(ctx, arguments)
	case "soul_factory_get_status":
		return s.handleGetStatus(ctx, arguments)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name)), nil
	}
}

func (s *MCPServer) handleListSouls(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	return errorResult("soul listing is unavailable: relay-backed list queries are not implemented"), nil
}

func (s *MCPServer) handleGetSoul(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	agentID, ok := args["agent_id"].(string)
	if !ok {
		return errorResult("agent_id is required"), nil
	}

	soul, err := s.reactor.GetSoul(ctx, agentID)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get soul: %v", err)), nil
	}
	if soul == nil {
		return errorResult(fmt.Sprintf("soul not found: %s", agentID)), nil
	}

	return jsonResult(soulToMap(soul))
}

func (s *MCPServer) handleListTemplates(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	return errorResult("template listing is unavailable: relay-backed template queries are not implemented"), nil
}

func (s *MCPServer) handleProvision(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	agentID, _ := args["agent_id"].(string)
	name, _ := args["name"].(string)
	brief, _ := args["brief"].(string)
	tier, _ := args["tier"].(string)
	template, _ := args["template"].(string)

	if agentID == "" {
		return errorResult("agent_id is required"), nil
	}
	if brief == "" && template == "" {
		return errorResult("brief or template is required"), nil
	}

	if name == "" {
		name = agentID
	}
	if tier == "" {
		tier = "standard"
	}

	return errorResult("provisioning is unavailable: MCP server cannot publish a signed Nostr request in this configuration"), nil
}

func (s *MCPServer) handleAction(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	agentID, _ := args["agent_id"].(string)
	action, _ := args["action"].(string)
	reason, _ := args["reason"].(string)
	_ = reason

	if agentID == "" {
		return errorResult("agent_id is required"), nil
	}
	if action == "" {
		return errorResult("action is required"), nil
	}

	// Validate action
	validActions := map[string]bool{
		"suspend": true, "resume": true, "revoke": true, "redeploy": true,
	}
	if !validActions[action] {
		return errorResult(fmt.Sprintf("invalid action: %s", action)), nil
	}

	return errorResult(fmt.Sprintf("lifecycle action %q is unavailable: MCP server cannot publish a signed Nostr action in this configuration", action)), nil
}

func (s *MCPServer) handleRegenerate(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	agentID, _ := args["agent_id"].(string)
	newBrief, _ := args["new_brief"].(string)

	if agentID == "" {
		return errorResult("agent_id is required"), nil
	}
	if newBrief == "" {
		return errorResult("new_brief is required"), nil
	}

	return errorResult("regeneration is unavailable: MCP server cannot publish a signed Nostr action in this configuration"), nil
}

func (s *MCPServer) handleGetStatus(ctx context.Context, args map[string]interface{}) (*MCPToolResult, error) {
	requestID, _ := args["request_id"].(string)

	if requestID == "" {
		return errorResult("request_id is required"), nil
	}

	run := s.reactor.GetRun(requestID)
	if run == nil {
		return errorResult(fmt.Sprintf("request not found: %s", requestID)), nil
	}

	result := map[string]interface{}{
		"request_id":   run.RequestID,
		"agent_id":     run.AgentID,
		"status":       run.Status,
		"current_step": run.CurrentStep,
		"steps":        run.Steps,
		"error":        run.Error,
	}

	if run.SoulID != nil {
		result["soul_id"] = run.SoulID.String()
	}

	return jsonResult(result)
}

// --- Helper Functions ---

func jsonResult(data interface{}) (*MCPToolResult, error) {
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal result: %v", err)), nil
	}

	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: string(jsonBytes),
		}},
	}, nil
}

func errorResult(message string) *MCPToolResult {
	return &MCPToolResult{
		Content: []MCPContent{{
			Type: "text",
			Text: message,
		}},
		IsError: true,
	}
}

func soulToMap(soul *domain.AgentSoul) map[string]interface{} {
	m := map[string]interface{}{
		"agent_id":      soul.AgentID,
		"name":          soul.Name,
		"purpose":       soul.Purpose,
		"status":        soul.Status,
		"tier":          soul.Tier,
		"npub":          soul.NostrNpub,
		"pubkey":        soul.NostrPubkey,
		"nip05":         soul.NIP05,
		"avatar_url":    soul.AvatarURL,
		"workspace":     soul.WorkspaceRepoURL,
		"qdrant":        soul.QdrantCollection,
		"deploy_status": soul.DeployStatus,
		"allowed_kinds": soul.AllowedKinds,
		"tools":         soul.ToolGrants,
		"created_at":    soul.CreatedAt,
	}

	if soul.BahiaServiceID != nil {
		m["bahia_service_id"] = soul.BahiaServiceID.String()
	}

	return m
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
