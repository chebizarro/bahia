// Package agentmemory provides typed helpers for the agent-memory MCP server.
package agentmemory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/mcpclient"
)

var (
	// ErrNotConfigured is returned when an operation is attempted without an explicit agent-memory URL.
	ErrNotConfigured = errors.New("agent-memory client not configured")
	// ErrTaskIDStoreNotConfigured prevents restart-unsafe, process-only task identity.
	ErrTaskIDStoreNotConfigured = errors.New("agent-memory durable task ID store not configured")
)

// Client communicates with agent-memory through the generic MCP client while
// preserving Soul Factory's typed helper methods.
type Client struct {
	baseURL string
	client  *mcpclient.Client
	logger  *slog.Logger

	taskMu     sync.Mutex
	taskIDFile string
}

// Config holds client configuration.
type Config struct {
	URL         string            // MCP server URL (e.g., http://localhost:8282)
	Timeout     time.Duration     // Request timeout
	AuthHeaders map[string]string // Optional MCP HTTP auth headers
	TaskIDFile  string            // Required durable JSON file for agent-to-task IDs
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
		logger:     componentLogger,
		taskIDFile: strings.TrimSpace(config.TaskIDFile),
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
	if err := c.requireConfigured(); err != nil {
		return err
	}
	if c.taskIDFile == "" {
		return ErrTaskIDStoreNotConfigured
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent ID is required")
	}

	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	taskIDs, err := c.loadTaskIDs()
	if err != nil {
		return err
	}
	taskID := taskIDs[agentID]
	if taskID == "" {
		c.logger.Info("starting agent memory task", "agent_id", agentID, "npub", npub)
		goal := fmt.Sprintf("Seed non-personal fleet provisioning context for Soul Factory agent %s.", agentID)
		result, err := c.callToolText(ctx, "memory_task_start", map[string]interface{}{
			"agent": agentID,
			"goal":  goal,
		})
		if err != nil {
			return fmt.Errorf("start memory task: %w", err)
		}
		taskID = parseTaskID(result)
		if taskID == "" {
			return fmt.Errorf("start memory task: response did not include task_id")
		}
		taskIDs[agentID] = taskID
		if err := c.persistTaskIDs(taskIDs); err != nil {
			return fmt.Errorf("persist memory task ID: %w", err)
		}
	}

	_, err = c.callTool(ctx, "memory_event", map[string]interface{}{
		"task_id": taskID,
		"agent":   agentID,
		"action":  "agent_identity_registered",
		"summary": "Registered Soul Factory agent identity metadata.",
		"detail": map[string]interface{}{
			"npub":     strings.TrimSpace(npub),
			"metadata": metadata,
		},
	})
	if err != nil {
		return fmt.Errorf("record agent identity metadata: %w", err)
	}
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
	if c.taskIDFile == "" {
		return "", ErrTaskIDStoreNotConfigured
	}
	c.taskMu.Lock()
	taskIDs, err := c.loadTaskIDs()
	c.taskMu.Unlock()
	if err != nil {
		return "", err
	}
	if taskID := taskIDs[agentID]; taskID != "" {
		return taskID, nil
	}
	if err := c.RegisterAgent(ctx, agentID, "", nil); err != nil {
		return "", err
	}
	c.taskMu.Lock()
	taskIDs, err = c.loadTaskIDs()
	c.taskMu.Unlock()
	if err != nil {
		return "", err
	}
	taskID := taskIDs[agentID]
	if taskID == "" {
		return "", fmt.Errorf("memory task id missing for agent %s", agentID)
	}
	return taskID, nil
}

func (c *Client) loadTaskIDs() (map[string]string, error) {
	data, err := os.ReadFile(c.taskIDFile)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read memory task ID store: %w", err)
	}
	var taskIDs map[string]string
	if err := json.Unmarshal(data, &taskIDs); err != nil {
		return nil, fmt.Errorf("decode memory task ID store: %w", err)
	}
	if taskIDs == nil {
		taskIDs = make(map[string]string)
	}
	return taskIDs, nil
}

func (c *Client) persistTaskIDs(taskIDs map[string]string) error {
	data, err := json.Marshal(taskIDs)
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.taskIDFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-memory-task-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, c.taskIDFile)
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
