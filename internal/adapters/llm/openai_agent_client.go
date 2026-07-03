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

// OpenAIAgentClientConfig configures an OpenAI-compatible chat-completions
// client that uses native tools/tool_calls for assistant agent turns.
type OpenAIAgentClientConfig struct {
	BaseURL     string
	Model       string
	APIKey      string
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
	HTTPClient  *http.Client
}

// OpenAIAgentClient serializes AgentModelRequest into /v1/chat/completions with
// native OpenAI-compatible tool-calling fields.
type OpenAIAgentClient struct {
	baseURL     string
	model       string
	apiKey      string
	maxTokens   int
	temperature float64
	httpClient  *http.Client
	logger      *slog.Logger
}

// NewOpenAIAgentClient creates an OpenAI-compatible agent model client.
func NewOpenAIAgentClient(config OpenAIAgentClientConfig, logger *slog.Logger) *OpenAIAgentClient {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.openai.com"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 2048
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
	return &OpenAIAgentClient{
		baseURL:     strings.TrimRight(config.BaseURL, "/"),
		model:       config.Model,
		apiKey:      config.APIKey,
		maxTokens:   config.MaxTokens,
		temperature: config.Temperature,
		httpClient:  config.HTTPClient,
		logger:      logger.With("component", "llm_openai_agent_client"),
	}
}

// Next asks the model for the next assistant turn.
func (c *OpenAIAgentClient) Next(ctx context.Context, req AgentModelRequest, onEvent AgentModelEventHandler) (*AgentModelResponse, error) {
	bodyMap, err := c.requestBody(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI agent request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send OpenAI agent request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read OpenAI agent response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isContextLimitResponse(resp.StatusCode, respBody) {
			return nil, &ContextTooLargeError{StatusCode: resp.StatusCode, Message: string(respBody)}
		}
		return nil, fmt.Errorf("OpenAI agent API error %d: %s", resp.StatusCode, string(respBody))
	}

	modelResp, err := parseOpenAIAgentResponse(resp.StatusCode, respBody)
	if err != nil {
		return nil, err
	}
	if onEvent != nil {
		onEvent(AgentModelEvent{Type: AgentModelEventCompleted, Content: modelResp.Content, StopReason: modelResp.StopReason})
	}
	return modelResp, nil
}

func (c *OpenAIAgentClient) requestBody(req AgentModelRequest) (map[string]any, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(c.model)
	}
	if model == "" {
		return nil, fmt.Errorf("llm model is required")
	}

	messages, err := openAIMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":       model,
		"messages":    messages,
		"max_tokens":  firstPositive(req.MaxTokens, c.maxTokens),
		"temperature": c.temperature,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		tools, err := openAITools(req.Tools)
		if err != nil {
			return nil, err
		}
		body["tools"] = tools
	}
	if req.ToolChoice.Mode != "" {
		choice, err := openAIToolChoice(req.ToolChoice)
		if err != nil {
			return nil, err
		}
		body["tool_choice"] = choice
	}
	return body, nil
}

func openAIMessages(messages []domain.AssistantAgentMessage) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for i, msg := range messages {
		switch msg.Role {
		case domain.AssistantAgentMessageRoleSystem, domain.AssistantAgentMessageRoleUser:
			content, err := contentBlocksString(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("serialize %s message %d: %w", msg.Role, i, err)
			}
			out = append(out, map[string]any{"role": string(msg.Role), "content": content})
		case domain.AssistantAgentMessageRoleAssistant:
			content, err := contentBlocksString(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("serialize assistant message %d: %w", i, err)
			}
			entry := map[string]any{"role": "assistant", "content": content}
			if len(msg.ToolCalls) > 0 {
				toolCalls, err := openAIMessageToolCalls(msg.ToolCalls)
				if err != nil {
					return nil, fmt.Errorf("serialize assistant message %d tool calls: %w", i, err)
				}
				entry["tool_calls"] = toolCalls
			}
			out = append(out, entry)
		case domain.AssistantAgentMessageRoleTool:
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, fmt.Errorf("tool message %d missing tool_call_id", i)
			}
			content, err := toolObservationContent(msg)
			if err != nil {
				return nil, fmt.Errorf("serialize tool message %d: %w", i, err)
			}
			entry := map[string]any{"role": "tool", "tool_call_id": msg.ToolCallID, "content": content}
			if msg.Name != "" {
				entry["name"] = msg.Name
			}
			out = append(out, entry)
		default:
			return nil, fmt.Errorf("message %d has unsupported role %q", i, msg.Role)
		}
	}
	return out, nil
}

