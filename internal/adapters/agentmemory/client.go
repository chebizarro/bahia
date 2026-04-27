// Package agentmemory provides a client for the agent-memory MCP server.
package agentmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client communicates with agent-memory MCP server.
type Client struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// Config holds client configuration.
type Config struct {
	URL     string        // MCP server URL (e.g., http://192.168.40.104:8282)
	Timeout time.Duration // Request timeout
}

// NewClient creates a new agent-memory client.
func NewClient(config Config, logger *slog.Logger) *Client {
	if config.URL == "" {
		config.URL = "http://192.168.40.104:8282"
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		baseURL: config.URL,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger.With("component", "agentmemory"),
	}
}

// MCPRequest represents an MCP JSON-RPC request.
type MCPRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
}

// MCPResponse represents an MCP JSON-RPC response.
type MCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

// MCPError represents an MCP error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RegisterAgent registers a new agent with the memory system.
func (c *Client) RegisterAgent(ctx context.Context, agentID string, npub string, metadata map[string]interface{}) error {
	c.logger.Info("registering agent with memory system",
		"agent_id", agentID,
		"npub", npub,
	)

	params := map[string]interface{}{
		"agent_id": agentID,
		"npub":     npub,
	}
	for k, v := range metadata {
		params[k] = v
	}

	_, err := c.call(ctx, "agent_register", params)
	if err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	return nil
}

// SeedMemory adds initial context/memory for an agent.
func (c *Client) SeedMemory(ctx context.Context, agentID string, entries []MemoryEntry) error {
	c.logger.Info("seeding agent memory",
		"agent_id", agentID,
		"entries", len(entries),
	)

	for _, entry := range entries {
		params := map[string]interface{}{
			"agent_id": agentID,
			"type":     entry.Type,
			"content":  entry.Content,
			"metadata": entry.Metadata,
		}

		if _, err := c.call(ctx, "memory_add", params); err != nil {
			return fmt.Errorf("add memory entry: %w", err)
		}
	}

	return nil
}

// MemoryEntry represents a memory item to seed.
type MemoryEntry struct {
	Type     string                 `json:"type"` // "context", "fact", "reflection"
	Content  string                 `json:"content"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// CreateInitialMemory creates default memory entries for a new agent.
func CreateInitialMemory(agentID, name, purpose, soulMD string) []MemoryEntry {
	return []MemoryEntry{
		{
			Type:    "context",
			Content: fmt.Sprintf("I am %s. %s", name, purpose),
			Metadata: map[string]interface{}{
				"source":    "soul_factory",
				"permanent": true,
			},
		},
		{
			Type:    "fact",
			Content: fmt.Sprintf("My agent ID is %s and my name is %s.", agentID, name),
			Metadata: map[string]interface{}{
				"source":    "soul_factory",
				"permanent": true,
			},
		},
		{
			Type:    "context",
			Content: "I was created by Soul Factory, the fleet's agent provisioning system.",
			Metadata: map[string]interface{}{
				"source":    "soul_factory",
				"permanent": true,
			},
		},
	}
}

// GetAgentContext retrieves an agent's context.
func (c *Client) GetAgentContext(ctx context.Context, agentID string) ([]MemoryEntry, error) {
	result, err := c.call(ctx, "context_get", map[string]interface{}{
		"agent_id": agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("get context: %w", err)
	}

	var entries []MemoryEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("unmarshal context: %w", err)
	}

	return entries, nil
}

// call makes an MCP JSON-RPC call.
func (c *Client) call(ctx context.Context, method string, params map[string]interface{}) (json.RawMessage, error) {
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/mcp"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, string(respBody))
	}

	var mcpResp MCPResponse
	if err := json.Unmarshal(respBody, &mcpResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if mcpResp.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}

	return mcpResp.Result, nil
}

// Health checks agent-memory connectivity.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.call(ctx, "health", nil)
	return err
}
