// Package mcpclient provides a generic JSON-RPC-over-HTTP MCP client.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openagentsinc/bahia/internal/httpclient"
)

const (
	defaultTimeout     = 30 * time.Second
	maxMCPResponseBody = 4 << 20
)

// ErrNotConfigured is returned when a client operation is attempted without a URL.
var ErrNotConfigured = errors.New("mcp client not configured")

// Config holds external MCP client configuration.
type Config struct {
	Name        string
	URL         string
	ToolPrefix  string
	Timeout     time.Duration
	AuthHeaders map[string]string
	HTTPClient  *http.Client
}

// Client is a JSON-RPC-over-HTTP MCP client.
type Client struct {
	name        string
	endpoint    string
	toolPrefix  string
	authHeaders map[string]string
	httpClient  *http.Client
	logger      *slog.Logger
	nextID      atomic.Int64
}

// JSONRPCRequest represents a JSON-RPC request.
type JSONRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error object.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *JSONRPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Message == "" {
		return fmt.Sprintf("MCP error %d", e.Code)
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// InitializeResult is the MCP initialize result subset Bahia needs.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion,omitempty"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`
	ServerInfo      map[string]any `json:"serverInfo,omitempty"`
}

// ToolSchema is an MCP tool schema projected into Bahia's agent-tool shape.
// Name is always the externally visible, prefixed name. RawName is the upstream
// MCP tool name used when issuing tools/call.
type ToolSchema struct {
	Name        string         `json:"name"`
	RawName     string         `json:"raw_name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// CallToolResult is the MCP tools/call result subset used by the assistant.
type CallToolResult struct {
	Content []Content       `json:"content,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// Content represents one MCP content block.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// NewClient creates a configured MCP client. Empty URLs are allowed so callers
// can construct optional clients and get ErrNotConfigured on use.
func NewClient(config Config, logger *slog.Logger) *Client {
	name := strings.TrimSpace(config.Name)
	endpoint := normalizeEndpoint(config.URL)
	prefix := strings.TrimSpace(config.ToolPrefix)
	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	httpClient := httpclient.Harden(config.HTTPClient, config.Timeout)
	headers := make(map[string]string, len(config.AuthHeaders))
	for key, value := range config.AuthHeaders {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	return &Client{
		name:        name,
		endpoint:    endpoint,
		toolPrefix:  prefix,
		authHeaders: headers,
		httpClient:  httpClient,
		logger:      logger.With("component", "mcpclient", "server", name),
	}
}

// Configured reports whether the client has an explicit endpoint.
func (c *Client) Configured() bool {
	return c != nil && strings.TrimSpace(c.endpoint) != ""
}

// Name returns the configured server name.
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// ToolPrefix returns the configured external tool-name prefix.
func (c *Client) ToolPrefix() string {
	if c == nil {
		return ""
	}
	return c.toolPrefix
}

// Initialize performs the MCP initialize handshake.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "bahia-assistant",
			"version": "1.0",
		},
	}
	result, err := c.Call(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	var initialized InitializeResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &initialized); err != nil {
			return nil, fmt.Errorf("decode initialize result: %w", err)
		}
	}
	return &initialized, nil
}