func contentBlocksString(blocks []domain.AssistantAgentContentBlock) (string, error) {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "", domain.AssistantAgentContentText:
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case domain.AssistantAgentContentJSON:
			encoded, err := json.Marshal(block.JSON)
			if err != nil {
				return "", err
			}
			parts = append(parts, string(encoded))
		case domain.AssistantAgentContentObservation:
			encoded, err := json.Marshal(block.Observation)
			if err != nil {
				return "", err
			}
			parts = append(parts, string(encoded))
		case domain.AssistantAgentContentToolCall:
			continue
		default:
			return "", fmt.Errorf("unsupported content block type %q", block.Type)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func toolObservationContent(msg domain.AssistantAgentMessage) (string, error) {
	if msg.Observation != nil {
		encoded, err := json.Marshal(msg.Observation)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	return contentBlocksString(msg.Content)
}

func openAIMessageToolCalls(calls []domain.AssistantAgentToolCall) ([]map[string]any, error) {
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
		args := call.Arguments
		if args == nil {
			args = map[string]any{}
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("marshal arguments for tool call %q: %w", id, err)
		}
		out = append(out, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(encoded),
			},
		})
	}
	return out, nil
}

func openAITools(tools []AgentToolSchema) ([]map[string]any, error) {
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
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": tool.Description,
				"parameters":  schema,
			},
		})
	}
	return out, nil
}

func openAIToolChoice(choice AgentToolChoice) (any, error) {
	switch choice.Mode {
	case AgentToolChoiceAuto:
		return "auto", nil
	case AgentToolChoiceNone:
		return "none", nil
	case AgentToolChoiceRequired:
		return "required", nil
	case AgentToolChoiceTool:
		name := strings.TrimSpace(choice.Name)
		if name == "" {
			return nil, fmt.Errorf("tool_choice mode %q requires name", choice.Mode)
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
	default:
		return nil, fmt.Errorf("unsupported tool_choice mode %q", choice.Mode)
	}
}

type openAIChatCompletionResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func parseOpenAIAgentResponse(statusCode int, body []byte) (*AgentModelResponse, error) {
	var apiResp openAIChatCompletionResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal OpenAI agent response: %w", err)
	}
	if apiResp.Error != nil {
		msg := apiResp.Error.Message
		if isContextLimitResponse(statusCode, body) || strings.Contains(apiResp.Error.Code, "context_length") || strings.Contains(apiResp.Error.Type, "context_length") {
			return nil, &ContextTooLargeError{StatusCode: statusCode, Message: msg}
		}
		return nil, fmt.Errorf("OpenAI agent API error: %s", msg)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("OpenAI agent response missing choices")
	}
	choice := apiResp.Choices[0]
	content := []domain.AssistantAgentContentBlock{}
	if strings.TrimSpace(choice.Message.Content) != "" {
		content = append(content, domain.AssistantAgentContentBlock{Type: domain.AssistantAgentContentText, Text: choice.Message.Content})
	}
	toolCalls := make([]domain.AssistantAgentToolCall, 0, len(choice.Message.ToolCalls))
	for i, call := range choice.Message.ToolCalls {
		if call.Type != "" && call.Type != "function" {
			return nil, fmt.Errorf("OpenAI agent tool call %d has unsupported type %q", i, call.Type)
		}
		id := strings.TrimSpace(call.ID)
		name := strings.TrimSpace(call.Function.Name)
		if id == "" {
			return nil, fmt.Errorf("OpenAI agent tool call %d missing id", i)
		}
		if name == "" {
			return nil, fmt.Errorf("OpenAI agent tool call %q missing function name", id)
		}
		args := map[string]any{}
		argText := strings.TrimSpace(call.Function.Arguments)
		if argText != "" {
			if err := json.Unmarshal([]byte(argText), &args); err != nil {
				return nil, fmt.Errorf("unmarshal arguments for OpenAI agent tool call %q: %w", id, err)
			}
		}
		toolCalls = append(toolCalls, domain.AssistantAgentToolCall{ID: id, Name: name, Arguments: args})
	}
	if len(content) == 0 && len(toolCalls) == 0 {
		return nil, fmt.Errorf("OpenAI agent response has no content or tool calls")
	}
	return &AgentModelResponse{Content: content, ToolCalls: toolCalls, StopReason: openAIStopReason(choice.FinishReason, len(toolCalls))}, nil
}

func openAIStopReason(reason string, toolCallCount int) AgentStopReason {
	if toolCallCount > 0 {
		return AgentStopReasonToolCalls
	}
	switch reason {
	case "stop", "":
		return AgentStopReasonEndTurn
	case "tool_calls", "function_call":
		return AgentStopReasonToolCalls
	case "length":
		return AgentStopReasonMaxTokens
	case "content_filter":
		return AgentStopReasonContentGuard
	default:
		return AgentStopReasonUnknown
	}
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

var _ AgentModelClient = (*OpenAIAgentClient)(nil)
