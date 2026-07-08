// Package agentmemory provides typed helpers for the agent-memory MCP server.
package agentmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/mcpclient"
)

// ErrNotConfigured is returned when an operation is attempted without an explicit agent-memory URL.
var ErrNotConfigured = errors.New("agent-memory client not configured")

// Client communicates with agent-memory through the generic MCP client while
// preserving Soul Factory's typed helper methods.
type Client struct {
	baseURL string
	client  *mcpclient.Client
	logger  *slog.Logger

	mu      sync.Mutex
	taskIDs map[string]string
}

// Config holds client configuration.
type Config struct {
	URL         string            // MCP server URL (e.g., http://localhost:8282)
	Timeout     time.Duration     // Request timeout
	AuthHeaders map[string]string // Optional MCP HTTP auth headers
}

// NewClient creates a new agent-memory client.
func NewClient(config Config, logger *slog.Logger) *Client {
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	componentLogger := logger.With("component", "agentmemory")

	return &Client{
		baseURL: config.URL,
		client: mcpclient.NewClient(mcpclient.Config{
			Name:        "agent-memory",
			URL:         config.URL,
			Timeout:     config.Timeout,
			AuthHeaders: config.AuthHeaders,
		}, componentLogger),
		logger:  componentLogger,
		taskIDs: make(map[string]string),
	}
}

// Configured reports whether this client has an explicit agent-memory endpoint.
func (c *Client) Configured() bool {
	return c != nil && c.client != nil && c.client.Configured()
}

func (c *Client) requireConfigured() error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	return nil
}

// RegisterAgent opens the agent's initial fleet-provisioning memory task.
func (c *Client) RegisterAgent(ctx context.Context, agentID string, npub string, metadata map[string]interface{}) error {
	c.logger.Info("starting agent memory task",
		"agent_id", agentID,
		"npub", npub,
	)

	goal := fmt.Sprintf("Seed non-personal fleet provisioning context for Soul Factory agent %s.", agentID)
	result, err := c.callToolText(ctx, "memory_task_start", map[string]interface{}{
		"agent": agentID,
		"goal":  goal,
	})
	if err != nil {
		return fmt.Errorf("start memory task: %w", err)
	}
	taskID := parseTaskID(result)
	if taskID == "" {
		return fmt.Errorf("start memory task: response did not include task_id")
	}
	c.mu.Lock()
	c.taskIDs[agentID] = taskID
	c.mu.Unlock()
	return nil
}

// SeedMemory adds initial context/memory for an agent.
func (c *Client) SeedMemory(ctx context.Context, agentID string, entries []MemoryEntry) error {
	c.logger.Info("seeding agent memory",
		"agent_id", agentID,
		"entries", len(entries),
	)

	taskID, err := c.taskID(ctx, agentID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		params := map[string]interface{}{
			"task_id": taskID,
			"agent":   agentID,
			"action":  "soul_factory_seed",
			"summary": entry.Content,
			"detail": map[string]interface{}{
				"type":     entry.Type,
				"metadata": entry.Metadata,
			},
		}

		if _, err := c.callTool(ctx, "memory_event", params); err != nil {
			return fmt.Errorf("record memory seed event: %w", err)
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
	result, err := c.callTool(ctx, "context_get", map[string]interface{}{
		"agent_id": agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("get context: %w", err)
	}

	var entries []MemoryEntry
	if len(result) > 0 {
		if err := json.Unmarshal(result, &entries); err != nil {
			return nil, fmt.Errorf("unmarshal context: %w", err)
		}
	}

	return entries, nil
}

func (c *Client) taskID(ctx context.Context, agentID string) (string, error) {
	c.mu.Lock()
	taskID := c.taskIDs[agentID]
	c.mu.Unlock()
	if taskID != "" {
		return taskID, nil
	}
	if err := c.RegisterAgent(ctx, agentID, "", nil); err != nil {
		return "", err
	}
	c.mu.Lock()
	taskID = c.taskIDs[agentID]
	c.mu.Unlock()
	if taskID == "" {
		return "", fmt.Errorf("memory task id missing for agent %s", agentID)
	}
	return taskID, nil
}

func parseTaskID(result string) string {
	for _, line := range strings.Split(result, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "task_id" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (c *Client) callTool(ctx context.Context, name string, params map[string]interface{}) (json.RawMessage, error) {
	result, err := c.callToolResult(ctx, name, params)
	if err != nil {
		return nil, err
	}
	return result.ResultJSON(), nil
}

func (c *Client) callToolText(ctx context.Context, name string, params map[string]interface{}) (string, error) {
	result, err := c.callToolResult(ctx, name, params)
	if err != nil {
		return "", err
	}
	for _, content := range result.Content {
		if strings.TrimSpace(content.Text) != "" {
			return content.Text, nil
		}
	}
	return string(result.Raw), nil
}

func (c *Client) callToolResult(ctx context.Context, name string, params map[string]interface{}) (*mcpclient.CallToolResult, error) {
	if err := c.requireConfigured(); err != nil {
		return nil, err
	}
	args := make(map[string]any, len(params))
	for key, value := range params {
		args[key] = value
	}
	result, err := c.client.CallTool(ctx, name, args)
	if err != nil {
		if errors.Is(err, mcpclient.ErrNotConfigured) {
			return nil, ErrNotConfigured
		}
		return nil, err
	}
	if result != nil && result.IsError {
		return nil, fmt.Errorf("agent-memory tool %s returned an MCP error result", name)
	}
	return result, nil
}

// Health checks agent-memory connectivity.
func (c *Client) Health(ctx context.Context) error {
	if err := c.requireConfigured(); err != nil {
		return err
	}
	_, err := c.client.Initialize(ctx)
	if errors.Is(err, mcpclient.ErrNotConfigured) {
		return ErrNotConfigured
	}
	return err
}
