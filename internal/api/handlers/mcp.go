package handlers

import (
	"encoding/json"
	"net/http"

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

	resp := CallToolResponse{
		Content: result.Content,
		IsError: result.IsError,
	}

	// Use 200 even for tool errors - the error is in the response body
	writeData(w, http.StatusOK, resp)
}

// GetServerInfo returns MCP server information.
func (h *MCPHandler) GetServerInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{
		"name":         "bahia-mcp",
		"version":      "1.0.0",
		"description":  "Bahia Deployment Registry MCP Server",
		"capabilities": []string{"tools"},
	}
	writeData(w, http.StatusOK, info)
}
