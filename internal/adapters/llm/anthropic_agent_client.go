package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const anthropicVersion = "2023-06-01"

// AnthropicAgentClientConfig configures the Anthropic /v1/messages adapter.
type AnthropicAgentClientConfig struct {
	BaseURL    string
	Model      string
	APIKey     string
	MaxTokens  int
	Timeout    time.Duration
	HTTPClient *http.Client
}

// AnthropicAgentClient serializes AgentModelRequest into Anthropic /v1/messages
// with native tool_use/tool_result content blocks.
type AnthropicAgentClient struct {
	baseURL    string
	model      string
	apiKey     string
	maxTokens  int
	httpClient *http.Client
	logger     *slog.Logger
}

// NewAnthropicAgentClient creates an Anthropic agent model client.
func NewAnthropicAgentClient(config AnthropicAgentClientConfig, logger *slog.Logger) *AnthropicAgentClient {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 4096
	}
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: config.Timeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AnthropicAgentClient{
		baseURL:    strings.TrimRight(config.BaseURL, "/"),
		model:      config.Model,
		apiKey:     config.APIKey,
		maxTokens:  config.MaxTokens,
		httpClient: config.HTTPClient,
		logger:     logger.With("component", "llm_anthropic_agent_client"),
	}
}

// Next asks Anthropic for the next assistant turn.
func (c *AnthropicAgentClient) Next(ctx context.Context, req AgentModelRequest, onEvent AgentModelEventHandler) (*AgentModelResponse, error) {
	bodyMap, err := c.requestBody(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal Anthropic agent request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Anthropic agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	if c.apiKey != "" {
		httpReq.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send Anthropic agent request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Anthropic agent response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isContextLimitResponse(resp.StatusCode, respBody) {
			return nil, &ContextTooLargeError{StatusCode: resp.StatusCode, Message: string(respBody)}
		}
		return nil, fmt.Errorf("Anthropic agent API error %d: %s", resp.StatusCode, string(respBody))
	}

	modelResp, err := parseAnthropicAgentResponse(resp.StatusCode, respBody)
	if err != nil {
		return nil, err
	}
	if onEvent != nil {
		onEvent(AgentModelEvent{Type: AgentModelEventCompleted, Content: modelResp.Content, StopReason: modelResp.StopReason})
	}
	return modelResp, nil
}

func (c *AnthropicAgentClient) requestBody(req AgentModelRequest) (map[string]any, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(c.model)
	}
	if model == "" {
		return nil, fmt.Errorf("llm model is required")
	}
	system, messages, err := anthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": firstPositive(req.MaxTokens, c.maxTokens),
	}
	if system != "" {
		body["system"] = system
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		tools, err := anthropicTools(req.Tools)
		if err != nil {
			return nil, err
		}
		body["tools"] = tools
	}
	if req.ToolChoice.Mode != "" {
		choice, err := anthropicToolChoice(req.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = choice
	}
	return body, nil
}

func anthropicMessages(messages []domain.AssistantAgentMessage) (string, []map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	systemParts := []string{}
	pendingToolResults := []map[string]any{}
	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		out = append(out, map[string]any{"role": "user", "content": pendingToolResults})
		pendingToolResults = nil
	}

	for i, msg := range messages {
		switch msg.Role {
		case domain.AssistantAgentMessageRoleSystem:
			text, err := contentBlocksString(msg.Content)
			if err != nil {
				return "", nil, fmt.Errorf("serialize system message %d: %w", i, err)
			}
			if strings.TrimSpace(text) != "" {
				systemParts = append(systemParts, text)
			}
		case domain.AssistantAgentMessageRoleUser:
			flushToolResults()
			blocks, err := anthropicContentBlocks(msg.Content)
			if err != nil {
				return "", nil, fmt.Errorf("serialize user message %d: %w", i, err)
			}
			out = append(out, map[string]any{"role": "user", "content": blocks})
		case domain.AssistantAgentMessageRoleAssistant:
			flushToolResults()
			blocks, err := anthropicContentBlocks(msg.Content)
			if err != nil {
				return "", nil, fmt.Errorf("serialize assistant message %d: %w", i, err)
			}
			toolBlocks, err := anthropicToolUseBlocks(msg.ToolCalls)
			if err != nil {
				return "", nil, fmt.Errorf("serialize assistant message %d tool calls: %w", i, err)
			}
			blocks = append(blocks, toolBlocks...)
			if len(blocks) == 0 {
				blocks = []map[string]any{{"type": "text", "text": ""}}
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
		case domain.AssistantAgentMessageRoleTool:
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return "", nil, fmt.Errorf("tool message %d missing tool_call_id", i)
			}
			content, err := toolObservationContent(msg)
			if err != nil {
				return "", nil, fmt.Errorf("serialize tool message %d: %w", i, err)
			}
			block := map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": content}
			if msg.Observation != nil && toolObservationIsError(msg.Observation) {
				block["is_error"] = true
			}
			pendingToolResults = append(pendingToolResults, block)
		default:
			return "", nil, fmt.Errorf("message %d has unsupported role %q", i, msg.Role)
		}
	}
	flushToolResults()
	return strings.Join(systemParts, "\n\n"), out, nil
}

