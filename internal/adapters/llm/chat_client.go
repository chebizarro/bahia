package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ChatClientConfig configures an OpenAI-compatible chat completions client.
type ChatClientConfig struct {
	BaseURL     string
	Model       string
	APIKey      string
	MaxTokens   int
	Temperature float64
	Timeout     time.Duration
}

// ContextTooLargeError marks provider failures caused by context-window limits.
type ContextTooLargeError struct {
	StatusCode int
	Message    string
}

func (e *ContextTooLargeError) Error() string {
	if e.Message == "" {
		return "llm context too large"
	}
	return fmt.Sprintf("llm context too large: %s", e.Message)
}

// IsContextTooLarge reports whether err is a ContextTooLargeError.
func IsContextTooLarge(err error) bool {
	var target *ContextTooLargeError
	return errors.As(err, &target)
}

// ChatClient plans assistant turns using an OpenAI-compatible chat completions endpoint.
type ChatClient struct {
	baseURL     string
	model       string
	apiKey      string
	maxTokens   int
	temperature float64
	httpClient  *http.Client
	logger      *slog.Logger
}

// NewChatClient creates a new planning chat client.
func NewChatClient(config ChatClientConfig, logger *slog.Logger) *ChatClient {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.openai.com"
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = 2048
	}
	if config.Timeout == 0 {
		config.Timeout = 120 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ChatClient{
		baseURL:     strings.TrimRight(config.BaseURL, "/"),
		model:       config.Model,
		apiKey:      config.APIKey,
		maxTokens:   config.MaxTokens,
		temperature: config.Temperature,
		httpClient:  &http.Client{Timeout: config.Timeout},
		logger:      logger.With("component", "llm_chat_client"),
	}
}

// PlanFromPrompt sends a single non-streaming planning request and parses an AssistantPlan.
func (c *ChatClient) PlanFromPrompt(ctx context.Context, systemPrompt string, userPrompt string) (*domain.AssistantPlan, error) {
	content, err := c.callChatCompletions(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, err
	}
	plan, err := parseAssistantPlan(content)
	if err != nil {
		c.logger.Warn("invalid assistant plan JSON", "error", err)
		return &domain.AssistantPlan{
			Summary:            "The planning model returned invalid JSON.",
			NeedsClarification: true,
			ClarifyingQuestion: "I could not parse the generated plan. Please restate the request with explicit target resources and desired action.",
			RiskLevel:          "low",
			Steps:              []domain.AssistantPlanStep{},
		}, nil
	}
	return plan, nil
}

// PlanFromPromptStreaming streams planning tokens and parses the accumulated response into an AssistantPlan.
func (c *ChatClient) PlanFromPromptStreaming(ctx context.Context, systemPrompt, userPrompt string, onChunk func(chunk string)) (*domain.AssistantPlan, error) {
	content, err := c.callChatCompletionsStreaming(ctx, systemPrompt, userPrompt, onChunk)
	if err != nil {
		return nil, err
	}
	plan, err := parseAssistantPlan(content)
	if err != nil {
		c.logger.Warn("invalid streamed assistant plan JSON", "error", err)
		return &domain.AssistantPlan{
			Summary:            "The planning model returned invalid JSON.",
			NeedsClarification: true,
			ClarifyingQuestion: "I could not parse the generated plan. Please restate the request with explicit target resources and desired action.",
			RiskLevel:          "low",
			Steps:              []domain.AssistantPlanStep{},
		}, nil
	}
	return plan, nil
}

func (c *ChatClient) callChatCompletions(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	reqBody, err := c.chatCompletionRequestBody(systemPrompt, userPrompt, false)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal chat completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create chat completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send chat completion request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read chat completion response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if isContextLimitResponse(resp.StatusCode, respBody) {
			return "", &ContextTooLargeError{StatusCode: resp.StatusCode, Message: string(respBody)}
		}
		return "", fmt.Errorf("chat completion API error %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal chat completion response: %w", err)
	}
	if apiResp.Error != nil {
		msg := apiResp.Error.Message
		if strings.Contains(apiResp.Error.Code, "context_length") || strings.Contains(apiResp.Error.Type, "context_length") {
			return "", &ContextTooLargeError{StatusCode: resp.StatusCode, Message: msg}
		}
		return "", fmt.Errorf("chat completion API error: %s", msg)
	}
	if len(apiResp.Choices) == 0 || strings.TrimSpace(apiResp.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty response from chat completion API")
	}
	return apiResp.Choices[0].Message.Content, nil
}

