package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const promptedToolCallFence = "```tool_call"

// PromptedAgentClientConfig configures an OpenAI-compatible chat-completions
// client that prompts text-only models to emit Bahia tool calls in-band.
type PromptedAgentClientConfig struct {
	BaseURL     string
	Model       string
	APIKey      string
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
	HTTPClient  *http.Client
}

// PromptedAgentClient serializes AgentModelRequest into ordinary
// /v1/chat/completions messages without native tools/tool_choice. It injects the
// Bahia tool catalog into the prompt and parses a strict fenced tool-call block
// from the model's text response.
type PromptedAgentClient struct {
	baseURL     string
	model       string
	apiKey      string
	maxTokens   int
	temperature float64
	httpClient  *http.Client
	logger      *slog.Logger
}

// NewPromptedAgentClient creates an OpenAI-compatible prompted tool-call client.
func NewPromptedAgentClient(config PromptedAgentClientConfig, logger *slog.Logger) *PromptedAgentClient {
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
	return &PromptedAgentClient{
		baseURL:     strings.TrimRight(config.BaseURL, "/"),
		model:       config.Model,
		apiKey:      config.APIKey,
		maxTokens:   config.MaxTokens,
		temperature: config.Temperature,
		httpClient:  config.HTTPClient,
		logger:      logger.With("component", "llm_prompted_agent_client"),
	}
}

// Next asks the model for the next assistant turn using a text tool harness.
func (c *PromptedAgentClient) Next(ctx context.Context, req AgentModelRequest, onEvent AgentModelEventHandler) (*AgentModelResponse, error) {
	bodyMap, err := c.requestBody(req)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("marshal prompted agent request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create prompted agent request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send prompted agent request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read prompted agent response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isContextLimitResponse(resp.StatusCode, respBody) {
			return nil, &ContextTooLargeError{StatusCode: resp.StatusCode, Message: string(respBody)}
		}
		return nil, fmt.Errorf("prompted agent API error %d: %s", resp.StatusCode, string(respBody))
	}

	modelResp, err := parsePromptedAgentResponse(resp.StatusCode, respBody)
	if err != nil {
		return nil, err
	}
	if onEvent != nil {
		onEvent(AgentModelEvent{Type: AgentModelEventCompleted, Content: modelResp.Content, StopReason: modelResp.StopReason})
	}
	return modelResp, nil
}

func (c *PromptedAgentClient) requestBody(req AgentModelRequest) (map[string]any, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(c.model)
	}
	if model == "" {
		return nil, fmt.Errorf("llm model is required")
	}

	messages, err := promptedMessages(req.Messages, req.Tools, req.ToolChoice)
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
	return body, nil
}

func promptedMessages(messages []domain.AssistantAgentMessage, tools []AgentToolSchema, choice AgentToolChoice) ([]map[string]any, error) {
	instructions, err := promptedToolInstructions(tools, choice)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(messages)+1)
	injected := false
	for i, msg := range messages {
		switch msg.Role {
		case domain.AssistantAgentMessageRoleSystem:
			content, err := contentBlocksString(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("serialize system message %d: %w", i, err)
			}
			if !injected {
				content = joinPromptSections(content, instructions)
				injected = true
			}
			out = append(out, map[string]any{"role": "system", "content": content})
		case domain.AssistantAgentMessageRoleUser:
			content, err := contentBlocksString(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("serialize user message %d: %w", i, err)
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		case domain.AssistantAgentMessageRoleAssistant:
			content, err := promptedAssistantContent(msg)
			if err != nil {
				return nil, fmt.Errorf("serialize assistant message %d: %w", i, err)
			}
			out = append(out, map[string]any{"role": "assistant", "content": content})
		case domain.AssistantAgentMessageRoleTool:
			content, err := promptedToolObservationContent(msg)
			if err != nil {
				return nil, fmt.Errorf("serialize tool observation message %d: %w", i, err)
			}
			out = append(out, map[string]any{"role": "user", "content": content})
		default:
			return nil, fmt.Errorf("message %d has unsupported role %q", i, msg.Role)
		}
	}
	if !injected {
		out = append([]map[string]any{{"role": "system", "content": instructions}}, out...)
	}
	return out, nil
}

func promptedToolInstructions(tools []AgentToolSchema, choice AgentToolChoice) (string, error) {
	var b strings.Builder
	b.WriteString("Bahia prompted tool-calling mode is active. The API transport cannot use native OpenAI tools, so you must follow this text protocol exactly.\n\n")
	b.WriteString("Reply with either a normal final answer in plain text, or exactly one tool call. Do not combine a final answer with a tool call.\n")
	b.WriteString("To call a tool, reply with exactly this fenced block format and valid JSON inside it:\n")
	b.WriteString("```tool_call\n")
	b.WriteString("{\"name\":\"<tool name>\",\"arguments\":{}}\n")
	b.WriteString("```\n")
	b.WriteString("The JSON object must contain name and arguments. arguments must be an object. Do not use Markdown fences for final answers.\n\n")
	if choice.Mode != "" {
		b.WriteString("Tool choice policy: ")
		switch choice.Mode {
		case AgentToolChoiceAuto:
			b.WriteString("call one tool only when it is needed; otherwise answer normally.")
		case AgentToolChoiceNone:
			b.WriteString("do not call tools; answer normally.")
		case AgentToolChoiceRequired:
			b.WriteString("call exactly one available tool.")
		case AgentToolChoiceTool:
			name := strings.TrimSpace(choice.Name)
			if name == "" {
				return "", fmt.Errorf("tool_choice mode %q requires name", choice.Mode)
			}
			b.WriteString("call exactly this tool: ")
			b.WriteString(name)
			b.WriteString(".")
		default:
			return "", fmt.Errorf("unsupported tool_choice mode %q", choice.Mode)
		}
		b.WriteString("\n\n")
	}
	b.WriteString("Available tools as JSON Schema descriptors:\n")
	catalog, err := promptedToolCatalog(tools)
	if err != nil {
		return "", err
	}
	b.Write(catalog)
	return b.String(), nil
}