func anthropicContentBlocks(blocks []domain.AssistantAgentContentBlock) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "", domain.AssistantAgentContentText:
			if block.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": block.Text})
			}
		case domain.AssistantAgentContentJSON:
			encoded, err := json.Marshal(block.JSON)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "text", "text": string(encoded)})
		case domain.AssistantAgentContentObservation:
			encoded, err := json.Marshal(block.Observation)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": "text", "text": string(encoded)})
		case domain.AssistantAgentContentToolCall:
			continue
		default:
			return nil, fmt.Errorf("unsupported content block type %q", block.Type)
		}
	}
	return out, nil
}

func anthropicToolUseBlocks(calls []domain.AssistantAgentToolCall) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(calls))
	for i, call := range calls {
		id := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Name)
		if id == "" {
			return nil, fmt.Errorf("tool call %d missing id", i)
		}
		if name == "" {
			return nil, fmt.Errorf("tool call %d missing name", i)
		}
		input := call.Arguments
		if input == nil {
			input = map[string]any{}
		}
		out = append(out, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
	}
	return out, nil
}

func toolObservationIsError(obs *domain.AssistantToolObservation) bool {
	if obs == nil {
		return false
	}
	switch obs.Status {
	case domain.AssistantToolObservationFailed, domain.AssistantToolObservationDenied, domain.AssistantToolObservationCancelled:
		return true
	default:
		return false
	}
}

func anthropicTools(tools []AgentToolSchema) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return nil, fmt.Errorf("tool schema %d missing name", i)
		}
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"name": name, "description": tool.Description, "input_schema": schema})
	}
	return out, nil
}

func anthropicToolChoice(choice AgentToolChoice) (map[string]any, error) {
	switch choice.Mode {
	case AgentToolChoiceAuto:
		return map[string]any{"type": "auto"}, nil
	case AgentToolChoiceNone:
		return map[string]any{"type": "none"}, nil
	case AgentToolChoiceRequired:
		return map[string]any{"type": "any"}, nil
	case AgentToolChoiceTool:
		name := strings.TrimSpace(choice.Name)
		if name == "" {
			return nil, fmt.Errorf("tool_choice mode %q requires name", choice.Mode)
		}
		return map[string]any{"type": "tool", "name": name}, nil
	default:
		return nil, fmt.Errorf("unsupported tool_choice mode %q", choice.Mode)
	}
}

type anthropicMessagesResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func parseAnthropicAgentResponse(statusCode int, body []byte) (*AgentModelResponse, error) {
	var apiResp anthropicMessagesResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal Anthropic agent response: %w", err)
	}
	if apiResp.Error != nil {
		msg := apiResp.Error.Message
		if isContextLimitResponse(statusCode, body) || strings.Contains(strings.ToLower(apiResp.Error.Type), "context") {
			return nil, &ContextTooLargeError{StatusCode: statusCode, Message: msg}
		}
		return nil, fmt.Errorf("Anthropic agent API error: %s", msg)
	}
	content := []domain.AssistantAgentContentBlock{}
	toolCalls := []domain.AssistantAgentToolCall{}
	for i, block := range apiResp.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				content = append(content, domain.AssistantAgentContentBlock{Type: domain.AssistantAgentContentText, Text: block.Text})
			}
		case "tool_use":
			id := strings.TrimSpace(block.ID)
			name := strings.TrimSpace(block.Name)
			if id == "" {
				return nil, fmt.Errorf("Anthropic agent tool_use %d missing id", i)
			}
			if name == "" {
				return nil, fmt.Errorf("Anthropic agent tool_use %q missing name", id)
			}
			args := map[string]any{}
			if len(block.Input) > 0 && string(block.Input) != "null" {
				if err := json.Unmarshal(block.Input, &args); err != nil {
					return nil, fmt.Errorf("unmarshal input for Anthropic agent tool_use %q: %w", id, err)
				}
			}
			toolCalls = append(toolCalls, domain.AssistantAgentToolCall{ID: id, Name: name, Arguments: args})
		default:
			return nil, fmt.Errorf("Anthropic agent response has unsupported content block type %q", block.Type)
		}
	}
	if len(content) == 0 && len(toolCalls) == 0 {
		return nil, fmt.Errorf("Anthropic agent response has no content or tool_use blocks")
	}
	return &AgentModelResponse{Content: content, ToolCalls: toolCalls, StopReason: anthropicStopReason(apiResp.StopReason, len(toolCalls))}, nil
}

func anthropicStopReason(reason string, toolCallCount int) AgentStopReason {
	if toolCallCount > 0 || reason == "tool_use" {
		return AgentStopReasonToolCalls
	}
	switch reason {
	case "end_turn", "stop_sequence", "":
		return AgentStopReasonEndTurn
	case "max_tokens":
		return AgentStopReasonMaxTokens
	case "refusal", "content_filter":
		return AgentStopReasonContentGuard
	default:
		return AgentStopReasonUnknown
	}
}

var _ AgentModelClient = (*AnthropicAgentClient)(nil)