func (c *ChatClient) callChatCompletionsStreaming(ctx context.Context, systemPrompt, userPrompt string, onChunk func(chunk string)) (string, error) {
	reqBody, err := c.chatCompletionRequestBody(systemPrompt, userPrompt, true)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal streaming chat completion request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create streaming chat completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send streaming chat completion request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return "", fmt.Errorf("read streaming chat completion error response: %w", readErr)
		}
		if isContextLimitResponse(resp.StatusCode, respBody) {
			return "", &ContextTooLargeError{StatusCode: resp.StatusCode, Message: string(respBody)}
		}
		return "", fmt.Errorf("streaming chat completion API error %d: %s", resp.StatusCode, string(respBody))
	}

	var accumulated strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		chunk, err := parseStreamingDelta(data)
		if err != nil {
			return "", err
		}
		if chunk == "" {
			continue
		}
		accumulated.WriteString(chunk)
		if onChunk != nil {
			onChunk(chunk)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read streaming chat completion response: %w", err)
	}
	if strings.TrimSpace(accumulated.String()) == "" {
		return "", fmt.Errorf("empty response from streaming chat completion API")
	}
	return accumulated.String(), nil
}

func (c *ChatClient) chatCompletionRequestBody(systemPrompt, userPrompt string, stream bool) (map[string]any, error) {
	if strings.TrimSpace(c.model) == "" {
		return nil, fmt.Errorf("llm model is required")
	}
	return map[string]any{
		"model":       c.model,
		"max_tokens":  c.maxTokens,
		"temperature": c.temperature,
		"stream":      stream,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt + "\n\nReturn only JSON matching the AssistantPlan schema."},
			{"role": "user", "content": userPrompt},
		},
		"response_format": assistantPlanResponseFormat(),
	}, nil
}

func parseStreamingDelta(data string) (string, error) {
	var apiResp struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(data), &apiResp); err != nil {
		return "", fmt.Errorf("unmarshal streaming chat completion chunk: %w", err)
	}
	if apiResp.Error != nil {
		return "", fmt.Errorf("streaming chat completion API error: %s", apiResp.Error.Message)
	}
	if len(apiResp.Choices) == 0 {
		return "", nil
	}
	return apiResp.Choices[0].Delta.Content, nil
}

func parseAssistantPlan(content string) (*domain.AssistantPlan, error) {
	jsonStr := strings.TrimSpace(content)
	if strings.Contains(jsonStr, "```json") {
		start := strings.Index(jsonStr, "```json") + len("```json")
		end := strings.LastIndex(jsonStr, "```")
		if end > start {
			jsonStr = strings.TrimSpace(jsonStr[start:end])
		}
	} else if strings.Contains(jsonStr, "```") {
		start := strings.Index(jsonStr, "```") + len("```")
		end := strings.LastIndex(jsonStr, "```")
		if end > start {
			jsonStr = strings.TrimSpace(jsonStr[start:end])
		}
	}
	var plan domain.AssistantPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, err
	}
	if plan.RiskLevel == "" {
		plan.RiskLevel = "low"
	}
	if plan.Steps == nil {
		plan.Steps = []domain.AssistantPlanStep{}
	}
	return &plan, nil
}

func isContextLimitResponse(statusCode int, body []byte) bool {
	lower := strings.ToLower(string(body))
	return statusCode == http.StatusRequestEntityTooLarge ||
		strings.Contains(lower, "context_length_exceeded") ||
		strings.Contains(lower, "context length") ||
		strings.Contains(lower, "maximum context")
}

func assistantPlanResponseFormat() map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "assistant_plan",
			"strict": true,
			"schema": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"summary", "needs_clarification", "risk_level", "steps"},
				"properties": map[string]any{
					"summary":             map[string]any{"type": "string"},
					"needs_clarification": map[string]any{"type": "boolean"},
					"clarifying_question": map[string]any{"type": "string"},
					"risk_level":          map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
					"context_refs":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"steps": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"step_id", "title", "description", "tool_name", "tool_args"},
							"properties": map[string]any{
								"step_id":         map[string]any{"type": "string"},
								"title":           map[string]any{"type": "string"},
								"description":     map[string]any{"type": "string"},
								"tool_name":       map[string]any{"type": "string"},
								"tool_args":       map[string]any{"type": "object"},
								"args_preview":    map[string]any{"type": "object"},
								"idempotency_key": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}
}

var _ interface {
	PlanFromPrompt(ctx context.Context, systemPrompt string, userPrompt string) (*domain.AssistantPlan, error)
	PlanFromPromptStreaming(ctx context.Context, systemPrompt, userPrompt string, onChunk func(chunk string)) (*domain.AssistantPlan, error)
} = (*ChatClient)(nil)
