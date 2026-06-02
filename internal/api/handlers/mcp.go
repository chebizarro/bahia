package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/mcp"
	"go.uber.org/zap"
)

// MCPHandler handles HTTP requests for MCP tool operations.
type MCPHandler struct {
	server *mcp.Server
	logger *zap.Logger
}

// NewMCPHandler creates a new MCP handler.
func NewMCPHandler(server *mcp.Server, logger *zap.Logger) *MCPHandler {
	return &MCPHandler{
		server: server,
		logger: logger,
	}
}

// ListToolsRequest is the request body for listing tools.
type ListToolsRequest struct{}

// ListToolsResponse is the response for listing tools.
type ListToolsResponse struct {
	Tools []mcp.Tool `json:"tools"`
}

// ListTools returns all available MCP tools.
func (h *MCPHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	tools := h.server.GetTools()
	resp := ListToolsResponse{Tools: tools}
	writeData(w, http.StatusOK, resp)
}

// CallToolRequest is the request body for calling a tool.
type CallToolRequest struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// CallToolResponse is the response for a tool call.
type CallToolResponse struct {
	Content []mcp.Content `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// CallTool executes an MCP tool.
func (h *MCPHandler) CallTool(w http.ResponseWriter, r *http.Request) {
	var req CallToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "tool name is required")
		return
	}
	result, err := h.server.CallTool(r.Context(), req.Name, req.Arguments)
	if err != nil {
		h.logger.Error("tool call failed", zap.String("tool", req.Name), zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := CallToolResponse{Content: result.Content, IsError: result.IsError}
	writeData(w, http.StatusOK, resp)
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolResult struct {
	Content []mcp.Content  `json:"content"`
	IsError bool           `json:"isError,omitempty"`
	Meta    map[string]any `json:"_meta,omitempty"`
}

// HandleJSONRPC exposes the native MCP HTTP JSON-RPC transport backed by the same tool registry.
func (h *MCPHandler) HandleJSONRPC(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32600, Message: "invalid jsonrpc version"}})
		return
	}
	switch req.Method {
	case "initialize":
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "bahia-mcp", "version": "1.0.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
		}})
	case "tools/list":
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: ListToolsResponse{Tools: h.server.GetTools()}})
	case "tools/call":
		h.handleJSONRPCToolCall(w, r, req)
	case "resources/list":
		h.handleJSONRPCResourcesList(w, r, req)
	default:
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32601, Message: "method not found"}})
	}
}

func (h *MCPHandler) handleJSONRPCResourcesList(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	resources, err := h.server.GetResources(r.Context())
	if err != nil {
		h.logger.Error("mcp json-rpc resources/list failed", zap.Error(err))
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32000, Message: err.Error()}})
		return
	}
	if resources == nil {
		resources = []mcp.Resource{}
	}
	writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"resources": resources}})
}

func (h *MCPHandler) handleJSONRPCToolCall(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	var params CallToolRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "invalid params"}})
		return
	}
	if params.Name == "" {
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "tool name is required"}})
		return
	}
	result, err := h.server.CallTool(r.Context(), params.Name, params.Arguments)
	if err != nil {
		h.logger.Error("mcp json-rpc tool call failed", zap.String("tool", params.Name), zap.Error(err))
		writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32000, Message: err.Error()}})
		return
	}
	payload := mcpToolResult{Content: result.Content, IsError: result.IsError, Meta: map[string]any{"nostr": h.nostrCorrelationMetadata(params.Name, params.Arguments, result)}}
	writeJSONRPC(w, jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: payload})
}

func (h *MCPHandler) nostrCorrelationMetadata(toolName string, args map[string]interface{}, result *mcp.ToolResult) map[string]any {
	payload := firstJSONContent(result)
	meta := map[string]any{
		"tool":             toolName,
		"correlation_tags": []string{"e", "service", "environment", "intent", "run"},
		"transport_kinds":  []int{kinds.ContextVMMessage, kinds.ContextVMGiftWrap, kinds.ContextVMEphemeralGiftWrap},
		"observable_kinds": []int{kinds.CASControlState, kinds.CASAudit, kinds.NIP38Status},
	}
	for _, key := range []string{"request_event_id", "service_id", "environment_id", "intent_id", "run_id"} {
		if v, ok := stringFromMap(payload, key); ok {
			meta[key] = v
			continue
		}
		if v, ok := stringFromMap(args, key); ok {
			meta[key] = v
		}
	}
	if id, ok := stringFromMap(payload, "id"); ok {
		if key := genericIDCorrelationKey(toolName, payload); key != "" {
			if _, exists := meta[key]; !exists {
				meta[key] = id
			}
		}
	}
	if intentID, ok := stringFromMap(payload, "deployment_intent_id"); ok {
		if _, exists := meta["intent_id"]; !exists {
			meta["intent_id"] = intentID
		}
	}
	return meta
}

func genericIDCorrelationKey(toolName string, payload map[string]interface{}) string {
	switch toolName {
	case "bahia_create_service", "bahia_get_service", "bahia_update_service", "bahia_delete_service":
		return "service_id"
	case "bahia_create_environment", "bahia_get_environment", "bahia_update_environment", "bahia_delete_environment":
		return "environment_id"
	case "bahia_get_intent":
		return "intent_id"
	case "bahia_get_run":
		return "run_id"
	}
	if _, ok := payload["deployment_intent_id"]; ok {
		return "run_id"
	}
	return ""
}

func firstJSONContent(result *mcp.ToolResult) map[string]interface{} {
	if result == nil {
		return nil
	}
	for _, content := range result.Content {
		if content.Type != "text" || content.Text == "" {
			continue
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(content.Text), &payload); err == nil {
			return payload
		}
	}
	return nil
}

func stringFromMap(values map[string]interface{}, key string) (string, bool) {
	if values == nil {
		return "", false
	}
	v, ok := values[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

func writeJSONRPC(w http.ResponseWriter, resp jsonRPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// ListResources returns all available MCP resources.
func (h *MCPHandler) ListResources(w http.ResponseWriter, r *http.Request) {
	resources, err := h.server.GetResources(r.Context())
	if err != nil {
		h.logger.Error("list resources failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if resources == nil {
		resources = []mcp.Resource{}
	}
	writeData(w, http.StatusOK, map[string]any{"resources": resources})
}

// GetServerInfo returns MCP server information.
func (h *MCPHandler) GetServerInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{"name": "bahia-mcp", "version": "1.0.0", "description": "Bahia Deployment Registry MCP Server", "capabilities": []string{"tools"}}
	writeData(w, http.StatusOK, info)
}