// ListTools calls tools/list and returns prefixed schemas suitable for assistant discovery.
func (c *Client) ListTools(ctx context.Context) ([]ToolSchema, error) {
	result, err := c.Call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Tools []struct {
			Name             string          `json:"name"`
			Description      string          `json:"description"`
			InputSchema      map[string]any  `json:"inputSchema"`
			InputSchemaSnake map[string]any  `json:"input_schema"`
			RawInputSchema   json.RawMessage `json:"-"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		return nil, fmt.Errorf("decode tools/list result: %w", err)
	}
	tools := make([]ToolSchema, 0, len(decoded.Tools))
	seen := map[string]struct{}{}
	for _, tool := range decoded.Tools {
		rawName := strings.TrimSpace(tool.Name)
		if rawName == "" {
			return nil, fmt.Errorf("tools/list from %s included a tool with an empty name", c.name)
		}
		prefixed := c.PrefixToolName(rawName)
		if _, ok := seen[prefixed]; ok {
			return nil, fmt.Errorf("tools/list from %s produced duplicate prefixed tool name %q", c.name, prefixed)
		}
		seen[prefixed] = struct{}{}
		schema := tool.InputSchema
		if schema == nil {
			schema = tool.InputSchemaSnake
		}
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, ToolSchema{
			Name:        prefixed,
			RawName:     rawName,
			Description: strings.TrimSpace(tool.Description),
			InputSchema: cloneMap(schema),
		})
	}
	return tools, nil
}

// CallTool calls an MCP tool by its externally visible prefixed name. For typed
// wrappers that intentionally use an empty ToolPrefix, name is also the raw name.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*CallToolResult, error) {
	rawName, err := c.RawToolName(name)
	if err != nil {
		return nil, err
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	params := map[string]any{
		"name":      rawName,
		"arguments": arguments,
	}
	result, err := c.Call(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	callResult := CallToolResult{Raw: cloneRawMessage(result)}
	if len(result) == 0 {
		return &callResult, nil
	}
	if err := json.Unmarshal(result, &callResult); err != nil {
		return nil, fmt.Errorf("decode tools/call result for %s: %w", name, err)
	}
	callResult.Raw = cloneRawMessage(result)
	return &callResult, nil
}

// Call sends an arbitrary JSON-RPC request to the configured MCP endpoint.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c == nil || !c.Configured() {
		return nil, ErrNotConfigured
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return nil, fmt.Errorf("MCP JSON-RPC method is required")
	}
	id := c.nextID.Add(1)
	reqBody, err := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", method, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for key, value := range c.authHeaders {
		httpReq.Header.Set(key, value)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send %s request: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", method, err)
	}
	if len(body) > maxMCPResponseBody {
		return nil, fmt.Errorf("read %s response: body exceeds %d bytes", method, maxMCPResponseBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP HTTP error %d for %s: %s", resp.StatusCode, method, string(body))
	}
	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", method, err)
	}
	if rpcResp.JSONRPC != "" && rpcResp.JSONRPC != "2.0" {
		return nil, fmt.Errorf("invalid JSON-RPC version %q in %s response", rpcResp.JSONRPC, method)
	}
	if rpcResp.ID != 0 && rpcResp.ID != id {
		return nil, fmt.Errorf("mismatched JSON-RPC id in %s response: got %d want %d", method, rpcResp.ID, id)
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return cloneRawMessage(rpcResp.Result), nil
}

// PrefixToolName returns the externally visible tool name.
func (c *Client) PrefixToolName(rawName string) string {
	rawName = strings.TrimSpace(rawName)
	if c == nil || c.toolPrefix == "" || strings.HasPrefix(rawName, c.toolPrefix) {
		return rawName
	}
	return c.toolPrefix + rawName
}

// RawToolName converts an externally visible tool name to the upstream MCP name.
func (c *Client) RawToolName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("MCP tool name is required")
	}
	if c == nil || c.toolPrefix == "" {
		return name, nil
	}
	if !strings.HasPrefix(name, c.toolPrefix) {
		return "", fmt.Errorf("external MCP tool %q does not use required prefix %q", name, c.toolPrefix)
	}
	raw := strings.TrimPrefix(name, c.toolPrefix)
	if raw == "" {
		return "", fmt.Errorf("external MCP tool %q has empty upstream tool name after prefix %q", name, c.toolPrefix)
	}
	return raw, nil
}

// ResultJSON returns the most likely JSON payload emitted by an MCP tool. It
// supports both structured JSON results and JSON-encoded text content blocks.
func (r *CallToolResult) ResultJSON() json.RawMessage {
	if r == nil {
		return nil
	}
	if len(r.Content) > 0 {
		for _, content := range r.Content {
			if strings.TrimSpace(content.Text) == "" {
				continue
			}
			text := strings.TrimSpace(content.Text)
			if json.Valid([]byte(text)) {
				return json.RawMessage(text)
			}
		}
	}
	if len(r.Raw) == 0 {
		return nil
	}
	var rawObject map[string]json.RawMessage
	if err := json.Unmarshal(r.Raw, &rawObject); err == nil {
		for _, key := range []string{"structuredContent", "structured_content", "result"} {
			if value := rawObject[key]; len(value) > 0 {
				return cloneRawMessage(value)
			}
		}
	}
	return cloneRawMessage(r.Raw)
}

func normalizeEndpoint(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/mcp"
	}
	return parsed.String()
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}