func promptedToolCatalog(tools []AgentToolSchema) ([]byte, error) {
	catalog := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return nil, fmt.Errorf("tool schema %d missing name", i)
		}
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		catalog = append(catalog, map[string]any{
			"name":        name,
			"description": tool.Description,
			"parameters":  schema,
		})
	}
	encoded, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal prompted tool catalog: %w", err)
	}
	return encoded, nil
}

func promptedAssistantContent(msg domain.AssistantAgentMessage) (string, error) {
	parts := []string{}
	content, err := contentBlocksString(msg.Content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) != "" {
		parts = append(parts, content)
	}
	if len(msg.ToolCalls) > 0 {
		parts = append(parts, "Assistant requested tool call(s):")
		for _, call := range msg.ToolCalls {
			args := call.Arguments
			if args == nil {
				args = map[string]any{}
			}
			encoded, err := json.MarshalIndent(args, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal arguments for tool call %q: %w", call.ID, err)
			}
			parts = append(parts, fmt.Sprintf("Tool call ID: %s\nTool name: %s\nArguments:\n```json\n%s\n```", call.ID, call.Name, string(encoded)))
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func promptedToolObservationContent(msg domain.AssistantAgentMessage) (string, error) {
	name := strings.TrimSpace(msg.Name)
	if name == "" && msg.Observation != nil {
		name = msg.Observation.ToolName
	}
	payload, err := toolObservationContent(msg)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Tool result for call")
	if strings.TrimSpace(msg.ToolCallID) != "" {
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(msg.ToolCallID))
	}
	if name != "" {
		b.WriteString(" (")
		b.WriteString(name)
		b.WriteString(")")
	}
	b.WriteString(":\n```json\n")
	b.WriteString(payload)
	b.WriteString("\n```")
	return b.String(), nil
}

func joinPromptSections(first string, second string) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "\n\n" + second
}

func parsePromptedAgentResponse(statusCode int, body []byte) (*AgentModelResponse, error) {
	var apiResp openAIChatCompletionResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal prompted agent response: %w", err)
	}
	if apiResp.Error != nil {
		msg := apiResp.Error.Message
		if isContextLimitResponse(statusCode, body) || strings.Contains(apiResp.Error.Code, "context_length") || strings.Contains(apiResp.Error.Type, "context_length") {
			return nil, &ContextTooLargeError{StatusCode: statusCode, Message: msg}
		}
		return nil, fmt.Errorf("prompted agent API error: %s", msg)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("prompted agent response missing choices")
	}
	choice := apiResp.Choices[0]
	text := choice.Message.Content
	if call, ok := extractPromptedToolCall(text); ok {
		return &AgentModelResponse{
			ToolCalls:  []domain.AssistantAgentToolCall{call},
			StopReason: AgentStopReasonToolCalls,
			Raw:        map[string]any{"prompted_tool_call_format": "fenced_tool_call_json"},
		}, nil
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("prompted agent response has no content")
	}
	return &AgentModelResponse{
		Content:    []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: text}},
		StopReason: openAIStopReason(choice.FinishReason, 0),
	}, nil
}

type promptedToolCallEnvelope struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func extractPromptedToolCall(text string) (domain.AssistantAgentToolCall, bool) {
	start := strings.Index(text, promptedToolCallFence)
	if start < 0 {
		return domain.AssistantAgentToolCall{}, false
	}
	afterFence := text[start+len(promptedToolCallFence):]
	if strings.HasPrefix(afterFence, "\r\n") {
		afterFence = afterFence[2:]
	} else if strings.HasPrefix(afterFence, "\n") {
		afterFence = afterFence[1:]
	}
	end := strings.Index(afterFence, "```")
	if end < 0 {
		return domain.AssistantAgentToolCall{}, false
	}
	block := strings.TrimSpace(afterFence[:end])
	if block == "" {
		return domain.AssistantAgentToolCall{}, false
	}
	var envelope promptedToolCallEnvelope
	if err := json.Unmarshal([]byte(block), &envelope); err != nil {
		return domain.AssistantAgentToolCall{}, false
	}
	name := strings.TrimSpace(envelope.Name)
	if name == "" {
		return domain.AssistantAgentToolCall{}, false
	}
	if envelope.Arguments == nil {
		envelope.Arguments = map[string]any{}
	}
	return domain.AssistantAgentToolCall{
		ID:        promptedToolCallID(name, envelope.Arguments, block),
		Name:      name,
		Arguments: envelope.Arguments,
		Metadata:  map[string]any{"source": "prompted_tool_call"},
	}, true
}

func promptedToolCallID(name string, args map[string]any, block string) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		encoded = []byte(block)
	}
	sum := sha256.Sum256([]byte(name + "\n" + string(encoded) + "\n" + block))
	return fmt.Sprintf("prompted_call_%x", sum[:8])
}

var _ AgentModelClient = (*PromptedAgentClient)(nil)
